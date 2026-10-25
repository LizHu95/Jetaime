# 日常协同 AI 助理

## Web 前端

响应式 Web 客户端位于 `apps/web/`，使用 React、TypeScript、Vite、shadcn/ui 和 TanStack Query。已提供登录、注册，以及登录后的聊天和我两个底部 Tab，支持群聊、加好友、修改密码和退出登录，适配手机与 PC，并接入现有 API。

首次运行 `cd apps/web && npm ci`，随后在仓库根目录运行 `make web`，打开 <http://127.0.0.1:5173/page/auth/login/>。真实认证需另行启动 `make dev` 并完成 PostgreSQL 配置与迁移。构建使用 `make web-build`，产物位于 `outputs/web/`；完整说明见 [Web 开发说明](apps/web/README.md)。

## 后端与主 Agent

正式服务提供账号、好友、双人群聊、实时同步、共享 AI、记忆检索和提醒。Web 客户端通过认证 API 使用这些能力。

旧 JSON 调试台与离线 Workflow 评测已于 2026-10-23 退役。历史案例、结果与源码快照保留用于追溯，见 [历史评测说明](evals/README.md)。一期方案与结果保留在 `docs/v1/`。

## 本地运行

统一入口会自动加载本地环境与默认模型配置。在仓库根目录启动：

```sh
make dev          # 认证、好友和群聊 API，需要 PostgreSQL
make model-check  # 只校验模型配置与密钥存在性，不调用模型
make feishu       # 飞书长连接
```

首次使用时，将 `services/api/config/models.example.json` 复制到 `local/models.json`，编辑模型参数与顶层 `default_profile`。实际密钥保存在 `local/.env`；启动入口自动加载该文件。飞书额外读取 `local/feishu/.env`。已有 shell 环境变量优先于文件，同名文件配置中飞书专用值优先于共享值。

```sh
# 临时切换，省略默认配置文件路径：
make dev API_ARGS='-model-profile local-qwen'
# 指定其他配置文件或服务端口：
make dev API_ARGS='-model-config ../../local/other-models.json -addr 127.0.0.1:8082'
```

也可使用 `python3 scripts/dev.py api`、`python3 scripts/dev.py model-check` 等同一入口。模型参数放在 JSON 中，启动命令不需要手动 source 或逐项指定模型参数。环境文件使用单行 `KEY=value` 或带引号的值，不执行命令替换或变量展开。直接 `go run` 不加载环境文件。

