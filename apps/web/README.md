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

Token 和用户信息保存在浏览器 `localStorage`，刷新、关闭标签页或重启浏览器后可恢复，直到后端会话过期（当前为 7 天）或主动退出。已有的标签页 `sessionStorage` 会话会自动迁移。密码不写入浏览器存储。会话过期时清理本地数据；退出请求 204 或 401 后清理会话，网络/服务器错误则保留会话并允许重试。会话页显示的是登录响应中的账号信息，后端尚无 `/me` 接口；该页面不能替代后端业务鉴权。当前 Assistant 调试接口仍使用模拟身份，未接入这个登录会话。

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

按业务域扩展 `features/`。当前群聊采用 HTTP 保存/查询消息，WebSocket 推送更新通知；普通消息和 AI 完整回答共用同一消息序列与阅读进度，TanStack Query 管理数据。

## 检查与构建

```sh
npm test
npm run test:e2e
npm run build
```

浏览器测试默认使用本机 Google Chrome；缺少 Chrome 时可安装 Playwright Chromium 并修改 `playwright.config.ts` 的 channel。默认 E2E 用受控响应验证前端行为；`auth.live.spec.ts` 仅在 `WEB_LIVE_AUTH=1` 时对真实后端执行，必须使用独立测试数据库或 schema。

生产文件输出到仓库根目录 `outputs/web/`；浏览器报告和截图位于 `outputs/web-check/`，均已被根 `.gitignore` 忽略。`make web-check` 执行单元测试、浏览器测试和生产构建。

## 部署约定

页面统一 `/page/`，接口统一 `/api/`。生产服务器须将 `/page/*` 页面路由回退到构建的 `index.html`，并将 `/api/*` 反向代理到 Go 服务；认证默认开启，启动时需提供数据库连接并完成迁移。不能使用 Vite preview 的开发代理代替生产配置。

`make deploy` 默认将 `outputs/web/` 的内容直接上传到服务器 `/var/www/jetaime/`（`index.html` 和 `assets/` 直接位于其中），可通过 `DEPLOY_WEB_DIR` 修改。Nginx 使用 `alias` 将 `/page/` 映射到此目录：

```nginx
server {
    listen 80 default_server;
    listen [::]:80 default_server;
    server_name _;

    location = / {
        return 302 /page/auth/login/;
    }
    location = /page {
        return 302 /page/;
    }
    location /page/ {
        alias /var/www/jetaime/;
        index index.html;
        add_header Cache-Control "no-cache" always;
        try_files $uri $uri/ /page/index.html;
    }
    location /api/ {
        proxy_pass http://127.0.0.1:8081;
        proxy_http_version 1.1;
        proxy_set_header Host $host;
        proxy_set_header X-Real-IP $remote_addr;
        proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
        proxy_set_header X-Forwarded-Proto $scheme;
        proxy_set_header Upgrade $http_upgrade;
        proxy_set_header Connection "upgrade";
        proxy_read_timeout 3600s;
    }
    location = /test/healthz {
        proxy_pass http://127.0.0.1:8081;
    }
    location / {
        return 404;
    }
}
```

配置源文件为 `deploy/nginx/default.conf`。`make deploy` 自动上传并替换 `/etc/nginx/sites-available/default`，检查配置后重新加载或启动 Nginx，失败时恢复原配置。自定义 `DEPLOY_WEB_DIR` 会同步调整 `alias`。仅手动修改配置时执行 `nginx -t && systemctl reload nginx`。通过 `http://服务器IP/page/auth/login/` 访问，生产环境按实际域名配置 HTTPS。

前端路由守卫用于页面导航；业务权限必须由后端校验 Bearer Token。


### 修改密码、加好友与创建群

登录成功页可进入 `/page/auth/password/` 修改密码，或 `/page/contacts/` 查找对方账号、发送/处理添加申请。接收方接受后创建固定双人群和共享 AI，双方都能在群列表查看。改密成功撤销全部登录，返回登录页；好友页每 10 秒轮询，401 会清除本地会话。

真实接口及事务语义见 [好友与群组接口](../../docs/v2/好友与群组接口.md) 和 [认证接口](../../docs/v2/认证接口.md)。文字群聊、实时同步和共享 AI 调用已接入，详见 [双人群聊接口](../../docs/v2/双人群聊接口.md)。

`python3 scripts/check-account-flow.py`（仓库根目录执行）使用 local/.env 数据库连接创建临时 schema，自动运行真实 API、桌面/手机浏览器用例并清理。测试端口为 18081（API）和 15173（Web），不占用日常开发服务端口。Playwright 可用 `WEB_PORT` 指定 Web 测试端口。


### 群聊与实时同步

登录后进入 `/page/chat/`，或从好友页点击“进入群聊”。Vite `/api` 代理启用 WebSocket。前端只在前台有焦点且消息可见时更新阅读进度，AI 回答对双方算收到的消息；重新连接后补拉数据库记录并去重。

普通聊天随 `make dev` 可用；AI 需 `make dev API_ARGS='-chat-ai'` 及有效模型配置。完整回答后统一保存和推送。联调脚本还会在 18082 端口启动受控模型验证 AI 成功/失败/重试，不消耗真实模型额度。


原 Agent 能力已接入：点击 @AI 后可选择历史问答或提取待办，回答带证据状态和来源跳转。回复的结构化结果保存到 PostgreSQL，重新打开仍可查看。需要应用迁移 000003；使用 `make dev API_ARGS='-chat-ai -model-profile local-qwen'` 调用本地 Ollama。提取结果仅为待办建议，未自动创建规划。
