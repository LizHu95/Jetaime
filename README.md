# 今天干嘛

面向个人与情侣的 AI 生活决策助手。V1 为微信小程序，以笔记收藏、空间共享、长期偏好、候选内 Top 3 和反馈形成闭环。当前已有实体规则、内存 DecisionService、演示资料与 CLI，能运行 select → 反馈 → 换批；推荐生成使用本地 Ollama，真实模型效果待评测。模型生成通过 `decisions.Provider` 接口接入，后续云模型可实现该接口并加入 Web/CLI 的配置入口。已提供本机调试 API 与浏览器调试台；生产 HTTP、真实登录和数据库尚未接入。

图片与多模态为 P1，分享、社区和多人空间属于后续版本。

## 目录

```text
apps/
└── client/
    └── src/
        ├── pages/         页面
        ├── components/    共用组件
        ├── api/           API 调用
        ├── platform/      小程序与 H5 平台适配
        └── types/         客户端类型
services/
└── api/
    ├── cmd/
    │   ├── api/           HTTP 服务入口
    │   └── decision/      CLI 决策验证入口
    ├── internal/
    │   ├── auth/          微信登录与会话
    │   ├── space/         个人/情侣空间、成员与邀请
    │   ├── notes/         笔记内容与空间收藏关系
    │   ├── memory/        长期偏好、排斥项与硬约束
    │   ├── decisions/     决策流程、会话、历史与反馈
    │   ├── demo/          虚构资料、事实校验替身、固定 Provider
    │   ├── ollama/        本地模型 HTTP 适配、提示词与输出 Schema
    │   └── telemetry/     可选 OTel 追踪、步骤输入输出与 Phoenix 上报
    └── db/
        ├── migrations/    数据库迁移
        └── queries/       SQL 查询
contracts/                OpenAPI 接口合同
deploy/                   部署配置
docs/                     产品、技术方案与交互原型
```

空目录使用 `.gitkeep` 保留，添加实际文件后可删除对应占位文件。

以上为当前实际目录。CLI 已调用 DecisionService，后续 HTTP 复用同一业务入口；配置组装、HTTP 适配、数据库和模型适配目录在实现对应能力时增加。图片模块留待 P1，V1 不设置独立执行计划模块。auth、数据库和小程序客户端仍为占位；cmd/api 提供本机开发调试服务。

## 本地决策演示

在仓库根目录运行，推荐请求需要本机 Ollama 已启动并下载模型；列出场景、导出资料无需连接模型：

```bash
cd services/api
go run ./cmd/decision                         # 默认演示：生成、反馈、换批、重试、新需求
go run ./cmd/decision -interactive            # 手动操作；输入 quit 退出
go run ./cmd/decision -list                   # 查看七个固定场景
go run ./cmd/decision -scenario unknown -once # 必要事实未知，不返回可采纳选项
go run ./cmd/decision -scenario normal -json  # 每步输出一个 JSON 对象
```

交互命令：`adopt 1`、`reject 2 user-b`、`batch user-b`、`retry`、`budget 200`（全体参与人总预算，整数元）、`query 新需求`、`history`、`session`。需求或预算改变会新建 Session；失败保留原组。`query` 只改变需求文本，类型和其他表单条件仍来自所选场景；模型结合正文判断语义限制。`history` 查看当前批次历史。身份是虚构 user-a/user-b，不代表已实现真实登录。

| 场景 | 验证内容 |
| --- | --- |
| normal | 晚餐总预算150元、60分钟、B不吃花生，完整反馈换批 |
| conflict | 显式零预算，所有候选都有已知违反 |
| unknown | 必须无障碍，但没有已确认设施信息 |
| empty | 空间没有指定类型收藏 |
| personal | 本人空间、收藏和私有记忆隔离 |
| contradiction | 同时要求室内和户外 |
| short | 5个电影候选，第二批只返回剩余2个未看选项 |

