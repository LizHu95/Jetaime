package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/LizHu95/Jetaime/services/api/internal/assistant"
	"github.com/LizHu95/Jetaime/services/api/internal/httpapi"
	"github.com/LizHu95/Jetaime/services/api/internal/llm"
	"github.com/LizHu95/Jetaime/services/api/internal/webchat"
)

func main() {
	addr := flag.String("addr", "127.0.0.1:8081", "loopback listen address")
	assistantEnabled := flag.Bool("assistant", true, "enable local assistant console")
	webchatEnabled := flag.Bool("webchat", false, "enable web chat (requires WEBCHAT_INVITE_CODE)")
	webchatState := flag.String("webchat-state", "../../local/data/webchat/state.json", "persistent web chat accounts and messages")
	publicURL := flag.String("public-url", os.Getenv("WEBCHAT_PUBLIC_URL"), "web chat HTTPS origin behind a reverse proxy")
	fixture := flag.String("fixture", "../../local/data/chat-history/history.fixture.json", "editable history fixture")
	state := flag.String("state", "../../local/data/assistant/state.json", "persisted messages and tasks")
	logs := flag.String("log", "../../local/data/assistant/runs.jsonl", "workflow trace log")
	modelFlags := llm.RegisterFlags(flag.CommandLine)
	reviewCache := flag.Bool("experimental-review-cache", false, "opt in to experimental review caching; disabled by default")
	timeout := flag.Duration("model-timeout", 5*time.Minute, "model timeout")
	tokens := flag.Int("context-tokens", 98304, "context capacity")
	flag.Parse()
	if flag.NArg() != 0 {
		log.Fatal("unexpected arguments")
	}
	host, _, err := net.SplitHostPort(*addr)
	if err != nil || net.ParseIP(host) == nil || !net.ParseIP(host).IsLoopback() {
		log.Fatal("addr must use a loopback IP")
	}
	if !*assistantEnabled && !*webchatEnabled {
		log.Fatal("enable at least one HTTP module with -assistant or -webchat")
	}
	paths := []string{*fixture, *state, *logs}
	if *assistantEnabled {
		if *webchatEnabled {
			paths = append(paths, *webchatState)
		}
		seen := make(map[string]bool, len(paths))
		for i, path := range paths {
			paths[i], err = filepath.Abs(path)
			if err != nil {
				log.Fatal(err)
			}
			if seen[paths[i]] {
				log.Fatal("fixture, state, log and webchat state must be separate files")
			}
			seen[paths[i]] = true
		}
	}
	deps := httpapi.Dependencies{}
	if *assistantEnabled {
		config, err := modelFlags.Resolve(*tokens)
		if err != nil {
			log.Fatal(err)
		}
		provider, err := config.New(*timeout)
		if err != nil {
			log.Fatal(err)
		}
		store, err := assistant.OpenStore(paths[0], paths[1])
		if err != nil {
			log.Fatal(err)
		}
		workflow, err := assistant.NewWorkflow(store, provider, config.Model, config.ContextTokens, paths[2])
		if err != nil {
			log.Fatal(err)
		}
		workflow.EnableReviewCache = *reviewCache
		deps.Assistant = assistant.NewServer(workflow)
		fmt.Printf("Model: %s (%s), profile: %s\n", config.Model, config.Provider, config.Profile)
	}
	if *webchatEnabled {
		chat, err := webchat.NewServer(*webchatState, os.Getenv("WEBCHAT_INVITE_CODE"), *publicURL)
		if err != nil {
			log.Fatal(err)
		}
		deps.Webchat = chat
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	listener, err := net.Listen("tcp", *addr)
	if err != nil {
		log.Fatal(err)
	}
	server := &http.Server{Handler: httpapi.NewRouter(deps), ReadHeaderTimeout: 5 * time.Second, IdleTimeout: 60 * time.Second, BaseContext: func(net.Listener) context.Context { return ctx }}
	fmt.Printf("HTTP API: http://%s\n", listener.Addr())
	if *assistantEnabled {
		fmt.Printf("Assistant: http://%s/page/assistant/\n", listener.Addr())
	}
	if *webchatEnabled {
		fmt.Printf("Web chat: http://%s/page/webchat/\n", listener.Addr())
	}
	go func() {
		if err := server.Serve(listener); err != nil && err != http.ErrServerClosed {
			log.Print(err)
			stop()
		}
	}()
	<-ctx.Done()
	shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := server.Shutdown(shutdown); err != nil {
		_ = server.Close()
	}
}
