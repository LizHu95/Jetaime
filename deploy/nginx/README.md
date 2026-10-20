# 公网 IPv4 HTTPS

Nginx 默认配置继续提供 HTTP，并预留 ACME 验证目录及独立的 TLS 配置目录。
常规 `make deploy` 不会覆盖 `/etc/nginx/jetaime-tls/` 中的证书配置。

服务器先开放入站 TCP 80、443，部署更新后的 `default.conf`，再以 root 执行：

```sh
apt-get install -y python3-venv
python3 -m venv /opt/jetaime-certbot
/opt/jetaime-certbot/bin/pip install 'certbot>=5.4'
bash enable-ip-https.sh PUBLIC_IPV4
```

脚本通过 webroot 申请 Let's Encrypt 的公网 IP 证书，不需要域名或 Apple 开发者账号。
Certbot 需要 5.4 或更新版本。证书有效期约 6 天，保留 80 端口的 ACME 验证路径，
由 `jetaime-certbot-renew.timer` 每 6 小时检查续期；deploy hook 验证 Nginx 配置并重新加载证书。
脚本使用无联系邮箱的 ACME 账号；依赖定时续期，不提供邮件到期提醒。

启用后访问 `https://PUBLIC_IPV4/page/`。服务器应使用固定公网 IP。
TLS 配置将 HTTP 自动跳转到 HTTPS，ACME 验证路径保留 HTTP 直连以便续期。
仅配置 HTTPS 不会自动启用 PWA 或 Web Push，这些需要前后端另外接入。

验证：

```sh
curl --fail https://PUBLIC_IPV4/page/
/opt/jetaime-certbot/bin/certbot renew --dry-run --no-random-sleep-on-renew --run-deploy-hooks
systemctl list-timers jetaime-certbot-renew.timer
```

若证书已申请但后续配置失败，检查 `/etc/nginx/jetaime-tls/ip.conf`、
`nginx -t` 和 `systemctl status nginx`；不要为排障删除仍在使用的证书。

参考：https://letsencrypt.org/2026/03/11/shorter-certs-certbot/

`/page/phoenix/` 独立反代至本机 6006，包含静态资源和 GraphQL，使用 Basic Auth。首次安装生成的凭据在 `/etc/jetaime-phoenix/access.txt`，后续部署保留；Phoenix 服务的 root path 必须为 `/page/phoenix`。
