# V2 Workflow 的 Phoenix 评测试验

更新：2026-10-11。`local/data/evals/cases.json` 已基于用户导入聊天准备 23 条案例（17 条问答、6 条待办提取）。参考答案是 AI 阅读转录证据后的待审草稿，全部 `reviewed: false`，不能当成人工金标准。逐条原消息及审核说明见 [聊天案例审核稿](../../local/data/evals/chat-cases-review.md)。

案例冻结、输入/参考版本、四轮历史逻辑变更和后续执行规则见 [评测集版本管理](../../evals/README.md)。

## 1. 采用的规范与边界

使用 Phoenix Client SDK 的 **Dataset → Experiment → Run → Evaluation** 流程，以及 OpenTelemetry / OpenInference 的 Trace 属性规范：

- Dataset 输入是本次案例的原始 fixture、可选已保存状态、群组、模拟成员、Workflow 类型和问题；输出字段保存人工预期结果，metadata 保存案例 ID 和资料哈希。
- 每次启动创建新的数据集快照，实验固定其 version ID，避免后续改动案例影响旧实验。实验记录模型、上下文配置、Git 版本 / 未提交标记、源码哈希、案例哈希及评估器版本。
- 被测任务调用与页面相同的 Go Workflow；逐案例串行运行，可配置 repetitions。每次运行使用独立临时 fixture / state，结束清理，不使用或修改页面里的消息、任务和未确认建议。
- 通过 Phoenix 官方 `experiments.create`、`log_run`、`log_evaluation` API 保存运行和评分，并显式关联 trace ID。
- 应用与评估器的 Trace 分项目保存。被测 Workflow 步骤使用 Go 实测时间回放为子 span，并标记 `workflow.observed_step=true`；这不是额外调用模型。LLM span 包含实际 Prompt、Schema 请求、响应、模型和真实 Token 统计。

这是**客户端代码执行实验和评估器**，结果可在 Phoenix UI 查看、比较；不等同于注册 Phoenix Server Evals 或直接从 Playground 执行 Go Workflow。页面的日常交互目前仍记录本地 JSONL，本轮 Phoenix 上报针对评测入口。

