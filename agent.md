# Agent 开发约定

## 路由约定

- 页面统一使用 `/page/` 前缀。
- HTTP 接口统一使用 `/api/` 前缀。
- 前缀之后按业务域组织，例如 `/page/assistant/`、`/api/assistant/...`、`/api/feishu/...`。
- 总 Router 只内置 `GET /test/healthz`；各业务模块通过 `NewRouter(modules ...Module)` 注入，负责注册自己的页面和接口。
- 不在 Router 中添加业务专用依赖字段、默认首页跳转或预先占位的接口；实现功能时再注册。
- 后续新增或调整路由时遵循以上约定，按实际需求拓展。
- 按用户指定，`GET /test/healthz` 是上述接口前缀约定的例外，用于测试连通性，固定返回 JSON。

## 打包约定

- 统一 HTTP API 使用 `make api-build` 打包，产物保存到仓库根目录的 `outputs/api/`。
- `deploy/api/` 只保存构建脚本和说明，不保存生成产物。

## 启动约定

- `make dev` 默认启用 Auth 和健康检查，加载 `local/.env` 并连接 PostgreSQL；`API_ARGS` 用于追加或覆盖参数，例如 `-addr`、`-auth=false`。
- API 二进制默认启用认证及其 PostgreSQL 业务模块，必须配置数据库并应用迁移；仅需健康检查时显式传 `-auth=false`。
- Assistant 模块通过 `-assistant` 显式启用，只在启用时校验自身配置；`make assistant` 显式关闭认证，保留独立的本地 JSON 调试流程。

- Auth 模块默认启用，自动连接 PostgreSQL；认证业务在 `internal/auth` 按 handler、service、store 拆分职责，不向 Router 添加业务字段。
