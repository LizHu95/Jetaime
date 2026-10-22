# 日常协同 AI 助理

## Web 前端

响应式 Web 客户端位于 `apps/web/`，使用 React、TypeScript、Vite、shadcn/ui 和 TanStack Query。已提供登录、注册，以及登录后的聊天和我两个底部 Tab，支持群聊、加好友、修改密码和退出登录，适配手机与 PC，并接入现有 API。

首次运行 `cd apps/web && npm ci`，随后在仓库根目录运行 `make web`，打开 <http://127.0.0.1:5173/page/auth/login/>。真实认证需另行启动 `make dev` 并完成 PostgreSQL 配置与迁移。构建使用 `make web-build`，产物位于 `outputs/web/`；完整说明见 [Web 开发说明](apps/web/README.md)。

## 后端与 Workflow 调试台

当前版本为 **V2 最简 Workflow 调试台**：从群组聊天历史回答问题并引用来源，提取明确行动，经成员确认后保存为待办。支持本地 JSON 历史与模拟发消息，提供可查看输入、模型响应和校验结果的调试页面。

一期的收藏驱动推荐、反馈与换批代码已移除。历史产品方案和评测结果归档在 `docs/v1/`，不再提供一期运行入口。

## 本地运行

统一入口会自动加载本地环境与默认模型配置。在仓库根目录启动：

```sh
make dev          # 认证、好友和群聊 API，需要 PostgreSQL
make assistant    # 显式启用 Assistant 调试模块
make model-check  # 只校验模型配置与密钥存在性，不调用模型
make feishu       # 飞书长连接
```

首次使用时，将 `services/api/config/models.example.json` 复制到 `local/models.json`，编辑模型参数与顶层 `default_profile`。实际密钥保存在 `local/.env`；启动入口自动加载该文件。飞书额外读取 `local/feishu/.env`。已有 shell 环境变量优先于文件，同名文件配置中飞书专用值优先于共享值。

```sh
# 临时切换，省略默认配置文件路径：
make assistant ASSISTANT_ARGS='-model-profile local-qwen'
# 指定其他配置文件或服务端口：
make assistant ASSISTANT_ARGS='-model-config ../../local/other-models.json -addr 127.0.0.1:8082'
```

也可使用 `python3 scripts/dev.py assistant`、`python3 scripts/dev.py eval` 等同一入口。模型参数放在 JSON 中，启动命令不需要手动 source 或逐项指定模型参数。环境文件使用单行 `KEY=value` 或带引号的值，不执行命令替换或变量展开。直接 `go run` 不加载环境文件。

