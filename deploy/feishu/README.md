# 飞书 SDK 长连接接入

`cmd/feishu` 默认使用官方 `oapi-sdk-go/v3 v3.12.1` 的 WebSocket 客户端。程序主动连接飞书，SDK 负责鉴权、心跳及自动重连，退出时取消 context 并清理连接。适用于飞书企业自建应用。

- `im.message.receive_v1`：记录 `feishu message received`，日志包含原始消息内容 `content`、消息 ID、会话 ID 和会话类型。群聊和单聊都回复一张正文为「我一直都在」、带「测试回调」按钮的卡片，成功记录 `feishu card replied`，失败记录 `feishu card reply failed`。
- `card.action.trigger`：记录 `feishu card action received`，返回成功 toast「飞书回调已接通」。
- `im.chat.access_event.bot_p2p_chat_entered_v1`：记录 `feishu bot chat entered`，确认用户进入机器人单聊事件已处理。

当前是连通性测试：收到群聊或单聊消息后打印内容并回复卡片，不修改任务、不调用模型。卡片 JSON 内嵌在 `services/api/internal/feishu/reply-card.json` 中，无需在服务器额外配置文件。重复投递会重复记录日志，卡片回复使用基于原消息 ID 的固定 UUID，飞书在 1 小时内去重。发送请求超时为 10 秒。

## 环境配置

在 `local/feishu/.env` 中填写应用「凭证与基础信息」的 **App ID、App Secret**：

```env
FEISHU_APP_ID=cli_你的应用ID
FEISHU_APP_SECRET=你的应用密钥
```

本机配置已被 Git 忽略，权限为 `600`。长连接只需 App ID 和 App Secret。

启动时使用 App ID 和 App Secret 显式调用 `/open-apis/auth/v3/tenant_access_token/internal` 检查应用鉴权。成功记录 `feishu application authorization ready` 和有效期；失败记录 `feishu application authorization failed`、错误码及飞书 `log_id`，仍继续启动长连接以便接收消息。日志不会打印 Token 或 App Secret。后续消息请求的 Token 缓存和更新由 SDK 自动管理，不需要用户扫码授权。`21001` 表示获取凭证被访问策略拒绝，需在飞书后台排查策略；添加此检查不会自动解除限制。

## 本机运行

本机测试和云服务联调建议只启动一个客户端，便于确认消息由哪个客户端处理。

```sh
# 仓库根目录；自动加载 local/.env 与 local/feishu/.env：
make feishu
# 等效：python3 scripts/dev.py feishu
```

日志出现 `feishu long connection ready` 表示建连成功，`starting feishu long connection` 仅表示开始尝试。启动配置错误或终止性连接错误返回非零退出码，systemd 可据此重启。

## 编译与打包

本机需要 Go 和 Python 3。在仓库根目录执行：

```sh
make feishu-build
```

输出 `outputs/feishu/feishu-deploy.tar.gz`，包含 Linux amd64 / arm64 二进制、当前私有环境配置、systemd 配置及安装/更新脚本。每次修改配置或代码后需重新打包。

## Linux 服务器部署

上传 `outputs/feishu/deploy` 目录到服务器主目录。例如，在本机仓库根目录执行，替换命令中的服务器用户名和公网 IP：

```sh
scp -r outputs/feishu/deploy <服务器用户名>@<服务器公网IP>:~/
```

首次安装，在服务器执行：

```sh
cd ~/deploy
sudo bash install.sh
```

**已有安装，使用更新脚本：**

```sh
cd ~/deploy
sudo bash update.sh
sudo journalctl -u feishu -f
```

更新脚本会备份原程序、环境配置和 systemd 文件，再替换文件并重启该服务。配置安装在 `/etc/jetaime-feishu.env`；程序安装在 `/opt/jetaime-feishu/feishu`。安装/更新脚本显示 active 仅表示进程运行，仍需通过日志确认长连接 ready。

服务通过长连接接收事件和回调，无需开放入站端口或配置请求地址。云服务器需能访问飞书的 HTTPS/WebSocket 服务。

## 飞书联调

1. 确认云端日志出现 `feishu long connection ready`。
2. 在飞书「事件与回调 → 事件配置」选择 **使用长连接接收事件**，重新验证并保存，添加 `im.message.receive_v1`。
3. 在「回调配置」选择 **使用长连接接收回调**，重新验证并保存，添加新版 `card.action.trigger`。
4. 启用机器人能力、接收消息权限、以应用的身份发消息（`im:message:send_as_bot`）权限及测试用户可用范围，按控制台要求发布/启用版本。
   如需接收不 @ 机器人的群消息，开通 `im:message.group_msg`（获取群组中所有消息）权限，完成审批和版本发布，并把机器人加入目标群。该权限接收用户消息，不包含机器人消息。在群里直接发送消息，确认日志出现 `chat_type: group` 及对应的 `content`，并收到「我一直都在」卡片。
5. 单聊机器人发送消息，确认云端出现 `feishu message received` 和 `feishu card replied`，聊天中收到测试卡片。
6. 点击卡片上的「测试回调」按钮，确认出现 `feishu card action received`，客户端显示「飞书回调已接通」。也可使用该应用身份在 API 调试台发送 `test-card.json` 卡片做独立测试。

连接状态通过、真实消息和真实卡片点击全部成功，才能判定接入完成。

## 验证

```sh
cd services/api
go test -race ./...
go vet ./...
```

测试覆盖 WebSocket 使用的明文事件/回调分发、业务错误传递、缺少 App ID/Secret 的启动校验和取消 context。单元测试不能代替真实飞书建连和投递。

官方资料：[SDK WebSocket 客户端](https://github.com/larksuite/oapi-sdk-go/blob/v3_main/ws/client.go)、[长连接事件配置](https://open.feishu.cn/document/server-docs/event-subscription-guide/event-subscription-configure-/request-url-configuration-case)、[长连接回调配置](https://open.feishu.cn/document/event-subscription-guide/callback-subscription/step-1-choose-a-subscription-mode/configure-callback-request-address)。
