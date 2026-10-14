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

- API 二进制和 `make dev` 默认只启动测试接口，不依赖模型配置或历史文件。
- Assistant 模块通过 `-assistant` 显式启用，只在启用时校验自身配置。
