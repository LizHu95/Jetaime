# Agent 开发约定

## 路由约定

- 页面统一使用 `/page/` 前缀。
- HTTP 接口统一使用 `/api/` 前缀。
- 前缀之后按业务域组织，例如 `/page/assistant/`、`/page/webchat/`、`/api/assistant/...`、`/api/feishu/...`。
- 路由由总 Router 统一装配，各业务模块负责注册自己的页面和接口。
- 后续新增或调整路由时遵循以上约定，按实际需求拓展。