默认资料为50条虚构 Note（餐厅20、菜谱15、活动10、电影5）、双方各12条 Memory，以及各自个人空间和情侣空间的收藏。可用 `go run ./cmd/decision -dump-fixture > /tmp/jetaime-fixture.json` 导出，修改人工事实后通过 `-fixture /tmp/jetaime-fixture.json` 读取。资料生成见 `internal/demo/fixture.go`；没有真实商户、隐私或实时信息。

可编辑资料已放在 `services/api/testdata/demo/fixture.json`。在仓库根目录执行：

```bash
cd services/api
go run ./cmd/decision -fixture ./testdata/demo/fixture.json -provider ollama -interactive
```

保持 CLI 运行，编辑并保存这个 JSON，然后在 CLI 输入 `reload`。它重新读取 `dataset`（笔记、收藏、成员和记忆）与 `facts`（核实费用、时长、食材等），按当前需求立即生成新一轮。只有读取、实体校验和推荐生成都成功，才切换资料与新 Session；失败保留原资料和结果。修改笔记正文不会自动改写 facts，两者应按实际信息同步维护。

重载沿用当前 query、预算等已确认条件；`scenarios` 只在启动时选择场景。成功后清除当前换批重试状态，旧会话过期，重新开始推荐/排除/采纳状态；旧 Decision 的笔记和反馈快照仍保留在进程内。`history <DecisionID>` 可查询旧批次，但仍按新资料中的成员和收藏权限授权，撤销权限后可能不可访问。未指定 `-fixture` 时 `reload` 会提示用文件模式重新启动；本期不自动监视文件，也不将对话自动写成 Memory。

当前 store 和命令状态仅在进程内保留，退出后清空。运行时仅使用 Ollama Provider；测试使用隔离的 HTTP 测试服务，不包含运行时固定规则推荐器。独立 Checker 只检查已填写的预算和时长，事实缺失仍返回未知。Ollama 接收已授权记忆与描述，判断自然语言限制的相关性；药物过敏不应阻塞电影推荐。模型可以因适用限制返回条件冲突或信息不足，不能绕过权限、类型和数值检查。此演示不代表真实语义理解、推荐质量或生产权限已经验收。

## 本地 Web 调试台

在仓库根目录运行 `make debug`，或手动启动：

```bash
cd services/api
go run ./cmd/api -fixture ./testdata/demo/fixture.json -trace
```

`make debug` 默认同时记录页面 Trace 并上报 Phoenix。先在另一个终端运行 `make trace`，再启动调试台；如果 Phoenix 已运行，无需重复启动。可以用 `make debug DEBUG_ARGS=` 仅记录页面，或用 `make debug DEBUG_ARGS="-trace -trace-project jetaime-debug"` 更换项目。手动运行 `cmd/api` 时需显式加 `-trace` 开启上报。

打开 <http://127.0.0.1:8080>。Go 服务内嵌静态 HTML/CSS/JS，无需安装前端依赖或单独运行前端。仅使用 Ollama，可以在页面指定模型；模型需要预先下载，Ollama 需要运行。服务启动参数包括 `-addr 127.0.0.1:8080`、`-ollama-url`、`-model`、`-model-timeout` 和 `-context-tokens`，模型默认值与 CLI 相同。

页面支持：

- 选择七个固定场景，编辑空间、模拟发起人、收藏类型、需求文本、预算、时长、硬约束与偏好。实际参与人由空间成员确定。预算单位为元，空值表示不限，0 表示明确零预算；需求文本不自动改变其他条件。
- 查看 Top 3、推荐理由与参与人解释；模拟成员采纳、拒绝、换批或重试失败换批。每个会话保留生成时的 Provider 与模型配置。
- 编辑 fixture 文件后重载，使用当前显示批次的已确认需求重新生成；新资料验证和生成全部成功才切换，失败保留旧资料和结果。成功后旧会话过期，历史仍保留并按新资料授权。
- 按模拟身份查看跨会话历史、各轮条件、反馈、采纳及排除状态。切换到旧批次时不能从它发起新换批。
- 查看每步输入输出、耗时、合法候选的硬约束结论、人工事实与授权记忆；Ollama 调用展示实际 Prompt、Schema、原始响应和可用 Token 统计。失败也保留 Trace，可导出 JSON。Trace 仅保留最近 100 次操作，不依赖 Phoenix。

