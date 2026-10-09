# 今天干嘛

面向个人与情侣的 AI 生活决策助手。V1 为微信小程序，以笔记收藏、空间共享、长期偏好、候选内 Top 3 和反馈形成闭环。当前已有实体规则、内存 DecisionService、默认 Mock 数据与 CLI，能运行 select → 反馈 → 换批。真实模型、HTTP 和数据库尚未接入。

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
    │   └── demo/          虚构资料、事实校验替身、固定 Provider
    └── db/
        ├── migrations/    数据库迁移
        └── queries/       SQL 查询
contracts/                OpenAPI 接口合同
deploy/                   部署配置
docs/                     产品、技术方案与交互原型
```

空目录使用 `.gitkeep` 保留，添加实际文件后可删除对应占位文件。

以上为当前实际目录。CLI 已调用 DecisionService，后续 HTTP 复用同一业务入口；配置组装、HTTP 适配、数据库和模型适配目录在实现对应能力时增加。图片模块留待 P1，V1 不设置独立执行计划模块。auth、cmd/api、数据库和客户端仍为占位。

## 本地决策演示

在仓库根目录运行，无需外部账号、模型下载或数据库：

```bash
cd services/api
go run ./cmd/decision                         # 默认演示：生成、反馈、换批、重试、新需求
go run ./cmd/decision -interactive            # 手动操作；输入 quit 退出
go run ./cmd/decision -list                   # 查看七个固定场景
go run ./cmd/decision -scenario unknown -once # 必要事实未知，不返回可采纳选项
go run ./cmd/decision -scenario normal -json  # 每步输出一个 JSON 对象
```

交互命令：`adopt 1`、`reject 2 user-b`、`batch user-b`、`retry`、`budget 200`（全体参与人总预算，整数元）、`query 新需求`、`history`、`session`。需求或预算改变会新建 Session；失败保留原组。`query` 只改变需求文本，Mock 不解析自然语言，类型和其他表单条件仍来自所选场景。`history` 查看当前批次历史。身份是虚构 user-a/user-b，不代表已实现真实登录。

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

当前 store 和命令状态仅在进程内保留，退出后清空。固定 Provider 按已授权偏好与标签排序，独立 Checker 只支持预算、时长、不吃花生/海鲜、室内/户外和无障碍这些明确条件；未支持的硬条件返回未知，不默认为通过。此演示不代表真实语义理解、推荐质量或生产权限已经验收。

## 代码阅读

先打开 [service.go](services/api/internal/decisions/service.go)，从 Generate 跟到 generate。推荐主流程按六步排列：准备会话 → 组装上下文 → 检查硬约束 → 生成并校验 → 更新推荐状态 → 提交保存。

| 想了解的内容 | 对应文件 |
| --- | --- |
| 推荐业务顺序 | [service.go](services/api/internal/decisions/service.go) |
| 候选与 Memory 从哪里来、如何限制权限 | [context_builder.go](services/api/internal/decisions/context_builder.go) |
| 单步实现：会话、三态评估、推荐生成与历史构建 | [generation_steps.go](services/api/internal/decisions/generation_steps.go) |
| 采纳、拒绝和换批 | [feedback_service.go](services/api/internal/decisions/feedback_service.go) |
| 内存复制、锁、版本检查与命令保存 | [store.go](services/api/internal/decisions/store.go) |

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

已有实体规则、Mock Workflow、CLI 和失败恢复测试；`go test -race ./...` 可在 `services/api` 内检查并发访问。真实模型、HTTP 权限和通用语义约束验证仍待实现。

## 项目文档

- [文档索引](docs/README.md)
- [产品方案](docs/产品方案-V1.0.md)
- [领域模型与决策契约](docs/领域模型与决策契约.md)
- [技术方案](docs/技术方案-V1.0.md)
- [开发任务清单](docs/开发任务清单-V1.0.md)
