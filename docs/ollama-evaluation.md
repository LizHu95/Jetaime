# 用 Ollama 评测推荐结果

`scripts/ollama_evaluator.py` 是本地 LLM-as-a-judge，调用 Ollama `/api/chat`，用 JSON Schema 约束输出；模型输出仍会在脚本中严格校验。返回 `score`、`label`、`explanation` 和评测模型、提示词版本、token/耗时元数据。

| 标签 | 分数 | 含义 |
| --- | --- | --- |
| `pass` | 1 | 有足够证据，需求和适用约束满足，解释有依据 |
| `fail` | 0 | 有具体证据表明违反约束或编造信息 |
| `insufficient_evidence` | null | 缺少判断必需的事实或记忆，无法评分 |

`null` 不代表通过；比较实验时同时查看缺证据比例。网络失败、输出截断、非法 JSON 是执行错误，不会伪装成质量分数。评测模型的判断仍需用人工确认样例校准，同模型评测可能有偏差。

## 与现有 Phoenix 实验一起运行

先保持 `make trace` 和 `make debug` 运行。确保本机已安装 uv、Ollama 已启动且模型已下载：

```bash
make eval-check EVAL_ARGS="--judge-model qwen3.5:9b"
make eval EVAL_ARGS="--name ollama-judge-v1 --judge-model qwen3.5:9b"
```

实验运行请求成功、结果类型匹配和推荐笔记 ID 匹配三个确定性检查，额外记录 `ollama_recommendation_quality`，标记为 LLM 评测，结果在 Phoenix 实验页查看。`--model` 控制被评测的推荐生成模型，`--judge-model` 控制评测模型；省略后者保持原有行为。两者可选择不同模型。

默认数据集为 `jetaime-select-regression`，可通过 `EVAL_ARGS="--dataset 数据集名称"` 切换。含 `fixture_setup` 的样例会校验基础 fixture 的 SHA-256、候选权限和类型；正式运行时编译临时 API，每条案例在独立进程中应用候选范围及事实覆盖，Trace 写入 `jetaime-eval`。`make eval-check` 只检查服务、样例与 fixture，不编译临时 API、不调用生成模型，也不创建实验；启用 judge 时会检查评测模型是否已安装。基础 fixture 已变化时，先更新数据集再运行。

可指定 `--ollama-url http://127.0.0.1:11434`、`--judge-timeout 300` 和 `--judge-context-tokens 32768`。当前只发送已有任务输出和样例证据，不自动读取完整 fixture，也不自动补齐授权记忆。

## 独立评测已有输出

新建 `examples.jsonl`，每行一个 JSON 对象：

```json
{"id":"case-1","input":{"query":"推荐一部电影","conditions":{"hardConstraints":["必须中文"]}},"output":{"answer":"推荐《Example》，已确认是英文电影"},"metadata":{"evaluation_context":{"verifiedFacts":{"Example":{"language":"英文"}}}}}
```

```bash
make eval-jsonl EVAL_ARGS="--input examples.jsonl --output scores.jsonl --model qwen3.5:9b"
```

也支持 Phoenix 实验导出的 CSV（包含 `input`、`output`、`reference_output`、`metadata` 列）：

```bash
make eval-jsonl EVAL_ARGS="--input baseline.csv --output scores.jsonl --model qwen3.5:9b"
```

CSV 中使用实际 `output` 评分，`reference_output` 只作为参考；不是用于上传测试题的数据集 CSV。案例 metadata 中的 `facts_overrides` 自动提供给评测器作为案例事实，不自动补充用户记忆。原实验执行失败的案例记录为 error，不调用模型评分。

如果已有评分文件需重跑，显式添加 `--overwrite`；默认仍拒绝覆盖。

结果逐行写入，每条带原始 id/行号；有执行错误会记 `error` 并继续后面的样例，最终退出码为 1。输出文件不能已存在，防止覆盖结果；省略 `--output` 则写到标准输出。

Phoenix 数据集样例同样可以设置以下 metadata：

```json
{
  "reference_reviewed": true,
  "evaluation_context": {
    "verifiedFacts": {"note-1": {"totalCostCents": 12000, "durationMinutes": 45}},
    "participants": [{"userId": "user-b", "hardConstraints": ["不吃花生"]}]
  }
}
```

只提供当前样例有权限使用的事实、候选和记忆。`expected` 表示参考答案；只有布尔值 `reference_reviewed: true` 时才会发送给评测模型。Trace 导入的历史模型输出默认未经人工确认。`output.decision.candidateNotes` 可帮助核对候选引用和正文，但不能替代核实事实；缺少参与人记忆时无法充分验证参与人解释。

## 与 Server Evals 的区别

你提供的 [Phoenix Server Evals 文档](https://arize.com/docs/phoenix/evaluation/server-evals/overview) 描述在数据集 Evaluators 页绑定评测器，随后由 Playground 实验触发服务端执行。本脚本通过 Python SDK 执行评测并写入实验结果，不会自动注册或绑定服务端 evaluator，也没有实现文档所述的独立评测项目中的完整模型调用追踪。

若需要完全在 UI 中运行，可在支持该功能的 Phoenix 实例中配置 [Custom AI Provider](https://arize.com/docs/phoenix/settings/custom-ai-providers)：选择 OpenAI SDK / Chat Completions，Base URL 指向 Phoenix 服务可访问的 Ollama `http://127.0.0.1:11434/v1`，API key 使用 `ollama` 占位，然后选择已下载的模型。Ollama 提供 [OpenAI 兼容 API](https://docs.ollama.com/api/openai-compatibility)，实际是否支持 Phoenix 使用的结构化工具调用须按模型验证；容器部署需要使用容器可访问的主机地址。再按 [LLM Evaluators](https://arize.com/docs/phoenix/evaluation/server-evals/llm-evaluators) 配置提示词、标签与输入映射。本脚本直接使用原生 `/api/chat` 的 Schema 输出，不依赖这条兼容链路。

## 入口与测试

日常操作使用 `make eval`、`make eval-check` 和 `make eval-jsonl`，参数通过 `EVAL_ARGS` 原样传给对应脚本；参数帮助可用 `make eval EVAL_ARGS=--help` 或 `make eval-jsonl EVAL_ARGS=--help` 查看。

`phoenix_experiment.py` 负责数据集、案例运行和实验记录；`ollama_evaluator.py` 负责评分，同时提供可复用的 `OllamaJudge` 和独立 JSONL CLI。Makefile 仅提供入口，不维护另一套模型默认值或业务逻辑。

```bash
make eval-test  # 隔离 HTTP 测试，无需本地服务
```

测试依赖由 uv 的隔离环境提供，首次运行需要联网。仓库统一测试与检查命令见 [开发与检查](../README.md#开发与检查)。真实模型评测单独执行，不纳入统一检查。当前 GitHub Actions 仍运行 `make go-check`，上述 Python 测试接入的是本地统一入口。
