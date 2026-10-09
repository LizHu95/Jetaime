# 今天干嘛

面向个人与情侣的 AI 生活决策助手。V1 为微信小程序，以笔记收藏、空间共享、长期偏好、候选内 Top 3 和反馈形成闭环。当前已建立目录骨架、Go 模块与首轮决策实体类型；尚未实现决策 Workflow 或模型调用。

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

以上为当前实际目录。CLI 与 HTTP 将共用决策服务；配置组装、HTTP 适配、数据库和模型适配目录在实现对应能力时增加。图片模块留待 P1，V1 不设置独立执行计划模块。notes、memory、space、decisions 已定义实体类型，其他目录仍为占位，尚无 Workflow 实现。

## 服务端类型检查

在 `services/api` 执行 `go test ./...` 和 `go vet ./...`。当前没有行为测试，这些命令用于编译与静态检查。

## 项目文档

- [文档索引](docs/README.md)
- [产品方案](docs/产品方案-V1.0.md)
- [领域模型与决策契约](docs/领域模型与决策契约.md)
- [技术方案](docs/技术方案-V1.0.md)
- [开发任务清单](docs/开发任务清单-V1.0.md)
