# 统一 API 打包

在仓库根目录运行：

```sh
make api-build
# 等效：bash deploy/api/build.sh
```

构建源为 `services/api/cmd/api`，产物保存在 `outputs/api/`：

- `api-linux-amd64`：Linux x86_64。
- `api-linux-arm64`：Linux ARM64。
- `api-deploy.tar.gz`：以上程序、模型配置示例、本说明与 SHA-256 校验清单。

页面静态资源已嵌入二进制，无需单独部署前端。包内不包含实际密钥、模型配置或聊天数据。生成的二进制、配置副本、校验清单和压缩包均被 Git 忽略。

解压后根据服务器架构选择程序：

```sh
tar -xzf api-deploy.tar.gz
cd api
sha256sum -c SHA256SUMS
chmod +x api-linux-amd64
```

只测试 HTTP 接口，直接运行即可，无需模型配置或历史文件：

```sh
./api-linux-amd64
# 在另一个终端测试：
curl http://127.0.0.1:8081/test/healthz
```

默认只启用 `/test/healthz`。Assistant 模块需显式用 `-assistant` 开启。

PostgreSQL 通过 `-database` 开启；连接地址从 `JETAIME_DATABASE_URL` 环境变量读取。二进制不自动加载 `.env`。先创建空数据库和有建表权限的账号，再执行一次迁移：

```sh
export JETAIME_DATABASE_URL='postgres://jetaime:replace-me@127.0.0.1:5432/jetaime?sslmode=disable'
./api-linux-amd64 -migrate
bash start.sh start -database
curl http://127.0.0.1:8081/test/healthz
```

示例为本机连接；远端连接按数据库配置使用 TLS。迁移 SQL 已嵌入程序，执行器使用事务和数据库锁避免重复执行，并在 `schema_migrations` 保存版本、文件名和 SHA-256。重复执行无变化，已有版本校验不一致或数据库版本超出程序支持范围时拒绝执行。迁移失败会回滚本次 DDL 和版本记录。不提供删除数据的 down 命令。

`-migrate` 执行后退出，不启动 HTTP 服务，也不调用模型；需直接运行二进制，不能通过后台 `start` 脚本运行迁移。`-database` 启动只检查连接和版本，不自动建表。未迁移或版本不匹配时启动失败。曾用 psql 手工建表但未记录版本的库不能自动接管，应使用新空库，保留旧库供核对或另行制定迁移方案。

`/test/healthz` 固定返回测试 JSON；数据库模式只在启动时检查连接和迁移版本。Router 已移除存活、就绪接口，后续按实际业务需要通过模块注册。目前数据库接入完成到基础设施层；注册、登录、双人会话及消息接口尚未实现，assistant 调试台仍使用 JSON 存储。

云服务器上可用包内脚本自动选择架构并后台运行：

```sh
bash start.sh start
bash start.sh status
bash start.sh stop
tail -f runtime/api.log
```

脚本使用部署目录内的 `runtime/` 保存 PID 和服务输出，不设置开机自启。
脚本默认监听 `0.0.0.0:8081`，支持公网访问健康检查接口；阿里云安全组和服务器防火墙需放行 TCP 8081。
可用 `API_ADDR=127.0.0.1:8081 bash start.sh start` 限制本机访问。
`start` 后可追加下方的 assistant 参数；启用 assistant 时必须使用回环地址，例如 `bash start.sh start -addr 127.0.0.1:8081 -assistant ...`。模型密钥需先通过环境变量导出。

启动 assistant 时，提供实际模型配置与历史文件，并显式指定持久数据路径：

```sh
./api-linux-amd64 -assistant \
  -model-config /etc/jetaime/models.json \
  -fixture /var/lib/jetaime/history.fixture.json \
  -state /var/lib/jetaime/assistant/state.json \
  -log /var/lib/jetaime/assistant/runs.jsonl
```

实际模型配置可参考包内 `models.example.json`。凭据通过运行环境提供，二进制不会自动加载 `.env`。直接运行二进制默认监听本机回环 IP，基础 API 可用 `-addr 0.0.0.0:8081` 对外监听；启用 assistant 时仅允许本机回环监听。

页面为 `/page/assistant/`；API 使用 `/api/assistant/`。assistant 保留本机 Host 访问限制。`GET /test/healthz` 测试 HTTP 连通性。飞书仍使用独立长连接程序，此包只包含统一 HTTP API。