支持 Eino Ollama / OpenAI 兼容接口，启动时选择模型，切换需重启；云模型与 profile 配置见 [模型接入说明](docs/v2/运行说明.md#eino-模型接入与启动选择)。

打开 <http://127.0.0.1:8081/page/assistant/>。页面内嵌于 Go 服务，无需前端依赖或构建。Ctrl+C 停止服务。

`make assistant` 默认加载你提供的微信记录：`local/data/chat-history/history.fixture.json`（2 位成员、441 条消息），不再包含虚构聊天数据。上下文容量使用所选 profile 的配置；未配置时默认 98304。页面“重新读取历史 JSON”重新读取该文件。模型调用默认超时为 5 分钟，以容纳全量历史的本地推理。

运行 `make assistant-check` 可用同一份历史和真实 Ollama 模型验证问答引用、待办提取、确认幂等、完成、重启恢复及 JSONL 轨迹。验证服务使用临时端口和状态目录，不改调试台的消息与任务。先验证全量历史，再回放原记录中的明确日程片段来验证确认与完成；报告保存到 `outputs/agent-check/acceptance.json`。

页面支持群组 / 模拟成员切换、历史问答与来源定位、模拟发送消息后触发 Workflow、待办提取 / 修改 / 忽略 / 确认、本人待办完成，以及运行轨迹查看和 JSON 下载。消息与已确认待办保存在本地，重启可恢复。

这是本机调试工具，未实现真实登录、邀请和多人实时同步。来源 ID 校验不等于语义准确，模型仍可能漏提取或引用不完整，确认前需核对原文。具体参数、文件与边界见 [V2 运行说明](docs/v2/运行说明.md)。

## HTTP 路由与模块装配

`cmd/api/main.go` 读取命令行参数并监听退出信号 → `config.Parse` 生成配置 → `app.Run` 初始化模块并运行服务。`-auth` 控制账号、好友、群聊和通知 API；`-chat-ai` 控制正式群聊中的主 Agent；`-debug-console` 只控制本地 JSON 模拟页面，三个开关的职责不同。

`make dev` 和 API 二进制默认启用认证、好友、群聊及实时同步 API、共享 AI 与 `/test/healthz`，需要 PostgreSQL 连接、已迁移的数据库及有效模型配置；无需历史文件。`make dev` 自动加载 `local/.env`，部署二进制从运行环境读取配置。仅需健康检查时显式传 `-auth=false`。通过 `-debug-console` 或 `make assistant` 显式启用本地调试台；`make assistant` 关闭认证以保留独立的本地 JSON 调试流程。

使用 Go 标准库 `net/http` 与 `ServeMux`。所有 HTTP 方法、路径和处理器绑定集中在 [internal/httpapi/router.go](services/api/internal/httpapi/router.go) 的 `NewRouter(Handlers)`，打开此文件即可查看完整路由表。启动流程在 [internal/app/server.go](services/api/internal/app/server.go)，模块装配在 [internal/app/modules.go](services/api/internal/app/modules.go)，按配置注入启用的端点组；未注入的组不注册路由。路由直接绑定业务 Handler 的具名方法（如 `ListMessages`、`SendMessage`），并显式包装 `Authenticate` 或 `LocalOnly` 中间件。模块装配注入 Handler 实例，不再通过 `Endpoints()` 转接字段。认证身份由私有 Context key 传递，参数解析留在 Handler，业务入口统一经过 Service；账号和业务 API 共用 `httpapi.WriteJSON` 输出响应。

HTTP 接口以实际客户端调用为边界；记忆检索、Worker 调度、模型调用和 Agent 编排通过进程内方法协作。当前路由用途如下：

| 路由组 | 实际调用方 |
| --- | --- |
| `/api/auth/register`、`login`、`logout`、`password` | 登录、注册、退出与修改密码页面；用户信息随登录结果返回 |
| `/api/contacts/*`、`/api/groups` | 好友管理与群聊列表 |
| `/api/chat/*` | 聊天、阅读进度、来源定位、AI 任务状态/重试与 WebSocket 同步 |
| `/api/push/*` | 通知设置页面，包括用户主动测试通知的按钮 |
| `/api/assistant/*`、`/page/assistant/*` | 显式启动的本地调试台与检查脚本，受回环地址和同源限制 |
| `/test/healthz` | 部署连通性检查 |

原 `GET /api/auth/me` 没有当前调用方，已移除；共享的 `Authenticate` 会话校验继续服务各业务接口。

PostgreSQL 基础接入已完成：`JETAIME_DATABASE_URL` 配合 `make db-migrate` 初始化空库，默认认证模块开启连接及版本检查。SQL 随二进制嵌入，迁移有事务、并发锁、版本与校验和。`JETAIME_TEST_DATABASE_URL` 配合 `make db-test` 运行真实 PostgreSQL 集成测试，测试创建并清理独立 schema。数据库接入与启动方式见 [部署说明](deploy/api/README.md)，表结构见 [PostgreSQL 数据设计](docs/v2/PostgreSQL数据设计.md)。注册、登录、退出接口默认开启，见 [认证接口说明](docs/v2/认证接口.md)；双人群聊、实时同步、阅读进度和共享 AI 接口已实现，见 [双人群聊接口](docs/v2/双人群聊接口.md)。

历史向量化与增量记忆已实现独立 Worker、持久游标/检查点、pgvector 和受群成员权限保护的内部检索方法。正式群聊首次模型调用只携带有预算限制的近期消息；依赖历史且证据不足时，主 Agent 调用 `answer_question` 混合检索并回查原文，所有取数遵守原提问截止序号。正式 API 启动时自动运行记忆 Worker，无需 enabled 开关，需有效的记忆模型配置与密钥；首次部署需安装 pgvector 并执行迁移，重启复用已完成数据。配置、恢复保证和接口见 [历史向量化与增量记忆](docs/v2/历史向量化与增量记忆.md)。

- `POST /api/auth/register`、`/api/auth/login`、`/api/auth/logout`：账号认证模块，`make dev` 默认启用。
- `/api/assistant/...`：catalog、state、run、messages、reload、dismiss、confirm、complete；页面在 `/page/assistant/`，仅允许本机访问，保留模拟身份与群组校验。
- `GET /test/healthz`：固定返回测试 JSON，不代表模型或数据库就绪。

旧的无业务域 `/api/...` 路径已替换，前端与验证脚本同步更新。根路径不注册，返回 404；使用完整业务页面地址。飞书继续通过 SDK 长连接接收事件，目前没有需要注册的飞书 HTTP 接口。`GET /test/healthz` 用于测试连通性，固定返回 `{"status":"ok","message":"hello"}`。

```sh
make dev                                  # 默认启用认证、好友、群聊、实时同步与健康检查
make dev API_ARGS='-auth=false'            # 只启用健康检查，不连接数据库
make assistant                            # 注入 Assistant 调试模块
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
├── cmd/workflow-eval/         离线评测入口
├── internal/config/          命令行参数解析为明确的配置值
├── internal/app/             启动流程、模块装配与后台任务生命周期
├── internal/auth/            账号与会话
├── internal/collaboration/   好友申请、群组、成员与关闭事务
├── internal/chat/            消息、阅读进度与 PostgreSQL 适配
├── internal/reminder/        提醒领域的 Service、持久化、调度与 HTTP 接口
├── internal/ai/
│   ├── service.go            共用执行入口与协议
│   ├── bootstrap/            注册当前唯一的 main Agent
│   ├── runtime/              按 Agent ID 调度与执行身份、Trace
│   ├── agent/main/           主 Agent 工具选择与执行循环
│   ├── context/              上下文选择、引用保留与预算
│   ├── tools/                工具注册表及 qa、extract、reminder 等 AI 适配
│   ├── llm/                  模型配置与 Eino 供应商适配
│   ├── memory/               AI 记忆提取、检索、向量与索引存储
│   ├── prompts/              主 Agent 与提取提示词、调用 Schema
│   └── jobs/                 持久队列执行、超时与结果提交
├── internal/debugconsole/    JSON 模拟状态、调试 HTTP 与内嵌页面
├── internal/notification/    实时通知与 Web Push
├── internal/database/        PostgreSQL 连接与嵌入式迁移
└── internal/telemetry/       调用链
```

当前只注册一个 `main` Agent，注册点为 `internal/ai/bootstrap/service.go`。普通知识、建议、写作、澄清及当前上下文足够的问题由主 Agent 直接生成，通过只校验、不调用模型的 `respond` 终结响应返回，通常只需一次模型调用。正式群聊每次 AI 提问先用当前问题、直接引用和最多 12 条/4 KiB 近期背景判断需求；只有需要历史证据时才调用 `answer_question` 检索后回答，时间回顾使用 `summarize_history`，提醒使用 `set_reminder` 调用领域 Service。工具目录只向模型提供名称、描述和输入 Schema，输出 Schema 保留在后端校验。所有入口显式注册完整能力，bootstrap 不添加默认工具。`extract_todos` 已移除默认注册，提取实现暂留；行动事项只作文字总结，不生成结构化待办建议。`qa/help` 旧模式仍兼容 QA。主提示词版本为 `main-v8-on-demand-history`，QA 为 `answer-question-v2-hybrid`。

提示词按能力维护：主 Agent 的提示词、Schema、版本和请求构造位于 [`agent/main/prompts/`](services/api/internal/ai/agent/main/prompts/prompts.go)（上下文预算检查复用该子包）；问答位于 [`tools/qa/prompts.go`](services/api/internal/ai/tools/qa/prompts.go)；待办提取、接受识别和行动复核统一位于 [`tools/extract/prompts.go`](services/api/internal/ai/tools/extract/prompts.go)；记忆提取与合并位于 [`memory/prompts.go`](services/api/internal/ai/memory/prompts.go)。各能力的提示词、模型输出 Schema 与版本标识集中维护，执行和校验逻辑保留在原模块文件中。

生产群聊、调试台和离线评测均调用 `ai.Service → runtime → agent/main`。运行时只依赖 Agent 协议，具体 Agent 不依赖聊天、数据库或调试台；未来新增 Agent 在注册点装配，不向聊天 worker 添加分支。群共享记忆通过 QA 的函数式上下文提供器接入；调试台与离线评测默认只使用传入快照。尚未实现跨 Agent 委派与调用树恢复。

`ai.Job` 表示数据库排队与租约状态，`ai.Run` 表示一次执行记录，`ai.Result` 是允许保存并展示的回答或建议。每次运行带独立的 `agentId/invocationId`，原始模型响应和内部步骤不会进入消息 API 的结果对象。`chat` 的上下文适配器执行群权限与触发序号检查，`ai/context` 负责原文选择与预算。

消息与 AI 入队、AI 回复与完成状态分别保持单事务提交；群关闭事务仍会禁用助手并取消未完成任务，过期 worker 的 attempt/租约保护保留。数据库结构与已有 HTTP 路径不变。WebSocket 和 Web Push 位于通知模块，通知丢失后仍以数据库补拉为准。

调试台独立管理模拟身份、JSON 消息与任务、候选忽略和运行日志。评测在内存中适配 fixture，不再为了调用执行内核创建临时 JSON 文件；案例支持 `main/auto` 完整入口与 `qa/help` 兼容模式；冻结案例的 extract 标签由评测适配器转换成 main 请求。模型、提示词、执行器、案例与评估器均纳入新实验快照，历史冻结文件不改写。

已移除默认关闭的核对缓存实验、模型指纹查询和预留指令提供器；提取中的证据边界与行动状态核对继续保留。私有输入在 `local/`，生成产物在 `outputs/`，部署定义在 `deploy/`。

目录职责、迁移与清理规则见 [目录约定](docs/目录约定.md)。`deploy/` 保存部署定义，`local/` 保存实际配置和持久数据，`outputs/` 保存生成产物。

## 开发与检查

Go 工具链版本由 `.go-version` 管理；Go 模块最低要求 Go 1.24，依赖 UUID 库、飞书官方 Go SDK 和 Eino 模型组件；本地评测使用独立 Python 项目 `evals/`。根目录提供统一检查入口：

```sh
make fmt        # 格式化 Go 源码
make check      # Go 检查 + Python 评估器测试
make test       # Go 与 Python 测试
```

`make go-check` 仅运行 Go 检查。在 `services/api` 内运行 `go test -race ./...` 可检查并发访问。JavaScript 语法可通过 `node --check services/api/internal/debugconsole/web/app.js` 验证。

golangci-lint 固定为 v2.14.0，自动安装到 Git 忽略的 `services/api/bin/`；首次安装需要 curl 与网络。CI 运行 `make check`。修改嵌入页面后需重启服务并刷新浏览器。

## 文档

飞书接入服务使用独立入口 `services/api/cmd/feishu`，默认通过官方 SDK 长连接接收消息事件和卡片回调。配置、云服务器部署与真实联调步骤见 [飞书接入说明](deploy/feishu/README.md)。

- [文档索引](docs/README.md)
- [V2 产品方案](docs/v2/产品方案.md)
- [V2 技术方案](docs/v2/技术方案.md)
- [V2 开发任务清单](docs/v2/开发任务清单.md)
- [V2 运行说明](docs/v2/运行说明.md)
- [V1 历史产品方案](docs/v1/产品方案.md)

## AI Trace 与本地评测

Go API 使用 OpenTelemetry 直接向 Phoenix 上报 AI 调用链，只采集 @AI、重试、Workflow 和模型调用；普通聊天和其他 HTTP 请求不上报。Phoenix 使用独立 SQLite 存储，配置与查看方式见 [Phoenix 接入说明](deploy/phoenix/README.md)。

`make eval-check` 离线验证案例，`make eval` 执行本地 Workflow 评测并保存 JSONL 与汇总报告，不依赖 Trace 平台。输入契约、评估器与运行方式见 [Workflow 评测说明](docs/v2/Workflow评测说明.md)。


### 双人群聊与共享 AI

Web 入口为 `/page/chat/`，支持实时文字消息、历史分页、未读角标、阅读进度和断线补拉。`make dev` 默认开启共享 AI，使用现有模型 profile；可用 `-chat-ai=false` 显式关闭；模型 ID 必须为实际可调用名称。普通消息不触发模型，@AI 提问排队，完整回复保存后向双方推送。实现和接口详见 [双人群聊接口](docs/v2/双人群聊接口.md)。

同机部署 Phoenix 收集 Trace，见 [Phoenix 部署说明](deploy/phoenix/README.md)。
