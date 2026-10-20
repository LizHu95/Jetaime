# Agent 开发约定

## 路由约定

- 页面统一使用 `/page/` 前缀。
- HTTP 接口统一使用 `/api/` 前缀。
- 前缀之后按业务域组织，例如 `/page/assistant/`、`/api/assistant/...`、`/api/feishu/...`。
- 所有 HTTP 方法与路径集中在 `internal/httpapi/router.go` 的 `NewRouter(Handlers)` 注册；业务模块通过 `Endpoints()` 提供处理器，不自行注册路由。
- `internal/app/modules.go` 负责构建并注入启用的端点组；未注入的组不挂载。认证、群 ID 校验、本机与同源限制由端点组封装，不得因集中注册而丢失。
- Router 只依赖 HTTP 端点契约，不依赖业务 service/store；不添加默认首页跳转或预先占位接口。
- 后续新增或调整路由时遵循以上约定，按实际需求拓展。
- 按用户指定，`GET /test/healthz` 是上述接口前缀约定的例外，用于测试连通性，固定返回 JSON。

## 打包约定

- 统一 HTTP API 使用 `make api-build` 打包，产物保存到仓库根目录的 `outputs/api/`。
- `deploy/api/` 只保存构建脚本和说明，不保存生成产物。

## 启动约定

- `make dev` 默认启用 Auth、群聊 AI 和健康检查，需要有效模型配置，加载 `local/.env` 并连接 PostgreSQL；`API_ARGS` 用于追加或覆盖参数，例如 `-addr`、`-auth=false`。
- API 二进制默认启用认证及其 PostgreSQL 业务模块，必须配置数据库并应用迁移；仅需健康检查时显式传 `-auth=false`。
- 本地调试台通过 `-debug-console` 显式启用（兼容旧 `-assistant`），与正式群聊主 Agent 独立，只在启用时校验自身配置；`make assistant` 显式关闭认证，保留独立的本地 JSON 调试流程。

- Auth 模块默认启用，自动连接 PostgreSQL；认证业务在 `internal/auth` 按 handler、service、store 拆分职责，通过 HTTP 端点契约注入 Router。

- 命令行参数在 `internal/config` 解析为配置值；`cmd/*/main.go` 管理退出信号；`internal/app/server.go` 仅保留启动顺序，模块装配与运行资源清理分别放在 `modules.go` 和 `lifecycle.go`。
