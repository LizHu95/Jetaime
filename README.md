# 今天干嘛

面向个人与情侣的 AI 生活决策助手。V1 为微信小程序，以笔记收藏、空间共享、长期偏好、候选内 Top 3 和反馈形成闭环。当前已有 Go 模块、实体类型、首轮规则与行为测试；尚未实现决策 Workflow 或模型调用。

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
    │   └── decisions/     决策流程、会话、历史与反馈
    └── db/
        ├── migrations/    数据库迁移
        └── queries/       SQL 查询
contracts/                OpenAPI 接口合同
deploy/                   部署配置
docs/                     产品、技术方案与交互原型
```

空目录使用 `.gitkeep` 保留，添加实际文件后可删除对应占位文件。

以上为当前实际目录。CLI 与 HTTP 将共用决策服务；配置组装、HTTP 适配、数据库和模型适配目录在实现对应能力时增加。图片模块留待 P1，V1 不设置独立执行计划模块。notes、memory、space、decisions 已有实体类型与规则，其他目录仍为占位，尚无 Workflow 实现。

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

已有首轮规则行为测试；真实模型、HTTP 权限和语义约束验证仍待实现。

## 项目文档

- [文档索引](docs/README.md)
- [产品方案](docs/产品方案-V1.0.md)
- [领域模型与决策契约](docs/领域模型与决策契约.md)
- [技术方案](docs/技术方案-V1.0.md)
- [开发任务清单](docs/开发任务清单-V1.0.md)
