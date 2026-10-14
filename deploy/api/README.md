# 统一 API 打包

先在 `local/.env` 配置服务器使用的 `JETAIME_DATABASE_URL`，再在仓库根目录运行：

```sh
make api-build
# 等效：bash deploy/api/build.sh
```

构建源为 `services/api/cmd/api`，产物保存在 `outputs/api/`：

- `api-linux-amd64`：Linux x86_64。
- `api-linux-arm64`：Linux ARM64。
- `.env`：从 `local/.env` 复制的运行配置，权限为 `600`。
- `api-deploy.tar.gz`：以上程序、`.env`、模型配置示例、本说明与 SHA-256 校验清单；压缩包权限为 `600`。

Assistant 调试页面已嵌入二进制；React Web 页面需单独部署 `outputs/web/` 的构建文件。包内包含 `local/.env` 中的实际配置及密钥，不包含实际模型 JSON 配置或聊天数据。生成的二进制、配置副本、校验清单和压缩包均被 Git 忽略。

解压后根据服务器架构选择程序：

```sh
tar -xzf api-deploy.tar.gz
cd api
sha256sum -c SHA256SUMS
chmod +x api-linux-amd64
```

默认启用认证、好友和群聊 API，需要配置 PostgreSQL 并完成迁移。仅测试 HTTP 连通性时显式关闭认证，无需数据库、模型配置或历史文件：

```sh
./api-linux-amd64 -auth=false
# 在另一个终端测试：
curl http://127.0.0.1:8081/test/healthz
```

`-auth=false` 仅启用 `/test/healthz`。Assistant 模块需显式用 `-assistant` 开启；独立运行 JSON 调试台时同时传 `-auth=false`。

默认认证模块自动开启 PostgreSQL；连接地址从 `JETAIME_DATABASE_URL` 环境变量读取。`start.sh`（默认 restart）、`start.sh start` 和 `start.sh migrate` 自动加载同目录 `.env` 并导出变量，文件中的同名配置优先于现有环境变量。`.env` 使用 Bash 赋值语法，特殊字符或空格使用单引号包裹；二进制本身不自动读取该文件。先创建空数据库和有建表权限的账号，再执行一次迁移：

```sh
# 打包前在 local/.env 设置数据库连接；部署目录已有打包后的 .env
bash start.sh migrate
API_ADDR=127.0.0.1:8081 bash start.sh start
curl http://127.0.0.1:8081/test/healthz
```

示例为本机连接；远端连接按数据库配置使用 TLS。迁移 SQL 已嵌入程序，执行器使用事务和数据库锁避免重复执行，并在 `schema_migrations` 保存版本、文件名和 SHA-256。重复执行无变化，已有版本校验不一致或数据库版本超出程序支持范围时拒绝执行。迁移失败会回滚本次 DDL 和版本记录。不提供删除数据的 down 命令。

`-migrate` 执行后退出，不启动 HTTP 服务，也不调用模型；使用 `bash start.sh migrate` 加载配置并前台执行迁移，成功后再运行 `bash start.sh start`。正常启动只检查连接和版本，不自动建表。未迁移或版本不匹配时启动失败。可用 `-auth=false -database` 仅检查数据库并提供健康检查。曾用 psql 手工建表但未记录版本的库不能自动接管，应使用新空库，保留旧库供核对或另行制定迁移方案。

`/test/healthz` 固定返回测试 JSON；数据库模式只在启动时检查连接和迁移版本。Router 已移除存活、就绪接口，后续按实际业务需要通过模块注册。认证、好友请求、双人群聊、阅读进度、WebSocket 实时通知和共享 AI 已实现；assistant 调试台仍独立使用 JSON 存储。群 AI 用 `-chat-ai` 开启，需有效模型配置；代理需转发 `/api/chat/events` 的 WebSocket 升级。

认证模块默认开启，自动连接 PostgreSQL 并注册账号相关接口，包括 `/api/auth/register`、`/api/auth/login`、`/api/auth/logout`。运行 `./api-linux-amd64` 或 `bash start.sh start` 即可，无需加 `-auth`；显式 `-auth=false` 可关闭。登录返回 7 天有效期的 Bearer Token，退出在请求头传 `Authorization: Bearer TOKEN`。认证应通过 HTTPS 使用。注册密码为 6–128 个 Unicode 字符，账号为 2–24 位 ASCII 字母、数字或下划线，大小写敏感；升级时先执行所有待应用迁移并重启服务。完整请求与响应示例见仓库 `docs/v2/认证接口.md`。

云服务器上可用包内脚本自动选择架构并后台运行。直接执行 `bash start.sh` 默认重启：先停止当前部署目录的旧 API，再后台启动；即使 PID 文件丢失，也会检查目标端口并识别旧 API（包含已被替换的二进制）。其他程序占用端口或无法确认进程身份时会退出并显示占用信息。端口检查依赖 Linux 的 `ss`（iproute2）。显式 `start` 保留已运行则直接返回的行为。

```sh
bash start.sh          # 默认重启，未运行时直接启动
bash start.sh restart  # 显式重启
```

其他命令：

```sh
API_ADDR=127.0.0.1:8081 bash start.sh start
bash start.sh status
bash start.sh stop
tail -f runtime/api.log
```

脚本使用部署目录内的 `runtime/` 保存 PID 和服务输出，不设置开机自启。
脚本默认监听 `0.0.0.0:8081`；与 Nginx 同机部署时按上面的示例设置 `API_ADDR=127.0.0.1:8081`，通过 Nginx 转发 `/api/` 请求。
`start` 后可追加下方的 assistant 参数；启用 assistant 时必须使用回环地址，例如 `bash start.sh start -addr 127.0.0.1:8081 -assistant ...`。模型密钥可放在打包的 `.env` 中。

启动 assistant 时，提供实际模型配置与历史文件，并显式指定持久数据路径：

```sh
./api-linux-amd64 -auth=false -assistant \
  -model-config /etc/jetaime/models.json \
  -fixture /var/lib/jetaime/history.fixture.json \
  -state /var/lib/jetaime/assistant/state.json \
  -log /var/lib/jetaime/assistant/runs.jsonl
```

实际模型配置可参考包内 `models.example.json`。`start.sh` 自动加载 `.env`；直接运行二进制时需自行导出环境变量。直接运行二进制默认监听本机回环 IP，基础 API 可用 `-addr 0.0.0.0:8081` 对外监听；启用 assistant 时仅允许本机回环监听。

页面为 `/page/assistant/`；API 使用 `/api/assistant/`。assistant 保留本机 Host 访问限制。`GET /test/healthz` 测试 HTTP 连通性。飞书仍使用独立长连接程序，此包只包含统一 HTTP API。
