# Langfuse Cloud Trace

Go API 使用 OpenTelemetry，经 OTLP/HTTP 直接向 Langfuse Cloud 上报 AI 调用链。无需部署 Trace 服务、Collector 或 Trace 数据库；业务 PostgreSQL 继续保存消息和任务。

## 配置和部署

在 Langfuse Cloud 注册免费账号，创建项目，在项目设置中生成 Public Key / Secret Key。把本目录 `.env.example` 中的配置添加到仓库 `local/.env`：

```dotenv
LANGFUSE_BASE_URL=https://cloud.langfuse.com
LANGFUSE_PUBLIC_KEY=pk-lf-REPLACE_ME
LANGFUSE_SECRET_KEY=sk-lf-REPLACE_ME
JETAIME_TRACE_CAPTURE_CONTENT=true
```

项目与域名必须属于同一数据区域：EU 使用 `https://cloud.langfuse.com`，US 使用 `https://us.cloud.langfuse.com`，日本使用 `https://jp.cloud.langfuse.com`。配置基础地址即可，代码自动附加 `/api/public/otel/v1/traces`，使用项目密钥生成 Basic 认证，并设置 v4 ingestion header。

密钥只放后端环境文件，不添加到 Web 前端。两个密钥均未配置时不上报；只配置一个或地址非法时启动报错。`JETAIME_TRACE_CAPTURE_CONTENT=false` 时只记录模型名、Token、耗时、错误及关联 ID；`true` 时包含模型输入和输出，每项最多 64 KiB，超出后标记截断。原始供应商错误响应和模型 API Key 不上报。

本地运行 `make dev`，或用 `make assistant` 调试。部署运行 `make deploy`，它会将 `local/.env` 打包上传。服务器更新后执行：

```sh
cd ~/api
bash start.sh restart
```

`000005_agent_trace_context.up.sql` 保留用于 AI 队列的 Trace 上下文与排队时间，不是 Trace 存储库。Phoenix 部署和启动入口已删除，旧 `PHOENIX_SQL_DATABASE_URL`、`JETAIME_TRACE_PROJECT`、`OTEL_EXPORTER_OTLP_ENDPOINT` / `OTEL_EXPORTER_OTLP_TRACES_ENDPOINT` 配置不再用于选择上报平台，可从环境文件移除。旧私有数据库文件与评测结果不会删除。

## 查看和额度

登录配置区域的 Langfuse Cloud，打开对应项目的 Traces。在群聊发送 `@AI` 并等待回复，可查看：

```text
chat.ai
└── chat.ai.execute
    ├── chat.build_context
    ├── assistant.main / assistant.main.route / assistant.child.*（自动路由时）
    ├── assistant.workflow
    │   └── llm.call（generation）
    └── chat.save_reply
```

实际层级由选中的路由决定。模型调用显示输入输出、模型名和 input/output Token。按 `session.id` 关联群聊；按 observation metadata 的 `chat_run_id`、`chat_trigger_message_id` 定位任务。`queue_wait_ms` 是入队至执行的等待时间，租约恢复时包含此前尝试。API 日志包含 `run` 与 `trace_id`。

只采集 AI 入队、AI 重试、Worker、Workflow 和模型调用；普通聊天、登录、轮询、WebSocket、健康检查及其他 HTTP 请求不创建 Trace。队列持久化 W3C 上下文，Worker 重启/租约恢复继续关联原 Trace。手动重试关联新的重试 Trace。

上报为有界队列批量异步发送，平台故障不阻塞业务，积压或进程异常退出可能丢失 Trace。通常约每秒发送，退出时最多等待 5 秒刷出。错误日志会提示检查密钥、区域和连通性。使用 `OTEL_SDK_DISABLED=true` 并重启可暂停上报；采样通过标准 OTel 采样配置按完整 Trace 执行。

在 Langfuse 的 Dashboards → Langfuse Usage Management 中观察实际用量。一次 AI 请求的 Trace 与每个 observation 均会消耗 units，自动路由、多轮核对和重试会增加消耗。离线 `make eval` 只生成本地评测报告，不上传到 Langfuse，不消耗其额度。

参考：[Langfuse OTLP 接入及字段映射](https://langfuse.com/integrations/native/opentelemetry)。
