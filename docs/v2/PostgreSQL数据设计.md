# PostgreSQL 数据设计

更新：2026-10-14。状态：第一版设计、建表 SQL、Go 连接池与版本迁移已实现；已接入 API 的可选数据库模式，账号认证接口已接入；聊天业务接口及线上部署尚未完成。

## 1. 已确认规则与建议

已确认：会话固定两位真人、一个共享 AI；成立后不能新增成员；任一人退出则整个会话消失；首版 @AI 触发；采用 PostgreSQL。

设计建议：通过添加请求和对方接受建立会话；同一用户对最多一个活跃会话；关闭后重新添加产生新会话，不继承旧历史。数据库采用逻辑关闭，停止双方访问，物理删除与备份保留期限另行决定。没有独立好友关系表，活跃双人会话就是当前联系关系。

## 2. 表与实体

| 表 | 主要字段 | 约束与职责 |
| --- | --- | --- |
| users | id、nickname、avatar_url、created_at | 用户 ID 稳定，昵称可修改 |
| user_credentials | user_id、username、password_hash、updated_at | 每人一个密码凭据，账号大小写敏感且唯一；注册/登录先去除两端空白，账号不随昵称变化 |
| auth_sessions | id、user_id、token_hash、expires_at、revoked_at | 多设备分别创建会话；保存随机 Token 的 SHA-256，不保存原始 Token；建议有效期 7 天 |
| chat_groups | id、user_low_id、user_high_id、status、last_message_seq、last_message_at、closed_at、closed_by | 两位用户按 UUID 排序；两人不同且固定；仅 active 用户对唯一；无群主特权 |
| group_members | group_id、user_id、last_read_seq、joined_at | 两条固定成员记录；管理读取进度与权限；退出关闭整个会话，不删除其中一条成员 |
| group_assistants | id、group_id、name、agent_key、enabled | 每会话一个 AI；agent_key 指向服务端配置，密钥不存此表、不下发客户端 |
| contact_requests | id、from_user_id、to_user_id、status、group_id、resolved_at | 建议新增的请求实体；同一用户对最多一个待处理请求，不分方向；接受后关联同一用户对的会话 |
| messages | id、group_id、seq、sender_type、sender_user_id、sender_assistant_id、content、mentions_ai、client_message_id、reply_to_id | 真人或 AI 两种发送者；复合外键限制为本会话成员/助手；回复关联只能指向同会话消息 |
| agent_runs | id、group_id、assistant_id、trigger_message_id、requested_by、context_end_seq、status、attempt_count、lease_expires_at、response_message_id、error_code | 每条 @AI 消息最多一个调用记录；触发者必须是该真人消息发送者；回复必须来自本会话的同一个助手 |

UUID 由 Go 生成，所有时间使用 timestamptz，接口输出 RFC3339，客户端按本地时区显示。消息序号使用 bigint，API 建议把序号编码为十进制字符串，兼顾现有 Web 的整数精度。

首版内容为纯文字。系统事件暂不写为第三种消息类型；待办 Task 与引用表在相关功能接入时补充。模型响应可包含来源 ID，但写入或展示前仍须校验当前会话和权限。

## 3. 数据库与服务端分别保证什么

数据库负责：账号唯一、固定两位合法成员、创建事务提交时恰好两条成员和一个助手、唯一活跃用户对、唯一待处理请求、真人消息去重、消息与 AI 上下文的会话关联、关闭不可恢复、关闭后拒绝插入消息和调用。

固定双人使用 chat_groups 的两位用户字段约束身份；group_members 触发器拒绝第三人、删除成员或变更身份，延迟约束触发器允许创建事务内先插会话再补齐成员和助手。

服务端负责：验证登录和操作人；查询仅返回 active 且本人所属会话；消息内容和请求体大小限制；所有消息只追加；分配连续递增序号；添加请求状态流转；关闭时停用 AI 和取消调用；工作进程重试及过期租约恢复。外键能验证“某用户是成员”，不能验证“HTTP 调用者就是该用户”。