支持 Eino Ollama / OpenAI 兼容接口；启动时加载主模型和顶层 `fallback_profiles` 候选列表，调用失败时自动切换，成功后优先沿用该候选。修改候选配置需重启；云模型与 profile 配置见 [模型接入说明](docs/v2/运行说明.md#eino-模型接入与启动选择)。

`make model-check MODEL_CHECK_ARGS='-model-profile local-qwen'` 可检查指定模型配置；该命令校验配置和密钥存在性，不发送推理请求。启动正式服务还需配置 PostgreSQL 与记忆模型，见 [运行说明](docs/v2/运行说明.md)。

## HTTP 路由与模块装配

`cmd/api/main.go` 读取命令行参数并监听退出信号 → `config.Parse` 生成配置 → `app.Run` 初始化模块并运行服务。`-auth` 控制账号、好友、群聊和通知 API；`-chat-ai` 控制正式群聊中的主 Agent。

`make dev` 和 API 二进制默认启用认证、好友、群聊及实时同步 API、共享 AI 与 `/test/healthz`，需要 PostgreSQL 连接、已迁移的数据库及有效模型配置；无需历史文件。`make dev` 自动加载 `local/.env`，部署二进制从运行环境读取配置。仅需健康检查时显式传 `-auth=false`。

使用 Go 标准库 `net/http` 与 `ServeMux`。所有 HTTP 方法、路径和处理器绑定集中在 [internal/httpapi/router.go](services/api/internal/httpapi/router.go) 的 `NewRouter(Handlers)`，打开此文件即可查看完整路由表。启动流程在 [internal/app/server.go](services/api/internal/app/server.go)，模块装配在 [internal/app/modules.go](services/api/internal/app/modules.go)，按配置注入启用的端点组；未注入的组不注册路由。路由直接绑定业务 Handler 的具名方法（如 `ListMessages`、`SendMessage`），并显式包装 `Authenticate` 中间件。模块装配注入 Handler 实例，不再通过 `Endpoints()` 转接字段。认证身份由私有 Context key 传递，参数解析留在 Handler，业务入口统一经过 Service；账号和业务 API 共用 `httpapi.WriteJSON` 输出响应。

HTTP 接口以实际客户端调用为边界；记忆检索、Worker 调度、模型调用和 Agent 编排通过进程内方法协作。当前路由用途如下：

| 路由组 | 实际调用方 |
| --- | --- |
| `/api/auth/register`、`login`、`logout`、`password` | 登录、注册、退出与修改密码页面；用户信息随登录结果返回 |
| `/api/contacts/*`、`/api/groups` | 好友管理与群聊列表 |
| `/api/chat/*` | 聊天、阅读进度、来源定位、AI 任务状态/重试与 WebSocket 同步 |
| `/api/push/*` | 通知设置页面，包括用户主动测试通知的按钮 |
| `/test/healthz` | 部署连通性检查 |

原 `GET /api/auth/me` 没有当前调用方，已移除；共享的 `Authenticate` 会话校验继续服务各业务接口。

PostgreSQL 基础接入已完成：`JETAIME_DATABASE_URL` 配合 `make db-migrate` 初始化空库，默认认证模块开启连接及版本检查。SQL 随二进制嵌入，迁移有事务、并发锁、版本与校验和。`JETAIME_TEST_DATABASE_URL` 配合 `make db-test` 运行真实 PostgreSQL 集成测试，测试创建并清理独立 schema。数据库接入与启动方式见 [部署说明](deploy/api/README.md)，表结构见 [PostgreSQL 数据设计](docs/v2/PostgreSQL数据设计.md)。注册、登录、退出接口默认开启，见 [认证接口说明](docs/v2/认证接口.md)；双人群聊、实时同步、阅读进度和共享 AI 接口已实现，见 [双人群聊接口](docs/v2/双人群聊接口.md)。

历史向量化与增量记忆已实现独立 Worker、持久游标/检查点、pgvector 和受群成员权限保护的内部检索方法。正式群聊首次模型调用只携带有预算限制的近期消息；依赖历史且证据不足时，主 Agent 调用 `answer_question` 混合检索并回查原文，所有取数遵守原提问截止序号。正式 API 启动时自动运行记忆 Worker，无需 enabled 开关，需有效的记忆模型配置与密钥；首次部署需安装 pgvector 并执行迁移，重启复用已完成数据。配置、恢复保证和接口见 [历史向量化与增量记忆](docs/v2/历史向量化与增量记忆.md)。

- `POST /api/auth/register`、`/api/auth/login`、`/api/auth/logout`：账号认证模块，`make dev` 默认启用。
- `GET /test/healthz`：固定返回测试 JSON，不代表模型或数据库就绪。

旧的无业务域 `/api/...` 路径已替换，前端与验证脚本同步更新。根路径不注册，返回 404；使用完整业务页面地址。飞书继续通过 SDK 长连接接收事件，目前没有需要注册的飞书 HTTP 接口。`GET /test/healthz` 用于测试连通性，固定返回 `{"status":"ok","message":"hello"}`。

```sh
make dev                                  # 默认启用认证、好友、群聊、实时同步与健康检查
make dev API_ARGS='-auth=false'            # 只启用健康检查，不连接数据库
```

统一 HTTP API 打包运行 `make api-build`，产物保存到 `outputs/api/`，包含 Linux amd64 / arm64 二进制和 `api-deploy.tar.gz`。详见 [API 打包说明](deploy/api/README.md)。

编译 API 和 Web、上传到云服务器并自动迁移和重启 API（本机需能通过 SSH 登录，首次构建 Web 前运行 `cd apps/web && npm ci`）：

```sh
# 首次配置：复制模板，在 local/deploy.mk 中填写 DEPLOY_SSH ?= user@host
cp deploy/deploy.mk.example local/deploy.mk
make deploy
# 临时覆盖 SSH 目标（也可通过环境变量 DEPLOY_SSH 注入）：
make deploy DEPLOY_SSH=user@host
# 自定义 API 上传目录和 Web 静态文件目录（同一服务器）：
make deploy DEPLOY_DEST='user@host:/srv/jetaime/' DEPLOY_WEB_DIR='/var/www/jetaime'
```

`make deploy` 自动读取 `local/deploy.mk`；该文件被 Git 忽略，地址无需写进受版本管理的文件。配置使用 Make 语法，值不加引号；环境变量和命令行赋值可覆盖模板中的 `?=` 默认值。未配置时仍使用 SSH 别名 `jetaime-server`。可用 `DEPLOY_CONFIG` 指定其他配置文件。

API 打包会将 `local/.env` 复制为 `outputs/api/.env` 并收入压缩包，上传会同步覆盖服务器的配置。`make deploy` 上传和 Nginx 配置成功后，自动 SSH 到同一服务器，在 API 上传父目录执行 `bash api/start.sh restart`，应用待执行的数据库迁移并重启 API；已应用的迁移会跳过。迁移失败时退出并保留旧 API 进程。脚本自动加载同目录 `.env`。

脚本在两项构建都成功后，将 API 压缩包上传到远端临时目录，解压并校验 SHA-256 后通过文件重命名更新 `~/api/`（避免直接覆盖运行中的二进制导致 Text file busy，保留服务器 runtime 数据），将 `outputs/web/` 的内容直接上传到 `/var/www/jetaime/`，例如 `/var/www/jetaime/index.html` 和 `/var/www/jetaime/assets/`，不再生成 `~/web/`，无需手动复制。Web 目录自动创建并设置读取权限，SSH 账号需有该目录的写入和权限修改权限。任何构建或上传失败都会立即退出；上传覆盖同名文件，保留远端其他文件。上传后自动应用并重新加载 Nginx，再迁移和重启 API；任一步骤失败都会让 `make deploy` 以非零状态退出。Phoenix 部署定义随 API 打包；部署最后的 `bash api/start.sh restart` 会自动部署同机 Phoenix、等待其自身迁移，再迁移业务库并重启 API。访问入口为 `/page/phoenix/`，认证信息见 [Phoenix 说明](deploy/phoenix/README.md)。

Nginx 配置源为 `deploy/nginx/default.conf`。部署时自动匹配 `DEPLOY_WEB_DIR` 并替换服务器 `/etc/nginx/sites-available/default`，确保默认站点已启用，执行 `nginx -t` 后重新加载 Nginx（未启动则启动）。原配置保存在 `/etc/nginx/default.backup.*`；检查或加载失败时恢复原配置并退出。服务器需安装 Nginx 和 systemd；应用配置使用 root 权限，非 root 账号需可通过 `sudo -n` 执行安装脚本，Web 上传仍需目录写入权限。完整配置见 [Web 部署约定](apps/web/README.md#部署约定)。

## 代码结构

`internal` 按领域拆分。业务规则、事务与定时任务属于对应业务域；AI 工具只在 `ai/tools/<能力>` 适配模型参数并调用业务 Service。AI 专属模型和记忆能力归入 `ai/llm`、`ai/memory`。具体约束见 [internal/AGENTS.md](services/api/internal/AGENTS.md)。

```text
services/api/
├── cmd/api/                   正式服务入口
├── cmd/model-check/          独立模型配置检查
├── internal/config/          命令行参数解析为明确的配置值
├── internal/app/             启动流程、模块装配与后台任务生命周期
├── internal/auth/            账号与会话
├── internal/collaboration/   好友申请、群组、成员与关闭事务
├── internal/chat/            消息、阅读进度与 PostgreSQL 适配
├── internal/reminder/        提醒领域的 Service、持久化、调度与 HTTP 接口
├── internal/ai/
│   ├── protocol.go           执行协议与持久任务数据类型
│   ├── execution/            main 装配、入口编排、执行身份与 Trace
│   ├── agent/main/           主 Agent 工具选择与执行循环
│   ├── context/              上下文选择、引用保留与预算
│   ├── skills/               五类沟通障碍的版本化策略、路由与按需加载
│   ├── tools/                工具注册表及 qa、extract、reminder 等 AI 适配
│   ├── llm/                  模型配置与 Eino 供应商适配
│   ├── memory/               AI 记忆提取、检索、向量与索引存储
│   ├── prompts/              主 Agent 与提取提示词、调用 Schema
│   └── jobs/                 持久队列执行、超时与结果提交
├── internal/notification/    实时通知与 Web Push
├── internal/database/        PostgreSQL 连接与嵌入式迁移
└── internal/telemetry/       调用链
```

当前只有一个 `main` Agent，装配入口为 `internal/ai/execution/service.go`。普通知识、建议、写作、澄清及当前上下文足够的问题由主 Agent 直接生成，通过只校验、不调用模型的 `respond` 终结响应返回，通常只需一次模型调用。正式群聊每次 AI 提问先用当前问题、直接引用和最多 6 条/4 KiB 近期背景判断需求；追问丢失所指事项时可通过 `read_recent_messages` 补读有权限与序号截止的近期真人原文；需要历史证据时调用 `answer_question` 检索后回答，时间回顾使用 `summarize_history`，提醒使用 `set_reminder` 调用领域 Service。工具目录只向模型提供名称、描述和输入 Schema，输出 Schema 保留在后端校验。调用方显式注册完整能力，execution 不添加默认工具。`extract_todos` 已移除默认注册，提取实现暂留；行动事项只作文字总结，不生成结构化待办建议。`qa/help` 旧模式仍兼容 QA。主提示词版本为 `main-v23-standard-skills`，QA 为 `answer-question-v2-hybrid`。

提示词按能力维护：被动干预的候选发现正文位于 [`intervention/recognition.md`](services/api/internal/ai/intervention/recognition.md)，补证后的最终判断位于 [`intervention/verification.md`](services/api/internal/ai/intervention/verification.md)，通过 `go:embed` 编译进程序，Schema、版本与结构修复提示位于 [`intervention/prompts.go`](services/api/internal/ai/intervention/prompts.go)；主 Agent 的提示词、Schema、版本和请求构造位于 [`agent/main/prompts/`](services/api/internal/ai/agent/main/prompts/prompts.go)（完整模型请求的预算在 Agent 内检查）；问答位于 [`tools/qa/prompts.go`](services/api/internal/ai/tools/qa/prompts.go)；待办提取、接受识别和行动复核统一位于 [`tools/extract/prompts.go`](services/api/internal/ai/tools/extract/prompts.go)；记忆提取与合并位于 [`memory/prompts.go`](services/api/internal/ai/memory/prompts.go)。各能力的提示词、模型输出 Schema 与版本标识集中维护，执行和校验逻辑保留在原模块文件中。

生产群聊调用 `execution.Service → entry → agent/main`。主动请求直接进入 main；被动消息由独立判断器发现 `skip / intervene / verify` 候选，verify 最多一轮只读补证后再判断。生产装配复用受群权限与消息序号截止约束的记忆/历史检索及原文回查。执行边界使用 Agent 协议，具体 Agent 不依赖聊天或数据库；群共享记忆通过 QA 的函数式上下文提供器接入。尚未实现跨 Agent 委派与调用树恢复。

五类沟通障碍使用 [Agent Skills 标准](https://agentskills.io/specification) 的 `skill-name/SKILL.md` 包，入口包含 YAML `name`、`description` 与 Markdown 正文。启动时 [`skills/communication.go`](services/api/internal/ai/skills/communication.go) 从 frontmatter 发现元数据与路由：`metadata.version` 和 `metadata.jetaime-breakdown` 是本项目约定，名称与父目录保持 kebab-case 一致。识别与冷却去重通过后，后端按主要障碍只选择一份，将 ID、版本与障碍类型保存在内部执行元数据中；主 Agent 按需从 `go:embed` 读取正文，在每轮系统上下文中保留、计入完整请求预算，不注入 YAML 头部。Skill 不扩张工具或写操作权限；明确操作请求和 @AI 路径不加载沟通 Skill。Trace 的 `ai.skill.execute` 及其模型、工具子节点记录 `skill.id/version/breakdown/selection_reason`，内容采集关闭时仍可查看元数据。修改策略须更新对应 `metadata.version` 并重新构建；不支持用户指定路径、任意 Skill 文本或运行时热加载。详见[沟通 Skill 设计](docs/v2/主Agent设计问答.md#q19五类情感沟通障碍如何主动帮助)。

主动建议实际发出后，群级冷却 2 分钟；规范化话题、障碍类型集合与帮助意图相同的建议在 24 小时内去重。记录持久化在 `agent_runs`，跨最近30条识别窗口与进程重启有效；主 Agent 执行前检查一次，保存回复时在群锁事务内再次检查。静默、失败、取消不会延长限制；@AI、既有请求补全与识别成立的明确操作请求不受该限制。话题只进行空白与大小写规范化，不提供语义改写去重或跨窗口拒绝记忆。部署需先应用 `000015_intervention_policy` 迁移；历史去重状态从新版本成功发送的建议开始累积。

原文读取统一由 `chat.Service` 提供，`chat/history` 只定义原文消息与读取契约；QA、干预核验和近期补读不再通过 memory Store 回查聊天。记忆提取及索引进度仍由 memory 的事务维护。`app/chat_tools.go` 将工具 Scope 转换为聊天领域的 `ExecutionScope`，聊天业务不依赖 `ai/tools`。初始上下文仅检查所选数据预算，各 Agent 在模型调用前检查包含提示词、Schema 与工具目录的完整请求。

`ai.Job` 表示数据库排队与租约状态，`ai.Run` 表示一次执行记录，`ai.Result` 是允许保存并展示的回答或建议。每次运行带独立的 `agentId/invocationId`，原始模型响应和内部步骤不会进入消息 API 的结果对象。`chat` 的上下文适配器执行群权限与触发序号检查，`ai/context` 负责原文选择与预算。

消息与 AI 入队、AI 回复与完成状态分别保持单事务提交；群关闭事务仍会禁用助手并取消未完成任务，过期 worker 的 attempt/租约保护保留。数据库结构与已有 HTTP 路径不变。WebSocket 和 Web Push 位于通知模块，通知丢失后仍以数据库补拉为准。

已移除默认关闭的核对缓存实验、模型指纹查询和预留指令提供器；提取中的证据边界与行动状态核对继续保留。私有输入在 `local/`，生成产物在 `outputs/`，部署定义在 `deploy/`。

目录职责、迁移与清理规则见 [目录约定](docs/目录约定.md)。`deploy/` 保存部署定义，`local/` 保存实际配置和持久数据，`outputs/` 保存生成产物。

## 开发与检查

Go 工具链版本由 `.go-version` 管理；Go 模块最低要求 Go 1.25（官方 MCP Go SDK 的要求），依赖 UUID 库、飞书官方 Go SDK 和 Eino 模型组件。根目录提供统一检查入口：

```sh
make fmt        # 格式化 Go 源码
make check      # Go 检查 + Python 启动脚本测试
make test       # Go 与 Python 启动脚本测试
```

`make go-check` 仅运行 Go 检查。在 `services/api` 内运行 `go test -race ./...` 可检查并发访问。

golangci-lint 固定为 v2.14.0，自动安装到 Git 忽略的 `services/api/bin/`；首次安装需要 curl 与网络。CI 运行 `make check`。

## 文档

飞书接入服务使用独立入口 `services/api/cmd/feishu`，默认通过官方 SDK 长连接接收消息事件和卡片回调。配置、云服务器部署与真实联调步骤见 [飞书接入说明](deploy/feishu/README.md)。

- [文档索引](docs/README.md)
- [V2 产品方案](docs/v2/产品方案.md)
- [V2 技术方案](docs/v2/技术方案.md)
- [V2 开发任务清单](docs/v2/开发任务清单.md)
- [V2 运行说明](docs/v2/运行说明.md)
- [V1 历史产品方案](docs/v1/产品方案.md)

## AI Trace

Go API 使用 OpenTelemetry 直接向 Phoenix 上报 AI 调用链，只采集 @AI、重试、Workflow 和模型调用；普通聊天和其他 HTTP 请求不上报。Phoenix 使用独立 SQLite 存储，配置与查看方式见 [Phoenix 接入说明](deploy/phoenix/README.md)。

### 双人群聊与共享 AI

Web 入口为 `/page/chat/`，支持实时文字消息、历史分页、未读角标、阅读进度和断线补拉。`make dev` 默认开启共享 AI，使用现有模型 profile；可用 `-chat-ai=false` 显式关闭；模型 ID 必须为实际可调用名称。普通消息不触发模型，@AI 提问排队，完整回复保存后向双方推送。实现和接口详见 [双人群聊接口](docs/v2/双人群聊接口.md)。

同机部署 Phoenix 收集 Trace，见 [Phoenix 部署说明](deploy/phoenix/README.md)。
