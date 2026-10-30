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

普通 `make api-build` 保留双架构压缩包，适用于离线分发；程序使用 `-trimpath -ldflags='-s -w'` 构建以移除调试符号。也可只构建指定架构、不生成压缩包：

```sh
make api-build API_BUILD_ARGS='--arch amd64 --no-archive'
# --arch 可选 amd64、arm64、all，默认 all。
```

每次构建重新生成 `SHA256SUMS`，只包含本次实际产物，并清除输出目录中不再构建的架构程序或压缩包。

`make deploy` 先通过 SSH 识别服务器架构，只构建对应 Linux 程序，跳过压缩包生成。API 按校验清单通过 rsync 目录同步，开启传输压缩、增量传输、进度/统计输出和断点文件保留；Web 同样使用 rsync，同步变化文件且保留已有静态资源。两端需安装 rsync，服务器另需 flock、sha256sum。依赖缺失或架构不支持时在构建前退出。

API 上传缓存为部署父目录下的 `.jetaime-api-upload-amd64/api/` 或 `.jetaime-api-upload-arm64/api/`，目录权限 `700`。缓存成功部署后仍保留，重复部署或中断后重试可复用；通常无须手动清理。上传和安装共用缓存锁，安装前同时核对本次清单的摘要与每个文件的 SHA-256，检测到其他部署已改写缓存时退出。仅复制清单中的文件到独立临时安装目录，校验及预检通过后再重命名替换程序；不会直接同步到运行中的二进制。缓存中的过期可选配置和断点文件不参与安装。上传失败时保留缓存，预检失败时保留现有程序和配置。

Assistant 调试页面已嵌入二进制；React Web 页面需单独部署 `outputs/web/` 的构建文件。包内包含 `local/.env` 中的实际配置及密钥，若存在 `local/models.json`，会一并打包为 `models.json`；不包含聊天数据。生成的二进制、配置副本、校验清单和压缩包均被 Git 忽略。

解压后根据服务器架构选择程序：

```sh
tar -xzf api-deploy.tar.gz
cd api
sha256sum -c SHA256SUMS
chmod +x api-linux-amd64
```

默认启用认证、好友、群聊 API 和共享 AI，需要配置 PostgreSQL、完成迁移并提供有效模型配置。仅测试 HTTP 连通性时显式关闭认证，无需数据库、模型配置或历史文件：

```sh
./api-linux-amd64 -chat-ai=false
# 在另一个终端测试：
curl http://127.0.0.1:8081/test/healthz
```

`-chat-ai=false` 仅关闭群聊 Agent 回复，账号、聊天、数据库和记忆处理仍启用，需要数据库与记忆模型配置。旧 JSON 调试台已退役，不再提供相关参数和路由。

默认认证模块自动开启 PostgreSQL；连接地址从 `JETAIME_DATABASE_URL` 环境变量读取。`start.sh`（默认 restart）、`start.sh start` 和 `start.sh migrate` 自动加载同目录 `.env` 并导出变量，文件中的同名配置优先于现有环境变量。`.env` 使用 Bash 赋值语法，特殊字符或空格使用单引号包裹；二进制本身不自动读取该文件。先创建空数据库和有建表权限的账号，再执行自动迁移与启动：

```sh
# 打包前在 local/.env 设置数据库连接；部署目录已有打包后的 .env
API_ADDR=127.0.0.1:8081 bash start.sh restart
curl http://127.0.0.1:8081/test/healthz
```

示例为本机连接；远端连接按数据库配置使用 TLS。迁移 SQL 已嵌入程序，执行器使用事务和数据库锁避免重复执行，并在 `schema_migrations` 保存版本、文件名和 SHA-256。重复执行无变化，已有版本校验不一致或数据库版本超出程序支持范围时拒绝执行。迁移失败会回滚本次 DDL 和版本记录。不提供删除数据的 down 命令。

`-migrate` 执行后退出，不启动 HTTP 服务，也不调用模型；使用 `bash start.sh migrate` 加载配置并前台执行迁移，成功后再运行 `bash start.sh start`。`restart`（含默认动作）会先执行增量迁移，成功后再停止旧进程并启动新 API；迁移失败时退出并保留旧进程。显式 `start` 或直接运行二进制只检查连接和版本，不自动建表，未迁移或版本不匹配时启动失败。核心业务和数据库固定启用，`-auth`、`-database` 参数已移除。曾用 psql 手工建表但未记录版本的库不能自动接管，应使用新空库，保留旧库供核对或另行制定迁移方案。

