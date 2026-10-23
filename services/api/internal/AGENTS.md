# internal 目录与依赖约束

- `internal` 的一级包按领域或明确的基础设施职责拆分。业务能力放在 `internal/<领域>`，例如 `auth`、`chat`、`collaboration`、`reminder`；不要按 AI 调用方重复实现业务。
- 业务域拥有其业务类型、Service、权限规则、事务、持久化及业务 Worker。同一能力的 HTTP、AI 和其他调用方复用该领域 Service。业务域不得依赖 `ai/tools` 或 Agent 实现。
- AI 工具封装统一放在 `internal/ai/tools/<能力>`。该层只处理模型工具定义、参数/上下文转换、模型输出校验和结果表达；涉及提醒等业务操作时调用 `internal/<领域>` 的 Service，禁止在工具层直接执行业务 SQL、编写业务事务或维护第二份业务状态机。
- `internal/ai` 负责 Agent 协议与编排、模型适配、上下文、工具及 AI 执行基础设施。`ai/llm` 放模型客户端；`ai/memory` 放 AI 专属的记忆提取、检索、向量与索引持久化，不在根目录重新建立 `llm` 或 `memory` 包。
- `ai/jobs` 通过持久化接口执行 AI 队列；聊天消息、AI 任务认领/提交事务等业务存储实现属于 `chat`。`app` 负责组装跨域依赖与后台生命周期。
- 所有 HTTP 方法与路径在 `httpapi/router.go` 集中注册，直接绑定 Handler 的具名方法，例如 `ListMessages`、`SendMessage`。不引入 `Endpoints()` 或由 `http.Handler` 字段组成的转接表。
- `httpapi` 通过标准 `net/http` 方法接口描述模块，不依赖具体业务包。模块装配直接注入 Handler 实例；未启用模块使用 nil 接口，避免注入带类型的 nil 指针。
- 认证与本机访问限制在路由表中显式包装。登录身份只由认证中间件写入私有 Context key，不信任请求体中的用户身份。WebSocket 保留实时传输层自己的握手鉴权。
- Handler 处理 HTTP 参数、状态码与响应，业务入口经过 Service，不直接访问 `service.store`。领域错误映射留在对应 Handler；账号和业务 API 使用 `httpapi.WriteJSON` 输出 JSON，保留既有错误码和请求大小限制。
- 移动包时同步更新 import、测试、Makefile、脚本和现行文档。不得重写 `local/`、`outputs/` 中的历史快照或私有数据。重构保持既有 URL、请求/响应字段与权限行为。

- `ai/execution` 是 main 的统一装配与执行入口，负责执行身份、Trace 和主动/被动入口编排；不再单独设立 bootstrap、runtime 转接包。`ai` 根包只维护协议和公共数据类型。
- 聊天原文的近期读取和来源/相邻消息回查由 `chat.Service` 提供，`chat/history` 保存消费者可依赖的原文读取契约；`ai/memory` 维护提取、索引、召回及其事务进度。业务授权只接收 `chat.ExecutionScope`，由 app 适配 AI 工具协议。
- `ai/context` 只依赖公共 AI 协议与预算基础设施，不依赖具体 Agent 提示词；完整提示词和工具 Schema 的预算由执行 Agent 在每次模型调用前检查。
