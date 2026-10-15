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
	"github.com/LizHu95/Jetaime/services/api/internal/auth"
	"github.com/LizHu95/Jetaime/services/api/internal/chat"
	"github.com/LizHu95/Jetaime/services/api/internal/contacts"
	"github.com/LizHu95/Jetaime/services/api/internal/database"
	"github.com/LizHu95/Jetaime/services/api/internal/httpapi"
	"github.com/LizHu95/Jetaime/services/api/internal/llm"
	"github.com/LizHu95/Jetaime/services/api/internal/webpush"
)

func main() {
	if err := run(); err != nil {
		log.Fatal(err)
	}
}

func run() error {
	addr := flag.String("addr", "127.0.0.1:8081", "listen address; assistant requires a loopback IP")
	databaseEnabled := flag.Bool("database", false, "enable PostgreSQL using JETAIME_DATABASE_URL; requires current schema")
	authEnabled := flag.Bool("auth", true, "enable account APIs by default; set -auth=false to disable; requires PostgreSQL using JETAIME_DATABASE_URL")
	migrateOnly := flag.Bool("migrate", false, "apply embedded PostgreSQL migrations and exit")
	chatAI := flag.Bool("chat-ai", true, "enable shared group AI worker by default with authentication; set -chat-ai=false to disable; requires model configuration")
	assistantEnabled := flag.Bool("assistant", false, "enable local assistant console")
	fixture := flag.String("fixture", "../../local/data/chat-history/history.fixture.json", "editable history fixture")
	state := flag.String("state", "../../local/data/assistant/state.json", "persisted messages and tasks")
	logs := flag.String("log", "../../local/data/assistant/runs.jsonl", "workflow trace log")
	modelFlags := llm.RegisterFlags(flag.CommandLine)
	reviewCache := flag.Bool("experimental-review-cache", false, "opt in to experimental review caching; disabled by default")
	timeout := flag.Duration("model-timeout", 5*time.Minute, "model timeout")
	tokens := flag.Int("context-tokens", 98304, "context capacity")
	flag.Parse()
	if flag.NArg() != 0 {
		return fmt.Errorf("unexpected arguments")
	}
	host, _, err := net.SplitHostPort(*addr)
	if err != nil || net.ParseIP(host) == nil {
		return fmt.Errorf("addr must use an IP and port")
	}
	if *assistantEnabled && !net.ParseIP(host).IsLoopback() {
		return fmt.Errorf("assistant requires a loopback listen IP")
	}
	paths := []string{*fixture, *state, *logs}
	if *assistantEnabled {
		seen := make(map[string]bool, len(paths))
		for i, path := range paths {
			paths[i], err = filepath.Abs(path)
			if err != nil {
				return err
			}
			if seen[paths[i]] {
				return fmt.Errorf("fixture, state and log must be separate files")
			}
			seen[paths[i]] = true
		}
	}
	chatAIExplicit := false
	flag.Visit(func(f *flag.Flag) {
		if f.Name == "chat-ai" {
			chatAIExplicit = true
		}
	})
	if chatAIExplicit && *chatAI && !*authEnabled {
		return fmt.Errorf("chat-ai requires auth")
	}
	var modules []httpapi.Module
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if *databaseEnabled || *migrateOnly || *authEnabled {
		startup, cancel := context.WithTimeout(ctx, 30*time.Second)
		defer cancel()
		pool, err := database.Open(startup, os.Getenv("JETAIME_DATABASE_URL"))
		if err != nil {
			return err
		}
		defer pool.Close()
		if *migrateOnly {
			if err := database.Migrate(startup, pool); err != nil {
				return err
			}
			fmt.Println("PostgreSQL migrations are current")
			return nil
		}
		if err := database.CheckSchema(startup, pool); err != nil {
			return err
		}
		fmt.Println("PostgreSQL connected; schema verified")
		if *authEnabled {
			service, err := auth.NewService(auth.NewPostgresStore(pool))
			if err != nil {
				return fmt.Errorf("initialize authentication: %w", err)
			}
			authentication := auth.NewHandler(service)
			pushConfig, err := webpush.FromEnv()
			if err != nil {
				return err
			}
			modules = append(modules, webpush.NewHandler(pool, authentication, pushConfig))
			go webpush.NewWorker(pool, pushConfig).Work(ctx)
			modules = append(modules, authentication, contacts.NewHandler(contacts.NewService(contacts.NewPostgresStore(pool)), authentication))
			var model llm.ChatModel
			var modelName string
			capacity := *tokens
			if *chatAI {
				config, err := modelFlags.Resolve(*tokens)
				if err != nil {
					return err
				}
				provider, err := config.New(*timeout)
				if err != nil {
					return err
				}
				model, modelName, capacity = provider, config.Model, config.ContextTokens
				fmt.Printf("Group AI: %s (%s)\n", config.Model, config.Provider)
			}
			chatStore := chat.NewStore(pool)
			chatService := chat.NewService(chatStore, model, modelName, capacity, *timeout)
			realtime := chat.NewRealtime(ctx, chatStore, service)
			go realtime.Listen(ctx)
			go chatService.Work(ctx)
			go chatService.Work(ctx)
			modules = append(modules, chat.NewHandler(chatService, authentication, realtime))
		}
	}
	if *assistantEnabled {
		config, err := modelFlags.Resolve(*tokens)
		if err != nil {
			return err
		}
		provider, err := config.New(*timeout)
		if err != nil {
			return err
		}
		store, err := assistant.OpenStore(paths[0], paths[1])
		if err != nil {
			return err
		}
		workflow, err := assistant.NewWorkflow(store, provider, config.Model, config.ContextTokens, paths[2])
		if err != nil {
			return err
		}
		workflow.EnableReviewCache = *reviewCache
		modules = append(modules, assistant.NewServer(workflow))
		fmt.Printf("Model: %s (%s), profile: %s\n", config.Model, config.Provider, config.Profile)
	}
	listener, err := net.Listen("tcp", *addr)
	if err != nil {
		return err
	}
	server := &http.Server{Handler: httpapi.NewRouter(modules...), ReadHeaderTimeout: 5 * time.Second, IdleTimeout: 60 * time.Second, BaseContext: func(net.Listener) context.Context { return ctx }}
	fmt.Printf("HTTP API: http://%s\n", listener.Addr())
	if *assistantEnabled {
		fmt.Printf("Assistant: http://%s/page/assistant/\n", listener.Addr())
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
	return nil
}
