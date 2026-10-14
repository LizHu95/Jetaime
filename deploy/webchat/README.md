# 移动端 Web 聊天室

独立于飞书的多人共享聊天室，移动端适配。邀请码注册，独立账号密码登录；用户名作为房间显示名。所有注册成员可查看共享房间最近 200 条消息，发送后机器人固定回复「我一直都在」，当前尚未接入模型 Workflow。页面每 2 秒更新消息，移动端点击发送，桌面端 Enter 发送。

账号和消息保存在服务器 JSON 文件中，密码使用 bcrypt 哈希。登录状态是服务端随机 Session，通过 HttpOnly、SameSite=Lax Cookie 保存，公网 HTTPS 模式开启 Secure。Session 最长 7 天，退出立即撤销，进程重启后需要重新登录；账号和消息不丢失。身份从 Session 获取，不接受前端指定用户 ID。登录/注册具备来源地址限速，反向代理后的用户共享该限额（10 次/分钟）。当前适合小范围共享房间；暂无改密、找回密码、多房间及管理后台。

## 本地运行

```sh
cd services/api
export WEBCHAT_INVITE_CODE="$(openssl rand -hex 16)"
# 将邀请码保存到你自己的密码管理器，再分享给需要加入的人。
go run ./cmd/api -assistant=false -webchat -addr 127.0.0.1:8082
```

访问 http://127.0.0.1:8082/page/webchat/（根路径自动跳转）。邀请码不要每次启动都重新生成，建议存入本地环境配置。程序只监听回环地址。

## 构建

连通性检查使用无需登录的 `GET /api/health`，正常返回 HTTP 200 和 `{"status":"ok"}`。此接口只确认统一 HTTP 服务可响应，不检查模型或外部服务。

```sh
# 公网 HTTPS 检查（替换成实际域名）：
curl -i --connect-timeout 5 --max-time 15 https://lizstar.site/api/health
# 在服务器上绕过反向代理，保留配置中的域名 Host：
curl -i -H 'Host: lizstar.site' http://127.0.0.1:8082/api/health
```

```sh
bash deploy/webchat/build.sh
```

产物 `outputs/webchat/webchat-deploy.tar.gz` 包含 Linux amd64/arm64 程序、安装脚本及配置示例，不包含真实凭证或聊天室数据。

## 部署到服务器

1. 准备域名，DNS 指向服务器公网 IP。通过 HTTPS 反向代理暴露，服务本身监听 `127.0.0.1:8082`。不要直接将未启用 HTTPS 的登录页公开。
2. 上传 `outputs/webchat/deploy` 到服务器独立目录，例如 `/root/webchat-deploy`，避免覆盖飞书部署目录。
3. 创建 `/etc/jetaime-webchat.env`（权限 600），根据 `.env.example` 填入实际域名和至少 16 位随机邀请码。`WEBCHAT_PUBLIC_URL` 必须与浏览器访问的 HTTPS origin 完全一致。
4. `cd /root/webchat-deploy && bash install.sh`。更新时使用同一脚本，它保留账号、聊天记录和服务器环境配置，仅替换程序和服务文件。
5. 使用 Caddy 或已有 Nginx 配置 HTTPS 反向代理。`Caddyfile.example` 提供 Caddy 示例，替换域名；若使用 Nginx，转发时保留 `Host`。公网只需开放 80/443。
6. 打开域名注册并登录，使用不同浏览器注册第二个账号，验证共享消息；退出后应回到登录页。

```sh
journalctl -u webchat -f
```

首次安装和更新都不改变飞书服务。不要同时启动多个 Webchat 进程写入同一份 JSON 状态文件。后续若要接入更多房间、更多并发和模型，可迁移到数据库并对接现有 Workflow。
