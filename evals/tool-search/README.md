# Tool search A/B/C 评测

这是一套人工编写的合成开发题，用于比较工具加载策略。聊天、人物、地点、价格、天气和路线数据都是虚构的，不是用户真实历史或真实高德录制，也不能作为线上准确率。所有候选题的 `reviewed` 均为 `false`，预期答案和验收线待业务审核。

| 版本 | 被测代码 | 策略 |
| --- | --- | --- |
| A | `1cc0db9`，当前 `feat/debug` 基线 | 全目录常驻 |
| B | 本地 `codex/tool-search-b` 对应提交 | 高德按需发现 |
| C | 本地 `codex/tool-search-c` 对应提交 | 最小常驻，其余按需发现 |

运行入口寻找已有的 Git worktree，并核对提交与共同基线，不自动切分支、合并、重置或部署。若找不到，使用 `--a-root`、`--b-root`、`--c-root` 指定已有工作目录。A 固定为 protocol 中的基线提交；B/C 必须匹配本地对应分支，且包含 A。修改 API 生产代码后应先提交再比较；输入和脚本修改会分别通过套件和工具哈希记录。

可用 `--variants A B` 只运行A/B，不查找或构建C；默认仍为A/B/C。任务上限按实际选择的版本数计算。共同修复先放到同一公共代码上，再仅修改工具注册策略派生B；正式比较前检查提交差异，避免仅B带有格式修复、位置规则等公共改动。

## 已准备的文件

| 文件 | 用途 |
| --- | --- |
| `messages.synthetic.jsonl` | 44 条合成消息，含被截止边界隐藏的未来改约消息 |
| `cases.draft.json` | 20 道候选题，附工具、关键词、来源断言和严格参数地图响应 |
| `protocol.json` | 分类、两道冒烟题、每题三次、逐题人工检查项及建议验收线 |
| `review.md` | 用户可以直接审核的问题、参考和待确认项 |

历史检索题的证据位于初始最近30条窗口之外，必须通过固定检索补入。总结题按时间读取完整历史。固定检索不评估检索排序；本套件也不覆盖连续追问、线上提醒投递或定位权限恢复。地图响应只匹配逐个列出的参数；同城名称、关键词和坐标的少量等价写法显式列出，不使用通配符。未覆盖的合理写法先记为录制缺失，复核后冻结新题集，不把缺录制直接解释为策略失败。

## 1. 离线检查（现在可运行）

```sh
make eval-tool-search-check
python3 -m unittest discover -s scripts/tests -p 'test_*eval*.py'
cd services/api && go test -race ./internal/ai/replay ./cmd/eval-replay
```

第一个命令构建三份真实分支二进制，使用同一回放代码，逐一验证全部20道题。它不加载模型密钥、不初始化 Phoenix SDK，也不发送模型或 Phoenix 请求。每次会在 `outputs/evals/tool-search/<run>/` 保存私有输入副本、二进制、预检报告和来源清单，不覆盖历史运行。

## 2. 审核和冻结

先审阅 `review.md`，逐题核对 `protocol.json` 中的人工检查项。确认后复制 `cases.draft.json` 到新文件（例如 `cases.reviewed-v1.json`），填写一个不含 `draft` 的新版本并仅将确实通过审核的题设为 `reviewed=true`。原候选文件继续保留，不能批量标记后称已人工审核。所有20题审核完成后才使用统一在线入口。

自动断言不等于完整质量评分。提醒必须人工核对参数和模拟操作数；地图需核对调用顺序、坐标、卡片和回答事实；边界题需检查是否真的追问、是否虚称完成。关键词命中不能替代这些检查。建议验收线仍需确认，脚本不会自动宣布获胜或部署。

## 3. 先冒烟，再完整对照（确认后执行）

模型依赖已安装在本机的 `local/eval-replay-venv`。新环境安装方法见 `docs/v2/本地回放评测.md`。先在另一个终端保持 SSH 隧道：

```sh
ssh -N -L 127.0.0.1:16006:127.0.0.1:6006 jetaime-server
```

从仓库根目录启动：

```sh
# 两题（历史检索、天气），三个版本，各一次：6次Agent执行。
make eval-tool-search TOOL_SEARCH_ARGS='--phase smoke --cases evals/tool-search/cases.reviewed-v1.json --model-profile cloud-qwen-max --max-tasks 6'

# 先检查冒烟结果，再运行20题×3版本×3次：180次Agent执行。
make eval-tool-search TOOL_SEARCH_ARGS='--phase full --cases evals/tool-search/cases.reviewed-v1.json --model-profile cloud-qwen-max --max-tasks 180'
```

两阶段合计186次Agent执行。`--max-tasks` 限制计划的Agent执行数，**不是模型请求数或人民币费用上限**：发现轮次、历史问答和总结均可能增加模型调用。模型和费用范围需在执行前确认。每阶段按A→B→C顺序运行，同步SDK逐题执行，固定模型、不启用fallback和自动重试。缓存和负载会影响耗时；需要确认稳定差异时，以新的实验复测并控制这些因素。

