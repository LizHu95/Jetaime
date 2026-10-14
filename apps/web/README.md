# Jetaime Web

React + TypeScript + Vite 的独立 Web 客户端，手机与 PC 共用响应式页面。基础交互使用 shadcn/ui（Radix），表单使用 React Hook Form + Zod，接口状态使用 TanStack Query。

## 开发

需要 Node.js 22.12+ 或支持当前 Vite 的更新版本；仓库包含 npm lockfile。

```sh
cd apps/web
npm ci
npm run dev
```

打开 <http://127.0.0.1:5173/page/auth/login/> 或 <http://127.0.0.1:5173/page/auth/register/>。在仓库根目录也可使用 `make web`。

真实注册、登录需要另一终端启动已有 Go Auth 服务：

```sh
# 在仓库根目录，先按 docs/v2/认证接口.md 配置 PostgreSQL 并迁移
make dev
```

Vite 将 `/api` 转发到 `http://127.0.0.1:8081`，浏览器同源访问，无需改变后端 CORS。需要使用其他开发服务时，将 `.env.example` 复制为 `.env.local` 后调整 `API_PROXY_TARGET`。任何 `VITE_*` 变量都会暴露给浏览器，不能放密钥。

## 当前范围

- `/page/auth/login/`：登录，显示/隐藏密码，错误反馈与防重复提交。
- `/page/auth/register/`：账号、可选昵称、密码及确认密码；注册成功跳回登录并预填账号，不自动登录。
- `/page/auth/session/`：登录结果与退出当前设备；未登录时返回登录页。
- `/page/`：根据本标签页的会话进入登录页或会话页。

账号大小写敏感；注册密码 6–128 个 Unicode 字符，原样提交、不去除空格；昵称最多 24 个 Unicode 字符；确认密码仅在前端校验，不发送给严格限制字段的后端。

Token 和用户信息保存在标签页 `sessionStorage`，刷新可恢复，关闭标签页后不主动持久化。密码不写入浏览器存储。会话过期时清理本地数据；退出请求 204 或 401 后清理会话，网络/服务器错误则保留会话并允许重试。会话页显示的是登录响应中的账号信息，后端尚无 `/me` 接口；该页面不能替代后端业务鉴权。当前 Assistant 调试接口仍使用模拟身份，未接入这个登录会话。

## 目录边界

```text
src/
  app/                 全局 Provider、路由
  components/ui/       shadcn/ui 基础组件源码
  features/auth/       页面、表单契约、认证 API、会话
  lib/                 HTTP 错误处理与样式工具
  styles.css           主题、页面样式、响应式规则
tests/
  unit/                Unicode、API 契约、会话测试
  e2e/                 浏览器认证流程与响应式检查
```

按业务域扩展 `features/`。后续 AI 问答采用 AI SDK UI（`@ai-sdk/react`）及其 SSE 流协议，在聊天功能落地时加入依赖与 Go 适配；普通接口数据仍由 TanStack Query 管理。

## 检查与构建

```sh
npm test
npm run test:e2e
npm run build
```

浏览器测试默认使用本机 Google Chrome；缺少 Chrome 时可安装 Playwright Chromium 并修改 `playwright.config.ts` 的 channel。默认 E2E 用受控响应验证前端行为；`auth.live.spec.ts` 仅在 `WEB_LIVE_AUTH=1` 时对真实后端执行，必须使用独立测试数据库或 schema。

生产文件输出到仓库根目录 `outputs/web/`；浏览器报告和截图位于 `outputs/web-check/`，均已被根 `.gitignore` 忽略。`make web-check` 执行单元测试、浏览器测试和生产构建。

## 部署约定

页面统一 `/page/`，接口统一 `/api/`。生产服务器须将 `/page/*` 页面路由回退到构建的 `index.html`，并将 `/api/*` 反向代理到启用了 `-auth` 的 Go 服务。不能使用 Vite preview 的开发代理代替生产配置。

例如将 `outputs/web/` 的内容放到 `/srv/jetaime/page/`：

```nginx
server {
    # 配合实际域名和 HTTPS 配置使用
    root /srv/jetaime;
    location /page/ {
        try_files $uri $uri/ /page/index.html;
    }
    location /api/ {
        proxy_pass http://127.0.0.1:8081;
    }
}
```

前端路由守卫用于页面导航；业务权限必须由后端校验 Bearer Token。
