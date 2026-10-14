# 飞书 SDK 长连接接入

`cmd/feishu` 默认使用官方 `oapi-sdk-go/v3 v3.12.1` 的 WebSocket 客户端。程序主动连接飞书，SDK 负责鉴权、心跳及自动重连，退出时取消 context 并清理连接。适用于飞书企业自建应用。

- `im.message.receive_v1`：记录 `feishu message received`，确认接收成功。
- `card.action.trigger`：记录 `feishu card action received`，返回成功 toast「飞书回调已接通」。

当前是连通性测试：不回复消息、不修改任务、不调用模型。重复投递会重复记录日志，后续业务写入需增加幂等控制。处理函数应快速返回。

## 环境配置

在 `local/feishu/.env` 中填写应用「凭证与基础信息」的 **App ID、App Secret**：

```env
FEISHU_APP_ID=cli_你的应用ID
FEISHU_APP_SECRET=你的应用密钥
```

本机配置已被 Git 忽略，权限为 `600`。长连接只需 App ID 和 App Secret。

## 本机运行

本机测试和云服务联调建议只启动一个客户端，便于确认消息由哪个客户端处理。

```sh
# 仓库根目录
set -a
. ./local/feishu/.env
set +a
cd services/api
go run ./cmd/feishu
```

日志出现 `feishu long connection ready` 表示建连成功，`starting feishu long connection` 仅表示开始尝试。启动配置错误或终止性连接错误返回非零退出码，systemd 可据此重启。

## 编译与打包

本机需要 Go 和 Python 3。在仓库根目录执行：

```sh
bash deploy/feishu/build.sh
```

输出 `local/feishu/feishu-deploy.tar.gz`，包含 Linux amd64 / arm64 二进制、当前私有环境配置、systemd 配置及安装/更新脚本。每次修改配置或代码后需重新打包。

## Linux 服务器部署

上传 `local/feishu/deploy` 目录到服务器主目录。例如，在本机仓库根目录执行，替换命令中的服务器用户名和公网 IP：

```sh
scp -r local/feishu/deploy <服务器用户名>@<服务器公网IP>:~/
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
4. 启用机器人能力、接收消息权限及测试用户可用范围，按控制台要求发布/启用版本。
5. 单聊机器人发送消息，确认云端出现 `feishu message received`。
6. 使用该应用身份在 API 调试台发送 `test-card.json` 卡片（需发送消息权限）；点击按钮，确认出现 `feishu card action received`，客户端显示「飞书回调已接通」。

连接状态通过、真实消息和真实卡片点击全部成功，才能判定接入完成。

## 验证

```sh
cd services/api
go test -race ./...
go vet ./...
```

测试覆盖 WebSocket 使用的明文事件/回调分发、业务错误传递、缺少 App ID/Secret 的启动校验和取消 context。单元测试不能代替真实飞书建连和投递。

官方资料：[SDK WebSocket 客户端](https://github.com/larksuite/oapi-sdk-go/blob/v3_main/ws/client.go)、[长连接事件配置](https://open.feishu.cn/document/server-docs/event-subscription-guide/event-subscription-configure-/request-url-configuration-case)、[长连接回调配置](https://open.feishu.cn/document/event-subscription-guide/callback-subscription/step-1-choose-a-subscription-mode/configure-callback-request-address)。
