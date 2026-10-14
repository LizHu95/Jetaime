# 日常协同 AI 助理

## Web 前端

响应式 Web 客户端位于 `apps/web/`，使用 React、TypeScript、Vite、shadcn/ui 和 TanStack Query。已提供登录、注册及登录结果/退出页面，适配手机与 PC，并接入现有认证 API。

首次运行 `cd apps/web && npm ci`，随后在仓库根目录运行 `make web`，打开 <http://127.0.0.1:5173/page/auth/login/>。真实认证需另行启动 `make dev` 并完成 PostgreSQL 配置与迁移。构建使用 `make web-build`，产物位于 `outputs/web/`；完整说明见 [Web 开发说明](apps/web/README.md)。

## 后端与 Workflow 调试台

当前版本为 **V2 最简 Workflow 调试台**：从群组聊天历史回答问题并引用来源，提取明确行动，经成员确认后保存为待办。支持本地 JSON 历史与模拟发消息，提供可查看输入、模型响应和校验结果的调试页面。

一期的收藏驱动推荐、反馈与换批代码已移除。历史产品方案和评测结果归档在 `docs/v1/`，不再提供一期运行入口。

## 本地运行

统一入口会自动加载本地环境与默认模型配置。在仓库根目录启动：

```sh
make dev          # 认证 API 与 /test/healthz，需要 PostgreSQL
make assistant    # 显式启用 Assistant 调试模块
make model-check  # 只校验模型配置与密钥存在性，不调用模型
make trace        # Phoenix
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

`make dev` 默认启用认证 API 与 `/test/healthz`，自动加载 `local/.env`，需要 PostgreSQL 连接与已迁移的数据库；无需模型配置或历史文件。直接运行 API 二进制不传业务模块参数时仍只提供 `/test/healthz`，认证通过 `-auth` 启用。通过 `-assistant` 或 `make assistant` 显式启用 Assistant 模块。

使用 Go 标准库 `net/http` 与 `ServeMux`。总 Router 在 `internal/httpapi/router.go`，只内置测试接口，通过 `NewRouter(modules ...Module)` 接收注入模块。各模块通过 `RegisterRoutes` 挂载 API 与页面；接口显式注册 HTTP 方法，业务 handler 分开实现。

PostgreSQL 基础接入已完成：`JETAIME_DATABASE_URL` 配合 `make db-migrate` 初始化空库，API 加 `-database` 开启连接及版本检查。SQL 随二进制嵌入，迁移有事务、并发锁、版本与校验和。`JETAIME_TEST_DATABASE_URL` 配合 `make db-test` 运行真实 PostgreSQL 集成测试，测试创建并清理独立 schema。数据库接入与启动方式见 [部署说明](deploy/api/README.md)，表结构见 [PostgreSQL 数据设计](docs/v2/PostgreSQL数据设计.md)。注册、登录、退出接口已实现，通过 `-auth` 开启，见 [认证接口说明](docs/v2/认证接口.md)；聊天业务接口仍待开发。

- `POST /api/auth/register`、`/api/auth/login`、`/api/auth/logout`：账号认证模块，`make dev` 默认启用。
- `/api/assistant/...`：catalog、state、run、messages、reload、dismiss、confirm、complete；页面在 `/page/assistant/`，仅允许本机访问，保留模拟身份与群组校验。
- `GET /test/healthz`：固定返回测试 JSON，不代表模型或数据库就绪。

旧的无业务域 `/api/...` 路径已替换，前端与验证脚本同步更新。根路径不注册，返回 404；使用完整业务页面地址。飞书继续通过 SDK 长连接接收事件，目前没有需要注册的飞书 HTTP 接口。`GET /test/healthz` 用于测试连通性，固定返回 `{"status":"ok","message":"hello"}`。

```sh
make dev                                  # 默认启用注册、登录、退出与健康检查
make dev API_ARGS='-auth=false'            # 只启用健康检查，不连接数据库
make assistant                            # 注入 Assistant 调试模块
```

统一 HTTP API 打包运行 `make api-build`，产物保存到 `outputs/api/`，包含 Linux amd64 / arm64 二进制和 `api-deploy.tar.gz`。详见 [API 打包说明](deploy/api/README.md)。

## 代码结构

```text
services/api/
├── cmd/api/main.go             统一 HTTP 服务入口
├── internal/httpapi/router.go 总 Router，装配业务模块
├── internal/assistant/
│   ├── types.go               实体与输入输出类型
│   ├── store.go               群组隔离、消息与待办 JSON 存储
│   ├── workflow.go            历史问答与待办提取
│   ├── server.go              本地 API 与页面适配
│   ├── web/                   内嵌 HTML / CSS / JS 调试页面
│   └── *_test.go              Workflow、存储、HTTP 和模型协议测试
services/api/internal/llm/      Eino 适配器与共享启动模型配置
services/api/config/            无密钥的模型配置示例
local/data/assistant/           本地状态与运行日志，Git 忽略
local/data/chat-history/        用户提供的历史、原始整理结果与说明
local/data/evals/               私有评测输入、审核材料与冻结版本
outputs/evals/reports/          生成的私有评测报告
local/data/phoenix-v2/          本地 Phoenix 数据库
docs/v2/                       当前设计、技术方案、任务与运行说明
outputs/evals/                 实验结果、汇总与源码快照
outputs/agent-check/           链路验证结果
outputs/feishu/                构建及部署产物（可能含凭据）
local/notes/                   私人文档
docs/v1/                       一期历史文档与评测结果
```

先从 [workflow.go](services/api/internal/assistant/workflow.go) 阅读两条固定流程：组装授权群组上下文 → Prompt / Schema → 模型调用 → 校验结果。问答调用一次；提取产生候选时追加状态核对调用。任务保存由确认接口触发，模型不能直接创建任务。当前不使用 Agent Loop、自动模型路由、摘要、检索或长期记忆。

目录职责、迁移与清理规则见 [目录约定](docs/目录约定.md)。`deploy/` 保存部署定义，`local/` 保存实际配置和持久数据，`outputs/` 保存生成产物。

## 开发与检查

Go 工具链版本由 `.go-version` 管理；Go 模块最低要求 Go 1.24，依赖 UUID 库、飞书官方 Go SDK 和 Eino 模型组件；Phoenix 评测使用独立 Python 项目 `evals/`。根目录提供统一检查入口：

```sh
make fmt        # 格式化 Go 源码
make check      # Go 检查 + Python 评估器测试
make test       # Go 与 Python 测试
```

`make go-check` 仅运行 Go 检查。在 `services/api` 内运行 `go test -race ./...` 可检查并发访问。JavaScript 语法可通过 `node --check services/api/internal/assistant/web/app.js` 验证。

golangci-lint 固定为 v2.14.0，自动安装到 Git 忽略的 `services/api/bin/`；首次安装需要 curl 与网络。CI 运行 `make check`。修改嵌入页面后需重启服务并刷新浏览器。

## 文档

飞书接入服务使用独立入口 `services/api/cmd/feishu`，默认通过官方 SDK 长连接接收消息事件和卡片回调。配置、云服务器部署与真实联调步骤见 [飞书接入说明](deploy/feishu/README.md)。

- [文档索引](docs/README.md)
- [V2 产品方案](docs/v2/产品方案.md)
- [V2 技术方案](docs/v2/技术方案.md)
- [V2 开发任务清单](docs/v2/开发任务清单.md)
- [V2 运行说明](docs/v2/运行说明.md)
- [V1 历史产品方案](docs/v1/产品方案.md)

## Phoenix 评测

`make trace` 启动本地 Phoenix，`make eval-check` 离线验证案例，`make eval` 执行 V2 Workflow 实验。正式案例与预期结果待提供，当前文件为空。完整输入契约、评估器与运行方式见 [Phoenix 评测说明](docs/v2/Phoenix评测说明.md)。
