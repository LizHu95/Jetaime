package app

import (
	"context"
	"fmt"
	"log"
	"sync"
	"time"

	"github.com/LizHu95/Jetaime/services/api/internal/ai"
	"github.com/LizHu95/Jetaime/services/api/internal/ai/bootstrap"
	"github.com/LizHu95/Jetaime/services/api/internal/ai/jobs"
	"github.com/LizHu95/Jetaime/services/api/internal/auth"
	"github.com/LizHu95/Jetaime/services/api/internal/chat"
	"github.com/LizHu95/Jetaime/services/api/internal/collaboration"
	"github.com/LizHu95/Jetaime/services/api/internal/config"
	"github.com/LizHu95/Jetaime/services/api/internal/debugconsole"
	"github.com/LizHu95/Jetaime/services/api/internal/httpapi"
	"github.com/LizHu95/Jetaime/services/api/internal/memory"
	"github.com/LizHu95/Jetaime/services/api/internal/notification/realtime"
	"github.com/LizHu95/Jetaime/services/api/internal/notification/webpush"
	"github.com/jackc/pgx/v5/pgxpool"
)

type modules struct {
	handlers   httpapi.Handlers
	database   *pgxpool.Pool
	background []func(context.Context)
	workers    sync.WaitGroup
}

// Build every dependency before starting background work. On failure, release
// resources here; on success, Run owns shutdown.
func buildModules(ctx context.Context, cfg config.Config) (_ *modules, err error) {
	m := &modules{}
	defer func() {
		if err != nil && m.database != nil {
			m.database.Close()
		}
	}()
	if cfg.EnableAccountAPI || cfg.EnableDatabase {
		m.database, err = openDatabase(ctx, cfg.DatabaseURL)
		if err != nil {
			return nil, err
		}
	}
	var executor *ai.Service
	if cfg.EnableChatAI || cfg.DebugConsole.Enabled {
		model, modelErr := cfg.Model.New(cfg.ModelTimeout)
		if modelErr != nil {
			return nil, modelErr
		}
		executor, err = bootstrap.New(model, cfg.Model.Model, cfg.Model.ContextTokens)
		if err != nil {
			return nil, err
		}
		fmt.Printf("Model: %s (%s), profile: %s\n", cfg.Model.Model, cfg.Model.Provider, cfg.Model.Profile)
	}
	if cfg.EnableAccountAPI {
		if err = m.addAccountModules(ctx, cfg, executor); err != nil {
			return nil, err
		}
	}
	if cfg.DebugConsole.Enabled {
		m.handlers.Assistant, err = buildDebugConsole(cfg, executor)
		if err != nil {
			return nil, err
		}
	}
	return m, nil
}

func (m *modules) addAccountModules(ctx context.Context, cfg config.Config, executor *ai.Service) error {
	accounts, err := auth.NewService(auth.NewPostgresStore(m.database))
	if err != nil {
		return fmt.Errorf("initialize authentication: %w", err)
	}
	authentication := auth.NewHandler(accounts)
	pushConfig, err := webpush.FromEnv()
	if err != nil {
		return err
	}

	chatStore := chat.NewStore(m.database)
	chatService := chat.NewService(chatStore, cfg.EnableChatAI)
	events := realtime.New(ctx, chatStore, accounts)
	m.handlers.Auth = authentication.Endpoints()
	m.handlers.Collaboration = collaboration.NewHandler(collaboration.NewService(collaboration.NewPostgresStore(m.database)), authentication).Endpoints()
	m.handlers.Chat = chat.NewHandler(chatService, authentication, events).Endpoints()
	m.handlers.Push = webpush.NewHandler(m.database, authentication, pushConfig).Endpoints()
	memoryStore := memory.NewStore(m.database, cfg.Memory)
	memoryService, err := cfg.Memory.Build()
	if err != nil {
		return err
	}
	memoryService.Store = memoryStore
	if err = memoryStore.Initialize(ctx); err != nil {
		return err
	}
	m.background = append(m.background, memoryService.Work, memoryService.WorkEmbeddings)
	fmt.Printf("Memory: extraction=%s, embedding=%s (%d dimensions)\n", cfg.Memory.Extraction.Model, cfg.Memory.Embedding.Model, cfg.Memory.Embedding.Dimension)

	m.background = append(m.background, events.Listen)
	if pushConfig.Enabled {
		m.background = append(m.background, webpush.NewWorker(m.database, pushConfig).Work)
	}
	if cfg.EnableChatAI {
		worker := jobs.New(chatStore, executor, cfg.Model.ContextTokens, cfg.ModelTimeout)
		m.background = append(m.background, worker.Work, worker.Work)
	}
	return nil
}

func buildDebugConsole(cfg config.Config, executor ai.Executor) (*httpapi.AssistantHandlers, error) {
	console := cfg.DebugConsole
	store, err := debugconsole.OpenStore(console.HistoryFile, console.StateFile)
	if err != nil {
		return nil, err
	}
	workflow, err := debugconsole.NewWorkflow(store, executor, cfg.Model.Model, console.RunLogFile)
	if err != nil {
		return nil, err
	}
	return debugconsole.NewServer(workflow).Endpoints(), nil
}

func (m *modules) startWorkers(ctx context.Context) {
	for _, work := range m.background {
		m.workers.Add(1)
		go func(work func(context.Context)) { defer m.workers.Done(); work(ctx) }(work)
	}
}

func (m *modules) close() {
	done := make(chan struct{})
	go func() { m.workers.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(12 * time.Second):
		log.Print("background workers shutdown timed out")
	}
	if m.database != nil {
		m.database.Close()
	}
}
