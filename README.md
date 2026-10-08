# 今天干嘛

面向两个人的 AI 生活决策助手。当前已建立目录骨架，尚未初始化前后端依赖或实现业务代码。

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
    ├── cmd/api/           Go 服务入口
    ├── internal/
    │   ├── auth/          登录与会话
    │   ├── space/         双人空间与邀请
    │   ├── ideas/         收藏灵感
    │   ├── assets/        图片资源
    │   ├── decisions/     AI 整理与推荐
    │   └── plans/         已保存方案与完成反馈
    └── db/
        ├── migrations/    数据库迁移
        └── queries/       SQL 查询
contracts/                OpenAPI 接口合同
deploy/                   部署配置
docs/                     产品、技术方案与交互原型
```

空目录使用 `.gitkeep` 保留，添加实际文件后可删除对应占位文件。

## 方案与原型

- [产品方案 V0.3](docs/产品方案-V0.3.md)
- [技术选型方案 V0.1](docs/技术选型方案-V0.1.md)
- [交互原型](docs/V0-demo.html)
