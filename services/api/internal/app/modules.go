package app

import (
	"context"
	"fmt"
	"log"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/LizHu95/Jetaime/services/api/internal/ai"
	"github.com/LizHu95/Jetaime/services/api/internal/ai/execution"
	"github.com/LizHu95/Jetaime/services/api/internal/ai/intervention"
	"github.com/LizHu95/Jetaime/services/api/internal/ai/jobs"
	"github.com/LizHu95/Jetaime/services/api/internal/ai/llm"
	"github.com/LizHu95/Jetaime/services/api/internal/ai/mcpclient"
	"github.com/LizHu95/Jetaime/services/api/internal/ai/memory"
	"github.com/LizHu95/Jetaime/services/api/internal/ai/tools"
	"github.com/LizHu95/Jetaime/services/api/internal/ai/tools/amap"
	"github.com/LizHu95/Jetaime/services/api/internal/ai/tools/history"
	"github.com/LizHu95/Jetaime/services/api/internal/ai/tools/qa"
	"github.com/LizHu95/Jetaime/services/api/internal/ai/tools/recent"
	remindertool "github.com/LizHu95/Jetaime/services/api/internal/ai/tools/reminder"
	"github.com/LizHu95/Jetaime/services/api/internal/ai/tools/respond"
	"github.com/LizHu95/Jetaime/services/api/internal/ai/tools/silence"
	"github.com/LizHu95/Jetaime/services/api/internal/auth"
	"github.com/LizHu95/Jetaime/services/api/internal/chat"
	"github.com/LizHu95/Jetaime/services/api/internal/collaboration"
	"github.com/LizHu95/Jetaime/services/api/internal/config"
	"github.com/LizHu95/Jetaime/services/api/internal/httpapi"
	"github.com/LizHu95/Jetaime/services/api/internal/location"
	"github.com/LizHu95/Jetaime/services/api/internal/notification/realtime"
	"github.com/LizHu95/Jetaime/services/api/internal/notification/webpush"
	"github.com/LizHu95/Jetaime/services/api/internal/reminder"
	"github.com/jackc/pgx/v5/pgxpool"
)

type modules struct {
	handlers        httpapi.Handlers
	database        *pgxpool.Pool
	memoryService   *memory.Service
	reminderService *reminder.Service
	chatStore       *chat.Store
	chatService     *chat.Service
	background      []func(context.Context)
	workers         sync.WaitGroup
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
	if cfg.EnableAccountAPI {
		m.chatStore = chat.NewStore(m.database)
		m.chatStore.SetLocationConverter(location.AmapConverter(cfg.GaodeAPIKey))
		m.chatService = chat.NewService(m.chatStore, cfg.EnableChatAI)
		m.background = append(m.background, m.chatStore.CleanLocations)
		m.reminderService = reminder.NewService(reminder.NewStore(m.database))
		memoryStore := memory.NewStore(m.database, cfg.Memory)
		memoryService, err := cfg.Memory.Build()
		if err != nil {
			return nil, err
		}
		memoryService.Store = memoryStore
		if err = memoryStore.Initialize(ctx); err != nil {
			return nil, err
		}
		m.background = append(m.background, memoryService.Work, memoryService.WorkEmbeddings)
		m.memoryService = memoryService
		fmt.Printf("Memory: extraction=%s, embedding=%s (%d dimensions)\n", cfg.Memory.Extraction.Model, cfg.Memory.Embedding.Model, cfg.Memory.Embedding.Dimension)
	}
	var model llm.ChatModel
	var executor *execution.Service
	if cfg.EnableChatAI {
		var modelErr error
		model, modelErr = cfg.Model.New(cfg.ModelTimeout)
		if modelErr != nil {
			return nil, modelErr
		}
		fmt.Printf("Model: %s (%s), profile: %s\n", cfg.Model.Model, cfg.Model.Provider, cfg.Model.Profile)
	}
	if cfg.EnableChatAI {
		if m.reminderService == nil {
			return nil, fmt.Errorf("chat AI requires account services")
		}
		var provider qa.ContextProvider
		var interventionEvidence ai.InterventionEvidenceProvider
		if m.memoryService != nil {
			provider = qa.HistoryContext(m.memoryService, m.chatService)
			interventionEvidence = intervention.HistoryEvidence(m.memoryService, m.chatService)
		}
		// Complete production chat capability list, including its domain dependencies.
		entries := []tools.Tool{
			respond.New(),
			silence.New(),
			qa.New(model, cfg.Model.Model, cfg.Model.ContextTokens, provider),
			recent.New(m.chatService),
			history.New(model, cfg.Model.Model, cfg.Model.ContextTokens, m.chatService),
			remindertool.New(m.reminderService, reminderOperation),
		}
		if key := strings.TrimSpace(cfg.GaodeAPIKey); key != "" {
			endpoint := "https://mcp.amap.com/mcp?" + url.Values{"key": {key}}.Encode()
			client := mcpclient.New(endpoint)
			guard := func(ctx context.Context, scope tools.Scope) error {
				return m.chatService.AuthorizeExternalTool(ctx, chatExecutionScope(scope))
			}
			entries = append(entries, amap.New(client, guard)...)
		}
		registry, e := tools.New(entries...)
		if e != nil {
			return nil, e
		}
		executor, err = execution.New(model, cfg.Model.Model, cfg.Model.ContextTokens, registry, interventionEvidence)
		if err != nil {
			return nil, err
		}
	}
	if cfg.EnableAccountAPI {
		if err = m.addAccountModules(ctx, cfg, executor); err != nil {
			return nil, err
		}
	}

	return m, nil
}

func (m *modules) addAccountModules(ctx context.Context, cfg config.Config, executor *execution.Service) error {
	accounts, err := auth.NewService(auth.NewPostgresStore(m.database))
	if err != nil {
		return fmt.Errorf("initialize authentication: %w", err)
	}
	authentication := auth.NewHandler(accounts)
	pushConfig, err := webpush.FromEnv()
	if err != nil {
		return err
	}

	chatStore, chatService := m.chatStore, m.chatService
	events := realtime.New(ctx, chatStore, accounts)
	m.handlers.Auth = authentication
	m.handlers.Collaboration = collaboration.NewHandler(collaboration.NewService(collaboration.NewPostgresStore(m.database)), authentication)
	m.handlers.Chat = chat.NewHandler(chatService, authentication, events)
	reminders := m.reminderService
	m.handlers.Reminders = reminder.NewHandler(reminders, authentication)
	m.background = append(m.background, reminder.NewWorker(reminders).Work)
	m.handlers.Push = webpush.NewHandler(m.database, authentication, pushConfig)

	m.background = append(m.background, events.Listen)
	if pushConfig.Enabled {
		m.background = append(m.background, webpush.NewWorker(m.database, pushConfig).Work)
	}
	if cfg.EnableChatAI {
		inject := remindertool.New(reminders, reminderOperation).Inject
		worker := jobs.New(chatStore, executor, cfg.Model.ContextTokens, cfg.ModelTimeout, inject)
		m.background = append(m.background, worker.Work, worker.Work)
	}
	return nil
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
