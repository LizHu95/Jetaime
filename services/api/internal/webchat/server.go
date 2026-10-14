package webchat

import (
	"crypto/rand"
	"crypto/subtle"
	"embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"golang.org/x/crypto/bcrypt"
)

//go:embed web/*
var assets embed.FS

type User struct {
	ID           string `json:"id"`
	Name         string `json:"name"`
	PasswordHash string `json:"passwordHash"`
}
type Message struct {
	ID     int       `json:"id"`
	UserID string    `json:"userId"`
	Name   string    `json:"name"`
	Text   string    `json:"text"`
	SentAt time.Time `json:"sentAt"`
	Bot    bool      `json:"bot,omitempty"`
}
type diskState struct {
	Users    []User    `json:"users"`
	Messages []Message `json:"messages"`
}
type session struct {
	userID  string
	expires time.Time
}
type attempt struct {
	count int
	until time.Time
}
type Server struct {
	mu                         sync.Mutex
	state                      diskState
	sessions                   map[string]session
	attempts                   map[string]attempt
	path, invite, origin, host string
	secure                     bool
	handler                    http.Handler
}

func NewServer(path, invite, publicURL string) (*Server, error) {
	if len(invite) < 16 {
		return nil, errors.New("WEBCHAT_INVITE_CODE must contain at least 16 characters")
	}
	s := &Server{path: path, invite: invite, sessions: map[string]session{}, attempts: map[string]attempt{}}
	if publicURL != "" {
		u, err := url.Parse(publicURL)
		if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || (u.Path != "" && u.Path != "/") || u.RawQuery != "" || u.Fragment != "" {
			return nil, errors.New("public URL must be an HTTPS origin, for example https://chat.example.com")
		}
		s.origin, s.host, s.secure = "https://"+u.Host, u.Host, true
	}
	data, err := os.ReadFile(path)
	if err == nil {
		if err = json.Unmarshal(data, &s.state); err != nil {
			return nil, err
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	if err = os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return nil, err
	}
	web, err := fs.Sub(assets, "web")
	if err != nil {
		return nil, err
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/webchat/session", s.withRequest(false, s.handleSession))
	mux.HandleFunc("POST /api/webchat/login", s.withRequest(false, s.handleAuth))
	mux.HandleFunc("POST /api/webchat/register", s.withRequest(false, s.handleAuth))
	mux.HandleFunc("POST /api/webchat/logout", s.withRequest(true, s.handleLogout))
	mux.HandleFunc("GET /api/webchat/messages", s.withRequest(true, s.handleListMessages))
	mux.HandleFunc("POST /api/webchat/messages", s.withRequest(true, s.handleSendMessage))
	files := http.FileServer(http.FS(web))
	for _, path := range []string{"/{$}", "/style.css", "/app.js"} {
		mux.Handle("GET "+path, files)
	}
	s.handler = mux
	return s, nil
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if s.host != "" {
		if r.Host != s.host {
			respond(w, 403, map[string]string{"error": "访问地址不匹配"})
			return
		}
	} else {
		host := r.Host
		if h, _, err := net.SplitHostPort(host); err == nil {
			host = h
		}
		ip := net.ParseIP(host)
		if host != "localhost" && (ip == nil || !ip.IsLoopback()) {
			respond(w, 403, map[string]string{"error": "请使用本机地址"})
			return
		}
	}
	origin := s.origin
	if origin == "" {
		origin = "http://" + r.Host
	}
	if (r.Header.Get("Origin") != "" && r.Header.Get("Origin") != origin) || r.Header.Get("Sec-Fetch-Site") == "cross-site" {
		respond(w, 403, map[string]string{"error": "不允许跨站请求"})
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self'; img-src 'self' data:; connect-src 'self'; frame-ancestors 'none'; base-uri 'none'; form-action 'self'")
	w.Header().Set("Referrer-Policy", "no-referrer")
	s.handler.ServeHTTP(w, r)
}

type apiRequest struct {
	Name     string `json:"name"`
	Password string `json:"password"`
	Invite   string `json:"invite"`
	Text     string `json:"text"`
}

func (s *Server) withRequest(authenticated bool, next func(http.ResponseWriter, *http.Request, apiRequest, *User)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var input apiRequest

		if r.Method == http.MethodPost {
			if !strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
				respond(w, 415, map[string]string{"error": "需要 JSON 请求"})
				return
			}
			r.Body = http.MaxBytesReader(w, r.Body, 8192)
			defer func() { _ = r.Body.Close() }()
			d := json.NewDecoder(r.Body)
			d.DisallowUnknownFields()
			if d.Decode(&input) != nil {
				respond(w, 400, map[string]string{"error": "请求格式错误"})
				return
			}
			var extra any
			if d.Decode(&extra) != io.EOF {
				respond(w, 400, map[string]string{"error": "请求格式错误"})
				return
			}
		}
		s.mu.Lock()
		defer s.mu.Unlock()
		current := s.currentUser(r)
		if authenticated && current == nil {
			respond(w, 401, map[string]string{"error": "登录已过期，请重新登录"})
			return
		}
		next(w, r, input, current)
	}
}

func (s *Server) handleSession(w http.ResponseWriter, r *http.Request, input apiRequest, current *User) {
	if current == nil {
		respond(w, 401, map[string]string{"error": "请先登录"})
	} else {
		respond(w, 200, publicUser(*current))
	}
	return
}

func (s *Server) handleAuth(w http.ResponseWriter, r *http.Request, input apiRequest, current *User) {
	if !s.allowAttempt(r) {
		respond(w, 429, map[string]string{"error": "尝试过于频繁，请稍后再试"})
		return
	}
	input.Name = strings.TrimSpace(input.Name)
	if utf8.RuneCountInString(input.Name) < 2 || utf8.RuneCountInString(input.Name) > 24 || len(input.Password) < 10 || len(input.Password) > 72 {
		respond(w, 400, map[string]string{"error": "用户名需 2–24 字，密码需 10–72 字节"})
		return
	}
	var found *User
	for i := range s.state.Users {
		if strings.EqualFold(s.state.Users[i].Name, input.Name) {
			found = &s.state.Users[i]
			break
		}
	}
	if r.URL.Path == "/api/webchat/register" {
		if subtle.ConstantTimeCompare([]byte(input.Invite), []byte(s.invite)) != 1 {
			respond(w, 403, map[string]string{"error": "邀请码不正确"})
			return
		}
		if found != nil {
			respond(w, 409, map[string]string{"error": "用户名已使用"})
			return
		}
		hash, err := bcrypt.GenerateFromPassword([]byte(input.Password), bcrypt.DefaultCost)
		if err != nil {
			respond(w, 500, map[string]string{"error": "注册失败"})
			return
		}
		id, err := randomToken()
		if err != nil {
			respond(w, 500, map[string]string{"error": "注册失败"})
			return
		}
		user := User{ID: id, Name: input.Name, PasswordHash: string(hash)}
		s.state.Users = append(s.state.Users, user)
		if err = s.save(); err != nil {
			s.state.Users = s.state.Users[:len(s.state.Users)-1]
			respond(w, 500, map[string]string{"error": "无法保存账号"})
			return
		}
		found = &user
	} else if found == nil {
		// Perform password hashing even for an unknown account.
		_, _ = bcrypt.GenerateFromPassword([]byte(input.Password), bcrypt.DefaultCost)
		respond(w, 401, map[string]string{"error": "用户名或密码不正确"})
		return
	} else if bcrypt.CompareHashAndPassword([]byte(found.PasswordHash), []byte(input.Password)) != nil {
		respond(w, 401, map[string]string{"error": "用户名或密码不正确"})
		return
	}
	token, err := randomToken()
	if err != nil {
		respond(w, 500, map[string]string{"error": "登录失败"})
		return
	}
	s.sessions[token] = session{userID: found.ID, expires: time.Now().Add(7 * 24 * time.Hour)}
	s.setCookie(w, token, 7*24*3600)
	respond(w, 200, publicUser(*found))
	return
}

func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request, input apiRequest, current *User) {
	if cookie, err := r.Cookie("jetaime_session"); err == nil {
		delete(s.sessions, cookie.Value)
	}
	s.setCookie(w, "", -1)
	respond(w, 200, map[string]bool{"ok": true})
}

