package app

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
	"sync"
	"syscall"
	"time"

	"github.com/LizHu95/Jetaime/services/api/internal/ai"
	"github.com/LizHu95/Jetaime/services/api/internal/ai/bootstrap"
	"github.com/LizHu95/Jetaime/services/api/internal/ai/jobs"
	"github.com/LizHu95/Jetaime/services/api/internal/auth"
	"github.com/LizHu95/Jetaime/services/api/internal/chat"
	"github.com/LizHu95/Jetaime/services/api/internal/collaboration"
	"github.com/LizHu95/Jetaime/services/api/internal/database"
	"github.com/LizHu95/Jetaime/services/api/internal/debugconsole"
	"github.com/LizHu95/Jetaime/services/api/internal/httpapi"
	"github.com/LizHu95/Jetaime/services/api/internal/llm"
	"github.com/LizHu95/Jetaime/services/api/internal/notification/realtime"
	"github.com/LizHu95/Jetaime/services/api/internal/notification/webpush"
	"github.com/LizHu95/Jetaime/services/api/internal/telemetry"
)

func Run(args []string) error {
	flags := flag.NewFlagSet("api", flag.ContinueOnError)
	addr := flags.String("addr", "127.0.0.1:8081", "listen address; assistant requires a loopback IP")
	databaseEnabled := flags.Bool("database", false, "enable PostgreSQL using JETAIME_DATABASE_URL; requires current schema")
	authEnabled := flags.Bool("auth", true, "enable account APIs by default; set -auth=false to disable; requires PostgreSQL using JETAIME_DATABASE_URL")
	migrateOnly := flags.Bool("migrate", false, "apply embedded PostgreSQL migrations and exit")
	chatAI := flags.Bool("chat-ai", true, "enable shared group AI worker by default with authentication; set -chat-ai=false to disable; requires model configuration")
	assistantEnabled := flags.Bool("assistant", false, "enable local assistant console")
	fixture := flags.String("fixture", "../../local/data/chat-history/history.fixture.json", "editable history fixture")
	state := flags.String("state", "../../local/data/assistant/state.json", "persisted messages and tasks")
	logs := flags.String("log", "../../local/data/assistant/runs.jsonl", "workflow trace log")
	modelFlags := llm.RegisterFlags(flags)

	timeout := flags.Duration("model-timeout", 5*time.Minute, "model timeout")
	tokens := flags.Int("context-tokens", 98304, "context capacity")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
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
	flags.Visit(func(f *flag.Flag) {
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
	shutdownTrace, err := telemetry.Init(ctx)
	if err != nil {
		return err
	}
	defer func() {
		flush, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := shutdownTrace(flush); err != nil {
			log.Print("trace shutdown failed")
		}
	}()
	var workers sync.WaitGroup

	if *databaseEnabled || *migrateOnly || *authEnabled {
		startup, cancel := context.WithTimeout(ctx, 30*time.Second)
		defer cancel()
		pool, err := database.Open(startup, os.Getenv("JETAIME_DATABASE_URL"))
		if err != nil {
			return err
		}
		defer pool.Close()
		defer func() {
			stop()
			done := make(chan struct{})
			go func() { workers.Wait(); close(done) }()
			select {
			case <-done:
			case <-time.After(12 * time.Second):
				log.Print("AI workers shutdown timed out")
			}
		}()
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
			workers.Add(1)
			go func() { defer workers.Done(); webpush.NewWorker(pool, pushConfig).Work(ctx) }()
			modules = append(modules, authentication, collaboration.NewHandler(collaboration.NewService(collaboration.NewPostgresStore(pool)), authentication))
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
			chatService := chat.NewService(chatStore, model != nil)
			var executor ai.Executor
			if model != nil {
				executor, err = bootstrap.New(model, modelName, capacity)
				if err != nil {
					return err
				}
			}
			worker := jobs.New(chatStore, executor, capacity, *timeout)
			realtime := realtime.New(ctx, chatStore, service)
			workers.Add(1)
			go func() { defer workers.Done(); realtime.Listen(ctx) }()
			for i := 0; i < 2; i++ {
				workers.Add(1)
				go func() { defer workers.Done(); worker.Work(ctx) }()
			}
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
		store, err := debugconsole.OpenStore(paths[0], paths[1])
		if err != nil {
			return err
		}
		executor, err := bootstrap.New(provider, config.Model, config.ContextTokens)
		if err != nil {
			return err
		}
		workflow, err := debugconsole.NewWorkflow(store, executor, config.Model, paths[2])
		if err != nil {
			return err
		}
		modules = append(modules, debugconsole.NewServer(workflow))
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
