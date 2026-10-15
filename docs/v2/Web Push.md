# Web Push 通知

使用 HTTPS + 主屏幕 PWA + 标准 Web Push。iPhone 需要 iOS 16.4 或更新版本；无需 Apple Developer Program 会员。

## 使用

1. Safari 打开 `https://101.132.186.29/page/`，通过分享菜单添加到主屏幕，开启“作为网页 App 打开”（若显示）。
2. 从桌面图标进入并登录，选择“消息通知设置”。
3. 点击“开启通知”，允许系统权限，再点击“发送测试通知”。切后台或锁屏查看。
4. 关闭通知只影响当前设备；退出账号、改密码或会话过期后，服务器停止向对应会话发送新通知。

普通消息通知其他成员；AI 完整回答通知所有成员。已读消息在发送前跳过。通知只显示通用提示，不包含聊天正文。
系统专注模式、通知设置和网络状态仍会影响实际展示。推送服务接受不代表用户必然已经看到，消息数据库仍是事实来源。

## 配置

在 `services/api` 执行 `go run ./cmd/webpush-keygen` 生成一次 VAPID 密钥（输出含私钥，不要提交、截图或粘贴到日志）。
将结果保存到忽略的 `local/.env` 或服务器 `api/.env`，并添加：

```sh
JETAIME_WEB_PUSH_ENABLED='true'
JETAIME_WEB_PUSH_PUBLIC_KEY='生成的公钥'
JETAIME_WEB_PUSH_PRIVATE_KEY='生成的私钥'
JETAIME_WEB_PUSH_SUBJECT='https://101.132.186.29/page/'
```

密钥必须固定保存并备份，不能随构建重新生成。SUBJECT 使用联系邮箱 `mailto:...` 或公开 HTTPS 联系 URL。
只有公钥通过接口公开，私钥不进入前端。更换公钥后，设备需在通知设置中重新点击开启，以更换旧订阅。

部署前执行迁移 000004，然后重启 API。先构建新二进制，再用包内 `bash start.sh migrate` 应用迁移。
迁移只新增表、索引和消息插入触发器，不回填历史消息。Nginx 精确提供 `/page/sw.js` 和 Manifest，
不能将这两个资源回退到 SPA HTML。Service Worker 仅处理通知，不缓存页面和认证 API。
服务器出站 HTTPS 需允许 Apple `*.push.apple.com`；Chrome/Firefox 还需要各自推送服务。

## 数据与可靠性

订阅绑定用户和当前 auth_sessions 记录；相同设备切换登录时删除旧绑定及其待发送任务。
消息插入触发器在同一事务写入 `web_push_jobs`，普通消息排除发送者，AI 消息发给双方。
Go worker 使用数据库锁领取任务，60 秒租约允许进程崩溃后恢复，不依赖易丢失的 LISTEN/NOTIFY。
网络发送超时 10 秒；失败指数退避，最多 5 次，任务 1 小时过期；404/410 清除订阅。
投递采用至少一次语义，进程在发送成功后、记录成功前崩溃可能重发，通知 tag 使用消息 ID 减少重复显示。
过期任务保留约一天后清理；已失效会话订阅每小时清理，投递时立即检查会话有效性。

订阅 endpoint 是敏感设备信息，不记录在应用日志。仅接受已知浏览器推送域名，验证 P-256 订阅密钥，
不跟随 HTTP 重定向。测试接口只允许当前用户、当前会话的订阅，每个设备间隔 30 秒。

## 验证

`internal/webpush` 的 PostgreSQL 测试通过 `JETAIME_TEST_DATABASE_URL` 创建并删除独立 schema，
验证事务回滚、消息幂等、AI 收件人、重试、失效订阅、账号切换及会话撤销/过期。HTTP 发送使用受控客户端，
不向 Apple 的真实用户设备发测试消息。真实锁屏送达需在 iPhone 上完成授权后验证。

前端单元测试验证通知显示和安全跳转，Playwright 覆盖通知开关、测试接口以及 iPhone 安装提示。