func (s *Server) handleListMessages(w http.ResponseWriter, r *http.Request, input apiRequest, current *User) {
	messages := s.state.Messages
	if len(messages) > 200 {
		messages = messages[len(messages)-200:]
	}
	if messages == nil {
		messages = []Message{}
	}
	respond(w, 200, map[string]any{"messages": messages, "user": publicUser(*current)})
}

func (s *Server) handleSendMessage(w http.ResponseWriter, r *http.Request, input apiRequest, current *User) {
	text := strings.TrimSpace(input.Text)
	if text == "" || utf8.RuneCountInString(text) > 2000 {
		respond(w, 400, map[string]string{"error": "消息需 1–2000 字"})
		return
	}
	before := len(s.state.Messages)
	now := time.Now()
	if before > 0 {
		previous := s.state.Messages[before-1]
		if now.Sub(previous.SentAt) < time.Second {
			respond(w, 429, map[string]string{"error": "发送太快，请稍后再试"})
			return
		}
	}
	s.state.Messages = append(s.state.Messages, Message{ID: before + 1, UserID: current.ID, Name: current.Name, Text: text, SentAt: now}, Message{ID: before + 2, UserID: "bot", Name: "Jetaime", Text: "我一直都在", SentAt: now, Bot: true})
	if err := s.save(); err != nil {
		s.state.Messages = s.state.Messages[:before]
		respond(w, 500, map[string]string{"error": "消息保存失败"})
		return
	}
	respond(w, 200, map[string]bool{"ok": true})
}