这是本机开发工具，模拟身份由请求提供，没有真实认证。服务仅允许监听 loopback IP，并拒绝非本机 Host、跨源请求；完整调试记录可由本机调试者读取。状态仅在进程内保留，多浏览器窗口共享同一服务；变更操作串行化，执行模型请求期间仍可读取配置和历史。重启清空页面中的会话、历史与 Trace；已上报 Phoenix 的记录仍持久保存。前端修改需要重启 Go 服务并刷新，因为静态资源通过 `go:embed` 编译进程序。

开启上报后，页面操作在 Phoenix 中以 `debug.decisions`、`debug.sessions/{id}/batch`、`debug.decisions/{id}/feedback`、`debug.fixture/reload` 等根 span 展示，生成流程和 Ollama 调用是子 span；读取页面、历史和 Trace 不产生新追踪。页面响应及导出 JSON 的 `otelTraceId` 与 Phoenix Trace ID 一致，根 span 的 `debug.trace_id` 对应页面记录 ID。两边共享同一批 span；Phoenix 使用服务级异步队列，离线不影响页面结果，Ctrl+C 正常停止服务时最多等待 3 秒导出剩余记录。

本地 API 使用 JSON；成功响应含 `decision`、`session`、`trace`，失败响应含 `error` 与已记录的 `trace`。模拟操作人使用 `actorId`；反馈、换批的 `eventId` 必须在同一操作重试时复用。

| 接口 | 请求 / 用途 |
| --- | --- |
| `GET /api/config` | 配置、空间、成员和待重试命令 |
| `GET /api/scenarios` | 固定场景及初始 Request |
| `GET /api/current?actorId=user-a` | 当前批次与会话；省略身份时使用当前发起人 |
| `POST /api/decisions` | `{request, provider, model}`，新建会话 |
| `POST /api/decisions/{id}/feedback` | `{actorId, eventId, action, optionId}`，action 为 adopt/reject |
| `POST /api/sessions/{id}/batch` | `{actorId, eventId, decisionId}`，换批 |
| `POST /api/sessions/{id}/retry` | `{actorId}`，重试该成员的原失败换批 |
| `GET /api/history?actorId=user-a` | 所有可访问批次及关联 Trace ID |
| `GET /api/sessions/{id}/history?actorId=user-a` | 指定会话历史 |
| `GET /api/traces/{id}` | 读取 Trace，超过保留窗口返回 404 |
| `GET /api/traces/{id}/download` | 下载完整 Trace JSON 附件 |
| `POST /api/fixture/reload` | `{decisionId, actorId}`，重载当前显示需求；`{}` 使用服务当前批次 |

代码入口为 `cmd/api/main.go`，本地 API、内存 Trace 与页面在 `internal/debugui/`。它复用现有 DecisionService 和 MemoryStore，不代表生产 HTTP 权限、数据持久化或真实模型质量已经验收。

## 使用本地 Ollama

先启动 Ollama 服务并下载模型。如果已使用 `brew services start ollama` 后台运行，无需另开 `ollama serve`。

```bash
ollama pull qwen3.5:9b
cd services/api
go run ./cmd/decision -provider ollama -once
go run ./cmd/decision -provider ollama -interactive
```

CLI 默认使用 `-provider ollama`，不再支持 Mock。Ollama 默认地址为 `http://localhost:11434`，模型 `qwen3.5:9b`，上下文 8192 token，单次请求超时 3 分钟，最多生成 2048 token；思考与流式输出关闭，temperature 为 0。需要时通过 `-ollama-url`、`-model`、`-context-tokens` 和 `-model-timeout` 覆盖，例如：

```bash
go run ./cmd/decision -provider ollama -model qwen3.5:4b -context-tokens 8192 -model-timeout 5m -once
```

