package app

import (
	"context"
	"fmt"
	"log"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/LizHu95/Jetaime/services/api/internal/ai/execution"
	"github.com/LizHu95/Jetaime/services/api/internal/ai/intervention"
	"github.com/LizHu95/Jetaime/services/api/internal/ai/jobs"
	"github.com/LizHu95/Jetaime/services/api/internal/ai/llm"
	"github.com/LizHu95/Jetaime/services/api/internal/ai/mcpclient"
	"github.com/LizHu95/Jetaime/services/api/internal/ai/memory"
	"github.com/LizHu95/Jetaime/services/api/internal/ai/tools"
	"github.com/LizHu95/Jetaime/services/api/internal/ai/tools/amap"
	"github.com/LizHu95/Jetaime/services/api/internal/ai/tools/catalog"
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

// buildModules 装配数据库、领域服务、AI 执行入口和 HTTP Handler，并登记后台任务。
// 此处只完成初始化，不启动后台循环；全部装配成功后，由 startWorkers 统一启动。
// 初始化失败时关闭已打开的数据库连接池；成功后由 Run 管理关闭流程。
func buildModules(ctx context.Context, cfg config.Config) (_ *modules, err error) {
	m := &modules{}
	defer func() {
		if err != nil && m.database != nil {
			m.database.Close()
		}
	}()
	// 领域服务共享数据库连接池，HTTP Handler 和 AI 工具复用同一套业务能力。
	m.database, err = openDatabase(ctx, cfg.DatabaseURL)
	if err != nil {
		return nil, err
	}
	m.chatStore = chat.NewStore(m.database)
	m.chatStore.SetLocationConverter(location.AmapConverter(cfg.GaodeAPIKey))
	m.chatService = chat.NewService(m.chatStore, cfg.EnableChatAI)
	m.background = append(m.background, m.chatStore.CleanLocations)
	m.reminderService = reminder.NewService(reminder.NewStore(m.database))

	// 初始化记忆存储与模型配置，登记记忆提取和向量化任务。
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

	// 聊天 AI 开启时构建模型和统一执行入口，供主动、被动回复共用。
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
		provider := qa.HistoryContext(m.memoryService, m.chatService)
		interventionEvidence := intervention.NewHistoryEvidenceProvider(m.memoryService, m.chatService)
		// 显式列出生产环境的 AI 工具，并注入其依赖的领域服务。
		entries := []tools.Tool{
			respond.New(),
			silence.New(),
			qa.New(model, cfg.Model.Model, cfg.Model.ContextTokens, provider),
			recent.New(m.chatService),
			history.New(model, cfg.Model.Model, cfg.Model.ContextTokens, m.chatService),
			remindertool.New(m.reminderService, reminderOperation),
		}
		if key := strings.TrimSpace(cfg.GaodeAPIKey); key != "" {
			// 高德工具按密钥配置启用，调用前通过聊天领域服务校验权限。
			endpoint := "https://mcp.amap.com/mcp?" + url.Values{"key": {key}}.Encode()
			client := mcpclient.New(endpoint)
			guard := func(ctx context.Context, scope tools.Scope) error {
				return m.chatService.AuthorizeExternalTool(ctx, chatExecutionScope(scope))
			}
			entries = append(entries, amap.New(client, guard, location.AmapRegionResolver(key))...)
		}
		registry, e := catalog.New(entries...)
		if e != nil {
			return nil, e
		}
		executor, err = execution.New(model, cfg.Model.Model, cfg.Model.ContextTokens, registry, interventionEvidence)
		if err != nil {
			return nil, err
		}
	}

	// 共享认证服务装配各业务 Handler；实时通知同时依赖聊天权限与账号认证。
	accounts, err := auth.NewService(auth.NewPostgresStore(m.database))
	if err != nil {
		return nil, fmt.Errorf("initialize authentication: %w", err)
	}
	authentication := auth.NewHandler(accounts)
	pushConfig, err := webpush.FromEnv()
	if err != nil {
		return nil, err
	}

	chatStore, chatService := m.chatStore, m.chatService
	events := realtime.New(ctx, chatStore, accounts)
	m.handlers.Auth = authentication
	m.handlers.Collaboration = collaboration.NewHandler(collaboration.NewService(collaboration.NewPostgresStore(m.database)), authentication)
	m.handlers.Chat = chat.NewHandler(chatService, authentication, events)
	reminders := m.reminderService
	m.handlers.Reminders = reminder.NewHandler(reminders, authentication)
	m.handlers.Push = webpush.NewHandler(m.database, authentication, pushConfig)

	// 登记提醒调度、数据库事件监听及按配置启用的 Web Push 任务。
	// 与前面登记的清理、记忆任务一样，这里不会立即启动循环。
	m.background = append(m.background, reminder.NewWorker(reminders).Work, events.Listen)
	if pushConfig.Enabled {
		m.background = append(m.background, webpush.NewWorker(m.database, pushConfig).Work)
	}
	if cfg.EnableChatAI {
		inject := remindertool.New(reminders, reminderOperation).Inject
		worker := jobs.New(chatStore, executor, cfg.Model.ContextTokens, cfg.ModelTimeout, inject)
		// 同一 Worker 启动两个消费循环，允许不同群并发处理；Claim 保证同群串行。
		m.background = append(m.background, worker.Work, worker.Work)
	}
	return m, nil
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