正式命令会将合成问题、参考答案、最终回答和指标上传到 Phoenix；默认不上传 Trace 正文。完整模型输入和执行步骤留在本地私有报告中。地图、历史和提醒使用隔离依赖，不访问业务数据库、真实高德或实际提醒投递。主模型服务仍会接收合成上下文。

默认草稿会在任何构建、模型调用或 Phoenix 上传之前被在线入口拒绝。`run` 还要求显式模型profile和足够的任务上限；这些是防止误跑的入口检查，不能替代对题集、上传范围与预算的确认。

当前本地评测专用设置 `local/eval-settings.json` 将被测 Agent 和语义裁判都选为 `cloud-qwen-flash`（`qwen3.7-flash`），业务模型仍读取原有默认值。没有此文件的环境仍须显式提供任务 profile。上例的 `--model-profile` 会覆盖本地设置；改用 Flash 时将其替换为 `--model-profile cloud-qwen-flash --judge-profile cloud-qwen-flash`。`--no-judge` 可关闭语义裁判，原规则评分保留。裁判每题最多一次请求，另记 token/耗时；`--max-tasks` 仍仅限制 Agent 执行数。

裁判综合审核参考、截止点前证据、实际工具结果和最终卡片；未知/未评不能算通过。汇总增加独立的 judge 状态、维度分和用量；人工审核表继续保留。详见[本地回放评测](../../docs/v2/本地回放评测.md#评测专用模型与语义裁判)。切换模型后另建实验输出目录，在同一轮 A/B/C 中固定模型，不把新旧模型结果拼成同一次对照。别名可能随服务商更新，记录每次冻结配置与响应中的实际模型名。

评分v2将Flash原始语义分与卡片证据、提醒一致性和工具约束规则分开呈现；`semantic_quality` 保留模型判断，`calibrated_quality` 综合规则与语义，汇总同时显示两者。提醒项由代码检查，不会因禁止地图工具被调用而扣提醒分。旧v1评分保留，不能与v2直接汇总。规则实现哈希也纳入冻结清单；结构化字段覆盖范围和证据缺失处理见上述说明。

## 4. 查看结果

在 Phoenix 的 Datasets → 对应数据集 → Experiments 中比较 A/B/C。相同阶段的三个实验复用同一冻结数据集；冒烟是完整题集的独立子集，使用不同数据集。Trace 项目为 `jetaime-eval`。

每阶段本地输出包含：

- `comparison.json`：分支提交、回放代码/二进制/模型配置/套件哈希及执行范围。
- `A/`、`B/`、`C/`：原始单题报告、实验和数据集ID。失败时也保留已经完成的报告。
- `summary.json`：总体与按场景分类的断言、指标、失败和缺失执行。
- `report.md`：执行覆盖、输入token及耗时概览。
- `manual-review.md`：逐题逐次人工复核表，附本地报告路径和Trace ID。

提醒题可在 `expected.reminder` 配置严格业务断言：`operations` 为模拟写入次数（拒绝他人提醒时为 0）；`action`、`date`、`time`、`titleContainsAll` 校验提交参数；`resultStatus`、`reminderStatus`、`userId` 校验实际业务结果，日期、时刻和标题也核对保存值；`sourceMessageIds` 校验工具验证后的来源，包含后端补入的当前请求。空字符串明确表示缺字段，未配置的字段不计分。新增 `reminder_operations`、`reminder_arguments`、`reminder_result` 同时进入本地统计和 Phoenix；没有相应期望或题目未审核时不记业务分。

2026-11-01 的真实 11 题修订版位于私有目录 `local/data/evals/tool-search-real-20261101-reviewed-r2/`。它继承原审核口径，补齐提醒断言和玉米检索证据，并移除 R20 对字面“成都”的要求；语义审核仍须检查费用归属、行程条件与未来信息泄漏。r1 题集和全部运行结果保持原样。修复已同步到 A/B/C 本地工作目录；正式对照前须冻结新的代码提交，保持相同公共修复，只比较工具发现策略。

中断后可重新汇总，已有人工笔记会保留：

```sh
python3 scripts/tool_search_eval.py summarize --output-dir outputs/evals/tool-search/<run>
```

先核对每题重复次数是否完整，再看质量和成本。未知token保持null并报告覆盖数；只对供应商用量完整、同一题集的执行比较真实token，不能把字节估算当token，也不能只比较成功题而忽略失败题。耗时看报告中的Agent `durationMs`（p50/p95），不是Phoenix包含进程启动和Trace flush的task耗时。

工具/来源/关键词断言及人工语义检查全部通过后，再按确认的验收线作选型。20题属于开发回归集；上线前还需要未参与方案调优的真实场景验收。
