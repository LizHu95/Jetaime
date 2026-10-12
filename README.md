# 日常协同 AI 助理

当前版本为 **V2 最简 Workflow 调试台**：从群组聊天历史回答问题并引用来源，提取明确行动，经成员确认后保存为待办。支持本地 JSON 历史与模拟发消息，提供可查看输入、模型响应和校验结果的调试页面。

一期的收藏驱动推荐、反馈与换批代码已移除。历史产品方案和评测结果归档在 `docs/v1/`，不再提供一期运行入口。

## 本地运行

确保本机 Ollama 已启动，并安装 `qwen3.5:9b` 或其他可配置模型。在仓库根目录执行：

```sh
make assistant
# 使用其他已安装模型或端口：
make assistant ASSISTANT_ARGS='-model qwen3.5:9b -addr 127.0.0.1:8081'
```

打开 <http://127.0.0.1:8081>。页面内嵌于 Go 服务，无需前端依赖或构建。Ctrl+C 停止服务。

`make assistant` 默认加载你提供的微信记录：`local/data/chat-history/history.fixture.json`（2 位成员、441 条消息），不再包含虚构聊天数据。上下文容量默认为 98304。页面“重新读取历史 JSON”重新读取该文件。模型调用默认超时为 5 分钟，以容纳全量历史的本地推理。

运行 `make assistant-check` 可用同一份历史和真实 Ollama 模型验证问答引用、待办提取、确认幂等、完成、重启恢复及 JSONL 轨迹。验证服务使用临时端口和状态目录，不改调试台的消息与任务。先验证全量历史，再回放原记录中的明确日程片段来验证确认与完成；报告保存到 `local/data/agent-check/acceptance.json`。

页面支持群组 / 模拟成员切换、历史问答与来源定位、模拟发送消息后触发 Workflow、待办提取 / 修改 / 忽略 / 确认、本人待办完成，以及运行轨迹查看和 JSON 下载。消息与已确认待办保存在本地，重启可恢复。

这是本机调试工具，未实现真实登录、邀请和多人实时同步。来源 ID 校验不等于语义准确，模型仍可能漏提取或引用不完整，确认前需核对原文。具体参数、文件与边界见 [V2 运行说明](docs/v2/运行说明.md)。

## 代码结构

```text
services/api/
├── cmd/assistant/main.go       本地 HTTP 服务入口
├── internal/assistant/
│   ├── types.go               实体与输入输出类型
│   ├── store.go               群组隔离、消息与待办 JSON 存储
│   ├── model.go               Ollama HTTP 调用
│   ├── workflow.go            历史问答与待办提取
│   ├── server.go              本地 API 与页面适配
│   ├── web/                   内嵌 HTML / CSS / JS 调试页面
│   └── *_test.go              Workflow、存储、HTTP 和模型协议测试
local/data/assistant/           本地状态与运行日志，Git 忽略
local/data/chat-history/        用户提供的历史、原始整理结果与说明
local/data/evals/               私有评测输入、版本和实验输出
local/data/chat-eval/           私有评测报告
local/data/phoenix-v2/          本地 Phoenix 数据库
docs/v2/                       当前设计、技术方案、任务与运行说明
docs/v1/                       一期历史文档与评测结果
```

先从 [workflow.go](services/api/internal/assistant/workflow.go) 阅读两条固定流程：组装授权群组上下文 → Prompt / Schema → 模型调用 → 校验结果。问答调用一次；提取产生候选时追加状态核对调用。任务保存由确认接口触发，模型不能直接创建任务。当前不使用 Agent Loop、自动模型路由、摘要、检索或长期记忆。

## 开发与检查

Go 工具链版本由 `.go-version` 管理；Go 模块当前仅依赖 UUID 库；Phoenix 评测使用独立 Python 项目 `evals/`。根目录提供统一检查入口：

```sh
make fmt        # 格式化 Go 源码
make check      # Go 检查 + Python 评估器测试
make test       # Go 与 Python 测试
```

`make go-check` 仅运行 Go 检查。在 `services/api` 内运行 `go test -race ./...` 可检查并发访问。JavaScript 语法可通过 `node --check services/api/internal/assistant/web/app.js` 验证。

golangci-lint 固定为 v2.14.0，自动安装到 Git 忽略的 `services/api/bin/`；首次安装需要 curl 与网络。CI 运行 `make check`。修改嵌入页面后需重启服务并刷新浏览器。

## 文档

- [文档索引](docs/README.md)
- [V2 产品方案](docs/v2/产品方案.md)
- [V2 技术方案](docs/v2/技术方案.md)
- [V2 开发任务清单](docs/v2/开发任务清单.md)
- [V2 运行说明](docs/v2/运行说明.md)
- [V1 历史产品方案](docs/v1/产品方案.md)

## Phoenix 评测

`make trace` 启动本地 Phoenix，`make eval-check` 离线验证案例，`make eval` 执行 V2 Workflow 实验。正式案例与预期结果待提供，当前文件为空。完整输入契约、评估器与运行方式见 [Phoenix 评测说明](docs/v2/Phoenix评测说明.md)。