`/test/healthz` 固定返回测试 JSON；数据库模式只在启动时检查连接和迁移版本。Router 已移除存活、就绪接口，后续按实际业务需要通过模块注册。认证、好友请求、双人群聊、阅读进度、WebSocket 实时通知和共享 AI 已实现。群 AI 默认开启，需有效模型配置，可用 `-chat-ai=false` 显式关闭；代理需转发 `/api/chat/events` 的 WebSocket 升级。

共享 AI 的部署配置：在 `local/models.json` 选择可用的云模型 profile，并在 `local/.env` 配置对应密钥，再构建上传。群聊 AI 默认开启，无需启用环境变量或启动参数。`start.sh` 自动读取同目录 `models.json`（可用 `JETAIME_MODEL_CONFIG` 指定其他路径），普通重启默认开启 AI。服务器上已有部署可补充这两个文件后运行 `bash start.sh restart`。启动日志应出现 `Group AI: ...`。显式参数优先，例如 `bash start.sh restart -chat-ai=false` 可临时关闭；不再提供仅健康检查的启动模式。启用后，聊天里失败的旧请求可点击“重试 AI 回复”，不会自动重新执行。

认证模块固定开启，启动时连接 PostgreSQL 并注册账号相关接口，包括 `/api/auth/register`、`/api/auth/login`、`/api/auth/logout`。运行 `./api-linux-amd64` 或 `bash start.sh start` 即可；不再提供关闭账号模块的参数。登录返回 7 天有效期的 Bearer Token，退出在请求头传 `Authorization: Bearer TOKEN`。认证应通过 HTTPS 使用。注册密码为 6–128 个 Unicode 字符，账号为 2–24 位 ASCII 字母、数字或下划线，大小写敏感；升级时运行 `bash start.sh restart` 自动执行所有待应用迁移并重启服务。完整请求与响应示例见仓库 `docs/v2/认证接口.md`。

云服务器上可用包内脚本自动选择架构并后台运行。直接执行 `bash start.sh` 默认重启：先应用待执行的数据库迁移，成功后停止当前部署目录的旧 API，再后台启动；即使 PID 文件丢失，也会检查目标端口并识别旧 API（包含已被替换的二进制）。其他程序占用端口或无法确认进程身份时会退出并显示占用信息。端口检查依赖 Linux 的 `ss`（iproute2）。显式 `start` 保留已运行则直接返回的行为。

```sh
bash start.sh          # 默认增量迁移后重启，未运行时启动
bash start.sh restart  # 显式增量迁移后重启
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
`start` 后可追加 API 参数。实际模型配置可参考包内 `models.example.json`，模型密钥可放在打包的 `.env` 中。`start.sh` 自动加载 `.env`；直接运行二进制时需自行导出环境变量。直接运行二进制默认监听本机回环 IP，可用 `-addr 0.0.0.0:8081` 对外监听。

`GET /test/healthz` 测试 HTTP 连通性。飞书仍使用独立长连接程序，此包只包含统一 HTTP API。

## Trace 上报

通过 OpenTelemetry 直接向 Phoenix 上报 AI 入队、Worker、Workflow 和模型调用。普通聊天和其他 HTTP 请求不上报。配置见 [Phoenix 接入说明](../phoenix/README.md)，Phoenix 使用独立 SQLite 存储。保留业务迁移 `000005_agent_trace_context.up.sql` 以关联持久化 AI 队列；更新二进制后执行 `bash start.sh restart`，自动增量迁移并重启。

## 历史向量化与增量记忆

配置模板为 `services/api/config/memory.example.json`，Embedding 为 `qwen3.7-text-embedding-flash`（1024 维），extraction 默认使用 `qwen3.7-flash`，主 Agent 使用独立的聊天模型配置；两者可以使用不同模型，切换聊天模型不要求同步修改 extraction。模板的区域 URL 仍占位。正式 API 启动时自动启动记忆与向量 Worker，没有 enabled 配置开关（旧配置需删除该字段）。构建要求 `local/memory.json` 存在并一并打包；`start.sh` 自动加载部署目录的 `memory.json`，可用 `JETAIME_MEMORY_CONFIG` 指定路径。配置文件不承载游标、向量或聊天数据，这些状态全部保存在 PostgreSQL。

新增 `000008_memory.up.sql` 需要数据库主机提供 pgvector，并在 `public` schema 安装扩展（迁移账号无权限时由 DBA 预先安装）。更新后执行 `bash start.sh restart`，自动增量迁移并重启。模型配置和密钥有效时，启动自动继续历史回填与增量处理；缺失配置或密钥则启动失败，不静默跳过。仅健康检查与迁移模式不运行记忆 Worker。

可前台执行 `bash start.sh memory-backfill` 继续处理尚未完成的消息；重复运行不清空数据库、不重置水位，不重复计算已完成向量。普通部署和重启同样复用持久化结果。模型调用成功但保存前崩溃的步骤可能重试，不承诺外部调用严格一次。完整配置、内部检索方法、权限、恢复机制见 `docs/v2/历史向量化与增量记忆.md`。

正式群聊的自动问答由 main 选择 `answer_question`，再由 QA 生成并校验回答，通常调用两次对话模型。历史与混合问题还会进行查询向量化和原文回查，所有检索受原提问 seq 截止限制。工具为进程内调用，不新增 HTTP 接口；本次工具收敛不需要新增迁移或重新向量化已有数据。

同机部署 Phoenix 收集 Trace，见 [Phoenix 部署说明](../phoenix/README.md)。

## Phoenix 随包部署

`make deploy` 同时打包并上传 `api/phoenix/` 部署定义，应用 `/page/phoenix/` 的 Nginx 反代和独立访问认证。配置同机 `JETAIME_PHOENIX_TRACES_ENDPOINT=http://127.0.0.1:6006/v1/traces` 后，`bash api/start.sh restart` 会先安装/启动 Phoenix，等待其数据库迁移和就绪，再迁移业务 PostgreSQL 并重启 API。首次安装需要包源网络及 root / `sudo -n`；已安装同版本不会重复下载依赖。访问账号和详细步骤见 [Phoenix 说明](../phoenix/README.md)。

