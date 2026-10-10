package decisions

import (
	"context"
	"fmt"
	"time"

	"github.com/LizHu95/Jetaime/services/api/internal/telemetry"
	"go.opentelemetry.io/otel/attribute"
)

// FactChecker 根据可信事实评估完整合法候选池，判断硬约束是否满足。
// context.Context 用于取消和超时；本包的 Context 承载业务数据，两者含义不同。
type FactChecker interface {
	Evaluate(context.Context, Context) (SelectEvaluation, error)
}

// Provider 从满足硬约束的候选中生成推荐和解释；CLI 可注入 Mock 或 Ollama。
// 服务端负责分配 OptionID 和校验结果，Provider 不能自行扩大候选或记忆范围。
type Provider interface {
	Generate(context.Context, Context) (Result, error)
}

// ServiceConfig 是服务的运行配置，不是用户这次决策的条件。
type ServiceConfig struct {
	SessionTTL  time.Duration    // 会话有效期；未配置时默认一小时。
	Now         func() time.Time // 获取当前时间；未配置时用 time.Now，测试可传固定时钟。
	DataVersion string           // 本地资料内容的 SHA-256，追踪使用，不参与业务判断。
}

// DecisionService 保存执行流程所需的工具；具体需求和结果由方法参数、返回值承载。
type DecisionService struct {
	store       *MemoryStore     // 读取资料、会话和历史，并保存处理结果。
	checker     FactChecker      // 检查预算、禁忌等硬约束的接口。
	provider    Provider         // 生成推荐的接口；创建服务时注入具体实现。
	ttl         time.Duration    // 创建新 Session 时使用的有效期。
	now         func() time.Time // 调用 s.now() 得到当前时间，便于验证过期行为。
	dataVersion string           // 每次 reload 创建新服务时更新，使各轮资料版本可追溯。
}

// NewDecisionService 组装存储、事实检查与生成能力，并补齐默认配置。
func NewDecisionService(store *MemoryStore, checker FactChecker, provider Provider, config ServiceConfig) (*DecisionService, error) {
	if store == nil || checker == nil || provider == nil {
		return nil, fmt.Errorf("store, checker and provider are required")
	}
	if config.SessionTTL < 0 {
		return nil, fmt.Errorf("session TTL must be positive")
	}
	if config.SessionTTL == 0 {
		config.SessionTTL = time.Hour
	}
	if config.Now == nil {
		config.Now = time.Now
	}
	return &DecisionService{store: store, checker: checker, provider: provider, ttl: config.SessionTTL, now: config.Now, dataVersion: config.DataVersion}, nil
}

// Generate 是新需求的入口，actorID 必须来自可信身份（CLI 暂用虚构身份）。
// 新需求不带 SessionID；沿用旧会话的换批通过 ChangeBatch 追踪，避免绕过重试规则。
func (s *DecisionService) Generate(ctx context.Context, request Request, actorID string) (Decision, error) {
	if actorID == "" || request.RequesterID != actorID {
		return Decision{}, ErrForbidden
	}
	if request.SessionID != "" {
		return Decision{}, fmt.Errorf("use ChangeBatch to continue an existing session")
	}
	return s.generate(ctx, request, actorID, "")
}

// generate 是新需求和换批共用的推荐主流程，按下面六步阅读即可。
// commandID 新需求时为空，换批时用于关联可重试的生成命令。
func (s *DecisionService) generate(ctx context.Context, request Request, actorID, commandID string) (generated Decision, generationErr error) {
	ctx, root := telemetry.Start(ctx, "decision.generate", "CHAIN", request)
	root.SetAttributes(attribute.String("user.id", actorID), attribute.String("fixture.sha256", s.dataVersion))
	defer func() {
		if generationErr == nil {
			telemetry.Output(root, generated)
			root.SetAttributes(attribute.String("session.id", generated.SessionID), attribute.String("decision.id", generated.ID))
		}
		telemetry.Finish(root, generationErr)
	}()
	if err := ctx.Err(); err != nil {
		return Decision{}, err
	}
	if err := request.ValidateSelect(); err != nil {
		return Decision{}, err
	}

	// 1. 准备会话：读取当前资料，新建 Session 或验证已有 Session。
	var data Dataset
	var revision uint64
	session, err := telemetry.Step(ctx, "prepare_session", request, func(context.Context) (Session, error) {
		var existing *Session
		var err error
		data, existing, revision, err = s.store.snapshot(request.SessionID)
		if err != nil {
			return Session{}, err
		}
		return s.prepareSession(request, existing)
	})
	if err != nil {
		return Decision{}, err
	}

	// 2. 组装上下文：获取有权使用的候选笔记和参与人记忆。
	// 不记录整个 Dataset；成功输出的 Context 已经过授权过滤。
	input, err := telemetry.Step(ctx, "build_context", request, func(context.Context) (Context, error) {
		return buildContext(data, request, actorID, session)
	})
	if err != nil {
		return Decision{}, err
	}

	// 3. 检查硬约束：评估完整候选池，得到满足、违反或未知。
	evaluation, err := telemetry.Step(ctx, "evaluate_candidates", input, func(ctx context.Context) (SelectEvaluation, error) {
		return s.evaluateCandidates(ctx, input)
	})
	if err != nil {
		return Decision{}, err
	}

	// 4. 生成结果：仅从满足项中选择，并校验返回内容。
	result, err := telemetry.Step(ctx, "generate_result", input, func(ctx context.Context) (Result, error) {
		return s.generateResult(ctx, input, evaluation, session)
	})
	if err != nil {
		return Decision{}, err
	}

	// 5. 记录结果：创建 Decision，更新 Session 的最新批次和已看笔记。
	if !s.now().Before(session.ExpiresAt) {
		return Decision{}, fmt.Errorf("session expired during generation")
	}
	decision, err := newDecision(request, input, session, result, s.now())
	if err != nil {
		return Decision{}, err
	}
	session.LatestDecisionID = decision.ID
	for _, option := range result.Options {
		session.rememberRecommendation(option.Selection.NoteID)
	}

	// 6. 提交保存：取消请求或版本已变化时不提交晚到结果。
	if err := ctx.Err(); err != nil {
		return Decision{}, err
	}
	if _, err := telemetry.Step(ctx, "commit", map[string]any{"decision": decision, "session": session, "revision": revision}, func(context.Context) (string, error) {
		return decision.ID, s.store.commitGenerated(decision, session, revision, commandID)
	}); err != nil {
		return Decision{}, err
	}
	return copyValue(decision)
}