func publicUser(u User) map[string]string { return map[string]string{"id": u.ID, "name": u.Name} }
func randomToken() (string, error) {
	b := make([]byte, 32)
	_, err := rand.Read(b)
	return hex.EncodeToString(b), err
}
func (s *Server) currentUser(r *http.Request) *User {
	now := time.Now()
	for token, item := range s.sessions {
		if !now.Before(item.expires) {
			delete(s.sessions, token)
		}
	}
	c, err := r.Cookie("jetaime_session")
	if err != nil {
		return nil
	}
	item, ok := s.sessions[c.Value]
	if !ok {
		return nil
	}
	for i := range s.state.Users {
		if s.state.Users[i].ID == item.userID {
			return &s.state.Users[i]
		}
	}
	return nil
}
func (s *Server) setCookie(w http.ResponseWriter, value string, maxAge int) {
	http.SetCookie(w, &http.Cookie{Name: "jetaime_session", Value: value, Path: "/", HttpOnly: true, Secure: s.secure, SameSite: http.SameSiteLaxMode, MaxAge: maxAge})
}
func (s *Server) allowAttempt(r *http.Request) bool {
	now := time.Now()
	for key, item := range s.attempts {
		if now.After(item.until) {
			delete(s.attempts, key)
		}
	}
	key, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		key = r.RemoteAddr
	}
	item := s.attempts[key]
	if item.until.IsZero() {
		if len(s.attempts) >= 1024 {
			return false
		}
		item.until = now.Add(time.Minute)
	}
	item.count++
	s.attempts[key] = item
	return item.count <= 10
}
func (s *Server) save() error {
	data, err := json.Marshal(s.state)
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(s.path), ".webchat-*")
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(f.Name()) }()
	if _, err = f.Write(data); err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	return os.Rename(f.Name(), s.path)
}
func respond(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

// RegisterRoutes mounts the API and page with the module's host and session checks.
func (s *Server) RegisterRoutes(mux *http.ServeMux) {
	mux.Handle("/api/webchat/", s)
	mux.Handle("/page/webchat/", http.StripPrefix("/page/webchat", s))
}