## 部署时的记忆版本预检

`make deploy` 在服务器的临时上传目录执行新程序的数据库迁移和记忆版本检查，通过后才替换已安装的 API 与配置，再更新 Web/Nginx。若记忆版本不一致，部署失败会保留旧程序、配置及运行中的进程；数据库增量迁移可能已执行。请不要手动删除游标或修改 pipeline 标记来绕过保护。

可运行 `bash start.sh check` 单独检查部署目录里的记忆配置和目标数据库是否兼容；也可直接执行 `./api-linux-amd64 -memory-check -memory-config /path/to/memory.json`。检查验证模型配置、所需密钥环境变量、数据库 schema 和记忆 pipeline，不发起模型推理、不创建或重置记忆进度。它不验证模型服务权限或输出质量。`restart` 在停止旧进程前也会执行同样的检查。

如果仅更换聊天模型，保持 `memory.json` 不变；如果更换 extraction、Embedding 或其他影响记忆 pipeline 的配置，应先在独立副本中显式重建并验证，再切换生产索引与配置。普通部署不自动清空或重建记忆。

### 记忆关键词检索升级

执行 `000018_memory_search.up.sql` 前，数据库主机必须安装 ParadeDB `pg_search >= 0.26.0`，并继续提供 pgvector；迁移账号需有启用扩展权限，或由管理员预先启用。普通 PostgreSQL / 仅含 pgvector 的安装不能直接执行该迁移。新关键词路使用 Jieba 中文分词与 BM25，原有向量路继续使用 pgvector。

通过现有迁移命令创建索引后再启动新程序；迁移失败不会写入该版本记录。已有记忆会自动建立搜索索引，无需重新调用模型。索引创建在事务内执行，会阻塞记忆表写入；较大数据库应安排维护窗口并预留磁盘空间。

部署脚本分别报告数据库迁移失败与记忆配置检查失败。缺少 `pg_search` 时，会提示先在实际数据库主机/容器安装匹配扩展，不要求恢复记忆配置。`start.sh migrate` 与 `restart` 共用此诊断。脚本不自动修改数据库主机的软件包或替换数据库容器；API 主机可能连接远程数据库。失败时保留旧进程与部署文件，但已成功提交的迁移不会因后续预检失败自动回滚。

### 部署代码信息

打包脚本自动将当前 Git 信息写入部署包 `.env`（不修改 `local/.env`）：

- `JETAIME_DEPLOY_BRANCH`：分支名，分离 HEAD 时为 `detached`。
- `JETAIME_DEPLOY_COMMIT`：完整提交哈希。
- `JETAIME_DEPLOY_DIRTY`：打包时是否包含未提交修改（`true` / `false`）。

登录后在“我 → 开发调试”查看运行中 API 的部署信息。未配置时显示“未记录”。
Trace 的 Resource 属性同步包含 `deployment.branch`、`deployment.commit`、`deployment.dirty`，并以提交哈希设置 `service.version`。只上报上述明确字段，不上报其他环境变量。
