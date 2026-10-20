# Phoenix Trace

同机部署，固定 `arize-phoenix==20.23.0`，Python venv + systemd，SQLite 数据在 `/var/lib/jetaime-phoenix`。API 的业务 PostgreSQL 与 Phoenix 数据库独立。

## 部署入口

```sh
make deploy
ssh jetaime-server 'bash api/start.sh restart'
```

`make deploy` 构建 API 和 Web，把 `phoenix/install.sh`、`phoenix.service`、`requirements.txt` 和本说明收入 `api-deploy.tar.gz` 并上传至服务器 `~/api/phoenix/`，同时应用 Nginx 反代配置。包中不包含 Python 运行时和依赖 wheel；首次安装需要服务器能访问 Python 包源，Ubuntu 缺少 venv 时会安装系统依赖。

API 的 `local/.env`（部署时同步到服务器 `~/api/.env`）：

```sh
JETAIME_PHOENIX_TRACES_ENDPOINT=http://127.0.0.1:6006/v1/traces
JETAIME_TRACE_CAPTURE_CONTENT=true
```

当上报地址为同机 `127.0.0.1:6006` 或 `localhost:6006` 时，`bash api/start.sh restart` 依次：

1. 安装或校验 Phoenix 固定版本，更新 systemd 配置并启动/重启。
2. 等待 Phoenix `/readyz` 就绪。Phoenix 启动时自动迁移自己的 SQLite。
3. 执行 API 的 PostgreSQL 增量迁移。
4. 停止旧 API 并启动新版。

Phoenix 或业务迁移失败时不重启 API。重复部署复用数据和已安装的同版本依赖；升级 Phoenix 版本前自动用 SQLite online backup 保存到 `/var/backups/jetaime-phoenix/`。部署 Phoenix 需要 root，非 root 用户需能执行 `sudo -n`。`start` 同样确保 Phoenix 就绪，但保留原来的 API 只检查、不自动迁移行为；`migrate` 只迁移业务 PostgreSQL。远程 Phoenix 地址不会触发本机安装。

## 访问

入口为站点的 `/page/phoenix/`，例如 `https://101.132.186.29/page/phoenix/`。Nginx 去掉前缀转发至 `127.0.0.1:6006`，Phoenix 设置 `PHOENIX_HOST_ROOT_PATH=/page/phoenix`，静态资源和 GraphQL 同样经过反代。

入口使用独立的 Basic Auth。首次应用 Nginx 配置时生成账号 `phoenix` 和随机密码，后续部署保留。查看凭据：

```sh
ssh jetaime-server 'sudo cat /etc/jetaime-phoenix/access.txt'
```

也可以通过 SSH 隧道访问 Nginx：

```sh
ssh -N -L 8088:127.0.0.1:80 jetaime-server
# 打开 http://127.0.0.1:8088/page/phoenix/，输入同一访问凭据
```

API 上报始终直连回环端口 `/v1/traces`，不经过 Nginx 的 Basic Auth。HTTP 6006 只监听回环；内建 gRPC 4317 的网络访问由 systemd `IPAddressDeny=any` / `IPAddressAllow=localhost` 限制为本机（需要支持 cgroup BPF 的 Linux）。

## Trace 覆盖

AI 入队、Worker 执行、聊天上下文构建、Agent 调度、主 Agent、工具调用、模型调用和结果落库均有 span。主 Agent 和工具记录的内部步骤另作为 `step.*` span 上报，包括 `build_context`、`budget`、`validate_result`、`tool_call`、`tool_result` 等；只上报实际执行/记录的步骤，不为未发生的分支生成节点。步骤保留已有测量时间和父 Trace，错误仅上报稳定类别。旧提取 workflow 的事件记录继续保留。

OpenInference 字段包含节点类型、模型名、输入输出、Token 数与 session/user。`JETAIME_TRACE_CAPTURE_CONTENT=true` 显式开启内容采集（单字段 64 KiB 上限）；`OTEL_SDK_DISABLED=true` 关闭全部上报。普通聊天和非 AI HTTP 请求不采集。远程有认证的 Phoenix 可设置 `PHOENIX_API_KEY`。

## 运维

```sh
systemctl status phoenix
journalctl -u phoenix -n 50 --no-pager
systemctl show phoenix -p MemoryCurrent
curl -fsS http://127.0.0.1:6006/readyz
```

关闭外部资源自动下载和代码沙箱，避免 WASM 下载阻塞启动。服务内存软限制 600 MiB、硬限制 800 MiB，超过硬限制后由 systemd 自动重启。依赖快照在 `/opt/jetaime-phoenix/requirements.lock`。人工备份运行中的 SQLite 也应使用 online backup。

官方文档：[子路径配置](https://arize.com/docs/phoenix/deployment/authentication)、[迁移](https://github.com/Arize-ai/phoenix/blob/main/src/phoenix/db/README.md)。