依据：[Phoenix Experiments API](https://arize-phoenix.readthedocs.io/projects/client/api/experiments.html)、[Datasets API](https://arize-phoenix.readthedocs.io/projects/client/api/datasets.html)、[OpenInference 语义规范](https://github.com/Arize-ai/openinference/blob/main/spec/semantic_conventions.md)。

## 2. 启动与检查

需要 Go、Make 和 uv。Python 支持 3.11–3.14，uv 可管理隔离环境。依赖与版本固定在 `evals/pyproject.toml` / `uv.lock`：Phoenix 20.20.0、Client 3.5.0、OTel 0.17.2。服务和评测脚本使用各自的虚拟环境，避免运行评测时移除服务依赖。

```sh
# 终端一：本地 Phoenix，保持运行
make trace
# 浏览器：http://127.0.0.1:6006

# 终端二：离线验证案例结构，不上传、不调用模型
make eval-check

# 只读检查 Phoenix 与本机 Ollama / 模型是否可访问
make eval-services

# 隔离测试，不需要 Phoenix 或真实模型
make eval-test

# Phoenix 协议联调：需要 Phoenix，使用临时 HTTP 测试模型
make eval-smoke
```

Phoenix 仅监听本机，SQLite 数据保存在 Git 忽略的 `local/data/phoenix-v2/`。Ctrl+C 停止；再次启动保留数据。协议测试实验以 `PROTOCOL TEST ONLY` 命名，仅验证接入、重复运行、评分和 Trace，不代表产品质量。测试案例只在临时目录生成，不写入正式案例文件。

## 3. 正式运行（案例准备后）

```sh
# 程序评估器 + 人工参考断言；语义评估标记 pending_judge
make eval EVAL_ARGS='--cases local/data/evals/cases.json --timeout 300 --context-tokens 16384 --repetitions 3'

# 显式启用已安装的独立评估模型
make eval EVAL_ARGS='--cases local/data/evals/cases.json --timeout 300 --context-tokens 16384 --repetitions 3 --judge-model YOUR_INSTALLED_JUDGE_MODEL'
```

不要原样执行占位模型名称。`--judge-model` 不配置时不会调用语义评估模型。优先使用与被测模型独立、经人工校准的评估模型；相同模型可以验证接入，但其评分不视为独立质量证据。

其他参数：`--model`、`--ollama-url`、`--phoenix-url`、`--timeout`（秒）、`--context-tokens`、`--dataset`、`--name`、`--output`。默认被测模型 `qwen3.5:9b`，上下文 8192，超时 180 秒。默认实验名使用 ASCII；Phoenix 20.20.0 的实验 JSON 导出在非 ASCII 名称下可能出现响应头编码错误。

空案例文件会拒绝创建正式实验。每次运行输出 Phoenix 实验链接、逐案例 JSONL 和汇总 JSON，默认保存在 Git 忽略的 `local/data/evals/output/`。评分 fail 本身不使进程失败；任务基础设施错误或评估器异常会返回非零退出码。语义 fail 与执行失败仍可在实验中分析，不设置未经确认的质量门槛。

## 4. 案例输入契约

完整机器格式见 `evals/cases.schema.json`，正式文件是案例数组。每条案例包含：

| 字段 | 含义 |
| --- | --- |
| `id` | 唯一、稳定的案例编号 |
| `input.fixture` | `users` / `groups` / `members` / `messages`，实体字段与当前 Demo 一致；消息时间需含时区 |
| `input.state` | 可选初始新增消息与已确认任务：`messages` / `tasks`；用于重复任务等场景 |
| `input.groupId` / `actorId` | 本次授权范围与模拟身份 |
| `input.kind` | `qa` 或 `extract` |
| `input.question` | qa 必填，extract 可省略 |
| `expected` | 可选人工预期，缺少时可跑程序校验，不能算语义通过 |
| `metadata` | 场景分类、双人 / 多人、说明等自由字段 |

`expected` 可配置：

| 字段 | 用途 |
| --- | --- |
| `reviewed` | 必填布尔值；只有 true 才使用该参考评分 |
| `workflowStatus` | 可选 `succeeded` / `failed`；负面权限案例可预期失败 |
| `answerStatus` | qa 的预期 answered / insufficient_evidence / conflicting_evidence |
| `requiredSourceMessageIds` | 必须包含的来源集合（允许额外合法来源） |
| `forbiddenSourceMessageIds` | 不得引用的来源集合 |
| `suggestionCount` | extract 的预期建议数；仅检查数量，不保证行动含义正确 |
| `requiredSuggestions` | 必须保留的独立行动；标题包含全部titleContains关键词，可配置assigneeId、timeExpression及requiredSourceMessageIds，每项须匹配不同建议 |
| `forbiddenSuggestions` | 禁止出现的行动；匹配方式与必需行动相同 |
| `reference` | 非空对象：人工参考答案、行动清单、负责人、时间、共识范围等；供语义评估 |
| `notes` | 对歧义、可接受替代表达、评分依据的说明 |

当前参考均待人工核对，未审核时仅执行程序检查，参考断言及语义评分跳过。核对后逐条将 `reviewed` 改为 true；`local/data/evals/prepare-chat-evals.py` 可重建初稿，但会覆盖已有审核标记，审核开始后不要重新生成。`--check-only` 验证 JSON Schema、时间格式和案例 ID 唯一性；跨实体关系与业务合法性由真实 Go 存储 / Workflow 在执行时验证。

## 5. 当前评估器

| 指标 | 类型 | 通过条件 / 范围 |
| --- | --- | --- |
| `execution_success` | CODE | Workflow 成功完成；与预期失败是否合理分开看 |
| `source_integrity` | CODE | 来源 ID 真实、属于授权群、没有重复，必要来源不为空，负责人属于群组；不判断引用是否支持答案 |
| `reference_rules` | CODE | 已审核参考中的状态、数量、必须 / 禁止来源断言全部满足；未配置断言则不适用 |
| `semantic_quality` | LLM | 根据原始授权对话和已审核 reference 判断：答案事实、引用支持、行动遗漏 / 误提取、负责人、时间，以及多人共识范围 |

语义评估器 Prompt / 输出 Schema 位于 `evals/evaluators.py`，版本 `collaboration-semantic-v1`。标签 pass / fail 对应 1 / 0；insufficient_evidence、pending_reference、pending_judge、not_applicable **没有分数，不计作通过，也不按 0 分参与均值**。汇总分别列 scored、passed、pending、errors；pending 字段统指所有未打分结果。

评估器 HTTP 错误、超时、截断、非法 JSON 等写入 Phoenix evaluator error，不伪装成质量 fail。任务进程异常写入 experiment run error；Workflow 自身失败保留完整业务输出和错误，由程序指标反映。LLM 判定是辅助证据，正式案例到位后还需要人工校准评分标准与评估器可靠性。

## 6. 查看与回溯

- Phoenix Datasets 中打开本次 `jetaime-v2-workflow-<hash>-<snapshot>` 数据集，进入 Experiments 比较结果和每条评分说明。
- `jetaime-v2-experiments` 项目查看被测 Workflow Trace，运行 trace ID 同时关联到 experiment run。
- `jetaime-v2-evaluators` 项目查看 CODE / LLM 评估器 Trace；评分关联其 trace ID，LLM 子 span 保留实际评估请求与响应。
- 本地 JSONL 保留输出、任务错误、评分及运行 ID。流程在写入中途失败会保留已经上传的记录，不自动删除或伪造完成状态。

输入与 Trace 可能包含完整案例对话，只向配置的 Phoenix 实例和模型端点发送；默认全部在本机。本轮没有恢复一期推荐、反馈、收藏或换批代码。

## 2026-10-12：reviewed-r2漏判断言

输入不变，强化参考规则并冻结chat-v1.1-reviewed-r2及extract6子集。健身/理发分别检查负责人、时间和来源；面包片段必须保留骑回家、做英语，禁止已完成购买。关键词断言不等于完整语义判定，需要维护同义表达或使用语义裁判。

运行 `uv run --project evals --locked python scripts/check-action-assertions.py` 可离线检查旧输出，不调用模型、不改旧实验评分。当前保存的v4完整复测18条提取中3条遗漏被新规则发现；v5同类3条、v6同类2条也失败。详情见 [漏判断言验证](../../local/data/chat-eval/action-assertions-r2.md)。
