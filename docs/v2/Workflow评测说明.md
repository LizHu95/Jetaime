# Workflow 本地评测

业务 AI Trace 使用 [Langfuse Cloud](../../deploy/langfuse/README.md)。批量评测独立执行，结果保存在本地 JSONL 和汇总 JSON，不需要 Trace 平台或额外数据库。

## 运行

需要 Go、uv 和 Python 3.11–3.14。统一入口加载 `local/.env`，任务模型复用 `local/models.json` 及默认 profile：

```sh
make eval-check     # 离线验证案例结构
make eval-services  # 检查模型配置、密钥存在性及所需本地模型，不执行推理
make eval-test      # 隔离规则、配置及报告测试
make eval
# 固定输入版本和模型 profile：
make eval EVAL_ARGS='--cases local/data/evals/versions/YOUR_SUITE/cases.json --suite-version YOUR_SUITE --model-profile local-qwen --repetitions 3'
```

案例契约在 `evals/cases.schema.json`。每例包含 `id`、`input`（fixture、groupId、actorId、kind、question）及可选 `expected`、`metadata`。只有审核通过的 `expected.reviewed=true` 参与参考和语义评分；未审核或缺少参考时保留 pending，不计为通过。正式输入和历史冻结版本保存在 Git 忽略的 `local/data/evals/`，新克隆需要自行提供。空案例拒绝执行正式评测。

## 评分和输出

每次调用在临时目录使用独立状态，不修改调试台数据。程序评估器检查执行成功、来源隔离、参考规则，以及 requiredSuggestions / forbiddenSuggestions 的逐项行动、负责人、时间和来源。来源 ID 有效不代表语义正确。

`--judge-model` 显式启用独立 Ollama 语义裁判；默认不启用。模型判定只是辅助证据，需要人工校准。裁判错误和任务基础设施错误会记录到结果并返回非零退出码；普通质量 fail 不自动触发质量门槛。

每次运行在 `outputs/evals/` 生成 `experiment-<id>.jsonl`、`summary-<id>.json` 和 `snapshot-<id>/`。快照保存案例、模型公共配置、源码、规则、Schema 和锁文件；manifest 包含案例/源码哈希、Git 状态、模型指纹、上下文、超时、重复次数和裁判配置。案例和报告可能包含真实聊天，应与快照一起作为私有数据归档。

历史评测结果、冻结输入和旧平台实验链接保持原样，不会因平台切换而重写。版本和参考审核记录见 [评测集管理](../../evals/README.md)。
