# 统一 API 打包

在仓库根目录运行：

```sh
make api-build
# 等效：bash deploy/api/build.sh
```

构建源为 `services/api/cmd/api`，产物直接保存在 `deploy/api/`：

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

仅启动 webchat（无需模型配置和历史文件）：

```sh
# 在运行环境中设置 WEBCHAT_INVITE_CODE，至少 16 位。
# 通过 HTTPS 反向代理访问时还需设置 WEBCHAT_PUBLIC_URL。
./api-linux-amd64 -assistant=false -webchat \
  -addr 127.0.0.1:8081 -webchat-state /var/lib/jetaime/webchat/state.json
```

启动 assistant 时，提供实际模型配置与历史文件，并显式指定持久数据路径：

```sh
./api-linux-amd64 \
  -model-config /etc/jetaime/models.json \
  -fixture /var/lib/jetaime/history.fixture.json \
  -state /var/lib/jetaime/assistant/state.json \
  -log /var/lib/jetaime/assistant/runs.jsonl
```

实际模型配置可参考包内 `models.example.json`。凭据通过运行环境提供，二进制不会自动加载 `.env`；开启 assistant 后可追加 `-webchat` 同端口运行两个模块。服务监听本机回环 IP，公网访问需 HTTPS 反向代理。

页面为 `/page/assistant/` 和 `/page/webchat/`；API 使用 `/api/assistant/` 和 `/api/webchat/`。assistant 保留本机 Host 访问限制，公网域名仅用于 webchat。`GET /api/health` 检查 HTTP 服务存活。飞书仍使用独立长连接程序，此包只包含统一 HTTP API。