`internal/ollama/provider.go` 调用 `/api/chat`，`prompt.go` 定义中文提示词与 JSON Schema。模型只收到当前已授权、已通过数值条件检查的候选与记忆；不传完整 fixture。服务端继续生成 OptionID 并校验候选引用、参与者解释及数值条件结论。调用失败、超时、截断、损坏 JSON 或不合法推荐返回错误，不使用模拟结果回退；无候选、数值条件全部违反或必要数值事实不足时不调用模型；自然语言限制由模型判断。

当前只接入推荐生成；基础资料和事实检查仍是虚构数据，`query` 不会自动修改已确认条件。真实模型的解释准确性、偏好平衡和延迟需在下载完成后独立评测。上下文设置需容纳提示词、候选与输出；数据量增长时再引入检索与 token 管理。

## 用 Phoenix 查看每轮输入输出

本地 Phoenix 固定为 20.20.0，使用 uv 的隔离工具环境，不修改系统 Python；初次启动会下载依赖。在仓库根目录开一个终端运行：

```bash
make trace
```

保持这个终端运行，打开 http://127.0.0.1:6006。SQLite 数据保存在 Git 忽略的 `deploy/data/phoenix/`，停止后仍保留；服务仅监听本机，关闭 Phoenix 自身的遥测。另一个终端运行：

```bash
cd services/api
go run ./cmd/decision -fixture ./testdata/demo/fixture.json -provider ollama -trace -interactive
```

在 Phoenix 选择 `jetaime` 项目，打开 `decision.generate` Trace。树形步骤包括 `prepare_session`、`build_context`、`evaluate_candidates`、`generate_result`、`validate_result` 和 `commit`；`ollama.generate` 是生成步骤中的 LLM 子 span。只有实际执行的步骤才出现，模型解码失败时不会进入最终校验或保存，无满足候选时不会出现 LLM 调用。点击步骤查看 Input/Output 和错误；LLM span 保存实际请求、完整 Prompt、原始响应，以及服务返回的 Token 数。没有返回用量时不伪造数值。

CLI 的 `-trace` 默认关闭；显式开启才向 Phoenix 发送完整调试输入输出。只记录本轮已授权的 Context，不记录整个 fixture；根 span 的 `fixture.sha256` 是当前资料的内容版本，`reload` 成功后下一轮使用新版本。每次生成一条 Trace，包含新需求和换批；已完成换批的幂等重试复用结果，不再次生成 Trace。CLI 当前不包含独立反馈操作追踪（Web 调试台会记录反馈操作），也不实现实时流式内容显示。

可用 `-trace-endpoint http://127.0.0.1:6006/v1/traces` 和 `-trace-project jetaime` 修改上报地址与项目。OTel SDK 固定为 1.34.0，以保持当前 Go 1.22 系列的兼容性。采用异步有界队列，Phoenix 离线会在 stderr 提示追踪丢失，业务继续运行；CLI 正常退出时最多等待 3 秒导出。使用 Ctrl+C 强制中断 CLI 可能丢失尚未导出的记录；Phoenix 在自己的终端中使用 Ctrl+C 停止。