AuthSession 每次认证均检查 expires_at 与 revoked_at；过期不依赖清理任务。密码验证已使用 Argon2id；注册、登录及退出接口见 [认证接口说明](认证接口.md)。数据库不存模型密钥、原始会话 Token 或用户明文密码。

## 4. 必须使用的事务流程

### 接受添加请求

仅接收方可接受，发送方可取消，接收方可拒绝。锁定 contact_requests 行；重复接受已 accepted 请求返回原会话（若已关闭返回关闭状态，不重新建立）。对 pending 请求：确认无活跃会话，创建 chat_groups、两条 group_members 和一个 group_assistants，再将请求更新 accepted 并设置 group_id、resolved_at，整体提交。

双方同时添加由待处理用户对唯一索引仲裁；已有反向请求时返回待处理请求，不隐式替对方接受。创建请求与关闭旧会话的竞态通过接受时再校验和活跃用户对唯一索引处理。

### 发送消息

1. 从登录会话获取 user_id，不相信客户端传入的发送者 ID。
2. 锁定 chat_groups 行（FOR UPDATE），校验 active 和本人为成员。
3. 按 sender_user_id + client_message_id 查重：相同会话、内容、回复目标和 mentions_ai 返回已有结果；相同 ID 不同请求返回冲突，不产生新消息或调用。
4. 更新 last_message_seq = last_message_seq + 1，RETURNING 得到 seq；插入真人消息并更新 last_message_at。
5. mentions_ai 为 true 时，在同一事务创建 queued AgentRun，并把 context_end_seq 固定为该消息 seq。主动参与另由群配置与数据库 5 秒等待时间触发，详见[群级主动 Agent](双人群聊接口.md#群级主动-agent)。
6. 提交后再通知客户端/唤醒工作进程。轮询或游标补拉负责恢复通知丢失，消息以数据库提交结果为准。

客户端通过 @AI 按钮写入结构化 mentions_ai 字段；不只通过字符串匹配识别。模型调用不占用上述事务或数据库锁。

### 执行 Agent

先锁会话，再锁调用行，校验会话 active、助手 enabled。原子认领 queued 调用，或恢复租约已过期的 running 调用；设置 running、started_at、lease_expires_at，attempt_count 加一。提交后读取本群 seq <= context_end_seq 的历史并调用模型，不混入后来的消息或其他会话。

调用返回后重新锁会话，再锁调用行；检查会话 active、助手 enabled、调用仍 running、attempt_count 等于本次认领值且租约有效。通过后分配新消息序号，插入助手消息（reply_to_id 为触发消息），更新调用为 succeeded 并关联 response_message_id，整体提交。旧工作进程、关闭会话和重复结果不得写入回复。

长调用需要续租，续租同样匹配 attempt_count。失败更新 failed 并记录安全的 error_code；显式重试复用同一个 AgentRun，变回 queued，清除本次错误、时间和租约。成功或 cancelled 不允许重试。工作进程定期扫描 queued 与过期租约，服务重启后可以恢复；首版无需额外队列或 Redis。

状态：queued → running → succeeded / failed；failed → queued；queued / running / failed → cancelled（会话关闭）。外部模型调用可能因恢复而重复执行，数据库只接纳当前尝试的一条成功回复。

### 退出会话

锁定 chat_groups，校验成员身份；已 closed 返回原关闭结果。设 status = closed、closed_at 和 closed_by；设助手 enabled = false；将非成功调用标记 cancelled、finished_at 并清空租约。提交后通知双方移除会话，尽力中断模型调用。

发送、认领、落回复、关闭均按“会话行 → 调用行”的顺序加锁。消息插入触发器也锁会话，避免绕过应用代码写入迟到回复。关闭后客户端缓存移除，历史接口拒绝访问；已经传到设备的数据无法由数据库远程收回。

### 阅读进度

只在客户端已显示到 seq 后更新 last_read_seq，取旧值和请求值的最大值，并限制不超过 last_message_seq。未读按 seq > last_read_seq 且非本人发送的消息统计，不能直接用序号差，因为本人发送不计未读。

## 5. SQL 与验证

建表文件：services/api/migrations/000001_chat_schema.up.sql。它是空库初始化基线，不迁移旧 Webchat 的 JSON 状态文件。Go 执行器位于 `internal/database`，SQL 通过 `migrations/embed.go` 嵌入二进制。使用 `make db-migrate` 或 API 的 `-migrate` 参数显式执行；迁移事务同时保存 `schema_migrations` 中的版本、文件名、SHA-256 和执行时间。事务级 advisory lock 串行化多个迁移进程；重复执行跳过已应用版本，校验不一致、未知版本或中间缺失版本时报错，失败整体回滚。

迁移 `000002_case_sensitive_usernames.up.sql` 将 `lower(username)` 唯一索引替换为 `username` 唯一索引。已应用的 000001 不修改，保持版本校验有效；新库依次应用两版，已有库仅升级第二版。用户名原值保留，`Liz`、`liz`、`LIZ` 作为不同账号，查重和登录均按相同大小写精确匹配。

`-database` 开启 pgx 连接池，启动时检查连接和所有迁移记录，不自动修改 schema。HTTP Router 精简后只内置 `/test/healthz`，不再提供运行期数据库就绪接口。默认基础 HTTP 模式仍无需数据库。连接从 `JETAIME_DATABASE_URL` 读取，配置和连接错误不输出连接字符串或密码。现有 assistant 调试存储仍为 JSON，不因为开启数据库而自动迁移。

可在独立测试库执行：

```sh
psql "$JETAIME_TEST_DATABASE_URL" -1 -v ON_ERROR_STOP=1 -f services/api/migrations/000001_chat_schema.up.sql
psql "$JETAIME_TEST_DATABASE_URL" -1 -v ON_ERROR_STOP=1 -f services/api/migrations/000002_case_sensitive_usernames.up.sql
psql "$JETAIME_TEST_DATABASE_URL" -v ON_ERROR_STOP=1 -f services/api/migrations/000001_chat_schema.check.sql
```

以上 psql 命令仅用于独立 SQL 检查，不建立迁移版本记录；API 使用的库应通过 `make db-migrate` 初始化。验证脚本使用虚构数据，最终 ROLLBACK；仅用于空白测试库。没有提供自动回滚建表脚本，避免把线上聊天数据删除当作常规版本回退。

2026-10-14 补充：已在本机临时 PostgreSQL 18 实例执行 Go 集成测试，覆盖四个连接并发迁移、重复执行、基线 SQL 约束验证、失败事务回滚、未知版本/缺失版本/校验和不一致拒绝、未记录版本的已有表拒绝及等待锁时取消恢复。`make db-test` 要求 `JETAIME_TEST_DATABASE_URL`，每个测试创建独立 schema 并清理；未设置时常规 `go test` 会跳过集成测试。

精简 Router 前的实际 API 二进制已验证（历史检查记录，就绪接口现已移除）：空库启动拒绝、显式迁移及重复迁移、数据库模式启动、数据库停止后 `/api/ready` 返回 503 而存活接口仍返回 200、恢复数据库后就绪恢复 200，以及 SIGTERM 正常退出。结果见 `outputs/postgres-check/result.json`。Go 全量 race 测试、vet、lint 和双架构 Linux 打包通过；临时 PostgreSQL 实例已停止。此轮多连接验证仅覆盖迁移，发送消息与关闭会话等业务事务仍待实现和验证。

2026-10-14 已在临时 PGlite（PostgreSQL 18.6 / WASM）中实际执行建表和验证 SQL：9 张表建立成功、24 项拒绝检查通过，正常双人会话、AI 回复、阅读进度和关闭后重新建立会话流程通过，测试数据全部回滚。报告位于 outputs/schema-check/result.json。此环境未验证真实多连接并发和线上部署。

线上发布前还需在选定的 PostgreSQL 版本上验证迁移、真实多连接并发、权限隔离与备份恢复；表结构验证不代表 HTTP 接口或客户端已经实现。

约束与索引设计参考 [PostgreSQL 约束](https://www.postgresql.org/docs/current/ddl-constraints.html)、[部分索引](https://www.postgresql.org/docs/current/indexes-partial.html)、[延迟约束触发器](https://www.postgresql.org/docs/current/sql-createtrigger.html)。