实现依据：[Phoenix 本地运行](https://github.com/Arize-ai/phoenix#run-locally)、[OpenInference LLM 属性](https://github.com/Arize-ai/openinference/blob/main/spec/llm_spans.md)、[OpenTelemetry Go](https://opentelemetry.io/docs/languages/go/)。

## 代码阅读

先打开 [service.go](services/api/internal/decisions/service.go)，从 Generate 跟到 generate。推荐主流程按六步排列：准备会话 → 组装上下文 → 检查硬约束 → 生成并校验 → 更新推荐状态 → 提交保存。

| 想了解的内容 | 对应文件 |
| --- | --- |
| 推荐业务顺序 | [service.go](services/api/internal/decisions/service.go) |
| 候选与 Memory 从哪里来、如何限制权限 | [context_builder.go](services/api/internal/decisions/context_builder.go) |
| 单步实现：会话、三态评估、推荐生成与历史构建 | [generation_steps.go](services/api/internal/decisions/generation_steps.go) |
| 采纳、拒绝和换批 | [feedback_service.go](services/api/internal/decisions/feedback_service.go) |
| 内存复制、锁、版本检查与命令保存 | [store.go](services/api/internal/decisions/store.go) |
| 当前模型实现的选择与注入 | [main.go](services/api/cmd/decision/main.go) |
| Ollama 请求、响应解码与中文提示词 | [provider.go](services/api/internal/ollama/provider.go)、[prompt.go](services/api/internal/ollama/prompt.go) |
| 追踪初始化、步骤输入输出与异步上报 | [trace.go](services/api/internal/telemetry/trace.go) |

第一次阅读先跟通 service.go 的调用顺序，再打开感兴趣的步骤。反馈服务同样按读取授权 → 检查采纳事实 → 应用规则并保存的顺序组织。锁和 map 操作集中在 MemoryStore 中。

## 开发与检查

仓库采用多语言单仓结构：Go 后端位于 `services/api`，TS 客户端预留在 `apps/client`。各项目维护自己的依赖、配置与检查命令，根目录 Makefile 提供统一入口。当前客户端尚无 `package.json` 或 TS 工具链，统一入口只执行已实现的 Go 检查。

开发与 CI 使用的 Go 版本记录在 `.go-version`，当前为 Go 1.27.1。请先安装该版本的 Go，以及 Make、curl；以下命令在仓库根目录执行。

```bash
make tools      # 安装开发工具，首次需要联网
make fmt        # 格式化当前已实现的项目
make check      # 检查当前已实现的项目
make go-check   # 仅检查 Go：格式、编译、vet、测试和 lint
```

`make build`、`make test`、`make lint` 和 `make fmt-check` 也是仓库统一入口。Go 专用命令为 `make go-tools`、`make go-fmt`、`make go-fmt-check`、`make go-build`、`make go-test`、`make go-vet` 和 `make go-lint`；`make vet` 保留为 `make go-vet` 的快捷入口。

Go 检查和 lint 会在工具缺失时自动安装 golangci-lint；版本固定为 v2.14.0，安装到已被 Git 忽略的 `services/api/bin/golangci-lint/v2.14.0/`，无需全局安装。

`services/api/.golangci.yml` 仅启用 errcheck、govet、ineffassign、staticcheck 和 unused；staticcheck 仅启用 SA 类正确性检查，不限制函数长度、命名风格或测试覆盖率。格式检查只报告问题，修复时执行 `make go-fmt` 并检查差异；编辑器也可以启用保存时格式化。

`.github/workflows/go.yml` 在 push、pull request 和手动触发时运行 `make go-check`。当前不强制安装 Git hook，也不校验提交信息格式。

引入 TS 客户端时，先在 `apps/client/package.json` 定义 `format`、`format:check`、`lint`、`typecheck`、`test` 和 `build` 脚本，再添加 `client-*` Make 目标并接入根目录统一入口。届时固定 Node 与包管理器版本、提交依赖锁文件，并增加独立的客户端 CI 工作流，分别展示 Go 和 TS 的检查结果。共享配置按实际复用需要添加。

更新工具版本时，修改 `.go-version` 或 Makefile 中的 `GOLANGCI_LINT_VERSION`，并同步本节说明；确认所选 golangci-lint 支持该 Go 版本后运行 `make check`。`services/api/go.mod` 的 `go` 指令仍为 1.22；该最低版本的兼容性目前未纳入 CI。

已有实体规则、决策 Workflow、CLI 和失败恢复测试，以及 Ollama 测试服务的协议、权限过滤、异常响应和取消测试；`go test -race ./...` 可在 `services/api` 内检查并发访问。真实模型效果、HTTP 权限和通用语义约束验证仍待完成。

## 项目文档

- [文档索引](docs/README.md)
- [产品方案](docs/产品方案-V1.0.md)
- [领域模型与决策契约](docs/领域模型与决策契约.md)
- [技术方案](docs/技术方案-V1.0.md)
- [开发任务清单](docs/开发任务清单-V1.0.md)
