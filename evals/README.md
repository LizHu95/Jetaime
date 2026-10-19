# 聊天评测集的版本与变更管理

## 当前状态

用户导入的441条消息是原始证据。评测集从中截取片段，包含17条问答和6条待办提取；未新增聊天内容。初稿参考为AI编写。用户于2026-10-12明确确认全部参考审核通过，当前工作稿为reviewed=true，已保留chat-v1.1-reviewed-r1及其extract6子集，当前工作稿进一步强化为reviewed-r2；历史草稿与评分保持原状态。

`local/data/evals/cases.json` 是工作稿。正式对比使用 `local/data/evals/versions/<suiteVersion>/cases.json` 的冻结快照；不要覆盖、重新生成或直接修改冻结目录。原始输入文件和调试台状态不因评测而变化。

2026-10-12将聊天数据、报告、实验输出和Phoenix数据库统一迁至local/data。历史实验中的旧路径保留原值以维持快照哈希，路径映射见local/data/migration-20261012.json；旧重复实验结果可由报告脚本自动解析。包含真实参考内容的初稿生成器位于local/data/evals/prepare-chat-evals.py。

## 分开记录的版本

| 对象 | 版本/指纹 | 何时更新 |
| --- | --- | --- |
| 原始聊天 | sourceSha256、原消息ID、视频位置 | 转录校正、账号归并或补全媒体证据 |
| 案例输入 | dataVersion、inputSha256 | 新增/删减问题、修改问法、修改片段、身份或初始状态 |
| 参考答案 | referenceVersion、referenceSha256 | 修改参考、来源、评分断言或审核状态；参考修订号在数据版本内计数 |
| 案例整体 | suiteVersion、casesSha256 | 输入或参考任一改变，生成新的冻结包 |
| 被测逻辑 | qa-v1 / extract-v1…v4、workflowSourceSha256 | 提示词、Schema、状态核对或代码发生变化 |
| 一次实验 | Phoenix experimentId、runId | 每次执行；同版本重复运行仍是不同实验 |

语义版本是供人理解的标签，SHA256验证实际内容；二者都要记录。Git HEAD不足以说明代码版本：这四次历史实验的HEAD相同且gitDirty=true，因此必须结合源码哈希和当时的模型请求。未进行Git提交。

## 已发生的案例变更

| 冻结包 | 基于哪个版本 | 改了什么 | 数量 |
| --- | --- | --- | --- |
| chat-v1.0-draft-r1 | 用户导入聊天 | 初稿：17 QA、6 extract | 23 |
| chat-v1.1-draft-r1 | chat-v1.0-draft-r1 | 第1题改为“什么时候健身的，什么时候理发的”，参考补充计划与实际执行的区别；第5题从“谁转账”改为“给L转账多少钱”，同步参考、类别和案例ID。其余未改 | 23 |
| chat-v1.1-extract6-draft-r1 | chat-v1.1-draft-r1 | 原样筛选6条extract，无新增/修改输入和参考 | 6 |

初稿从已知两处变更恢复，其整个文件哈希与最初离线检查记录 `778c06658c…` 完全相同；不是凭空重写。v1.1完整版哈希 `47cc6ed16a…`；6条子集哈希 `763b52e5be…`。完整哈希在各目录manifest.json中。

第5题改了评测目标，所以使用新ID `qa-transfer-amount`；旧ID `qa-merged-account-attribution`保留在v1.0，不用于跨版本直接比较。纯问法变化而目标不变时可沿用ID。

## 已发生的逻辑变更与实验

模型均为qwen3.5:9b，同一模型digest；上下文16384、超时300秒、每例1次、未启用模型裁判。历史配置、源码哈希和链接均已从Phoenix读取核对，保存在run-registry.json。

| runId | 案例包 | 提取逻辑 / 基于版本 | 改动与观察 |
| --- | --- | --- | --- |
| 0fabbf16 | v1.1完整版23条 | extract-v1 / 原逻辑 | 基线：健身归错人且漏理发；邀请当承诺；已买面包仍提取 |
| d280d787 | v1.1的extract6子集 | extract-v2 / v1 | 强化发送者、单项拆分、承诺及后续状态；前两类改善，面包仍误提取 |
| 2a9ba1d1 | 同一extract6子集 | extract-v3 / v2 | 增加独立状态核对，只保留active；面包排除，但未来安排被过度过滤 |
| 340d01bb | 同一extract6子集 | extract-v4 / v3 | 明确未出现完成证据的计划保持active，省略主语结合本人计划；面包排除，骑回家和做英语保留 |
| 8e938583 | v1.1完整版23条，每条3次 | 当前extract-v4 / qa-v1 | 69次执行和引用格式检查成功；23题三次输出一致，51次问答与基线一致；三类已知提取错误未复现。包含后续字段校验，源码哈希与首次v4不同，本次已保存源码快照 |

问答逻辑仍是qa-v1。v2/v3/v4首次修复验证各只测6条；随后8e938583用23条完整版每条重复3次补充检查。全部参考仍待审；执行/引用成功及重复输出一致不等于正式语义质量通过，也不证明生产环境稳定性。详细输出和开销见 [三次复测报告](../outputs/evals/reports/repeat3-report.md)。

历史运行没有保存完整当时源码：现在能核对源码哈希、原模型请求/提示词、输入、结果和配置，不能声称已能完整重建每一版Go程序。原模型请求保存于historical-runs/；新运行会自动保存源码快照。当前工作代码还包含之后加的字段校验和测试修复，不等同于历史v4实验源码哈希。

## 后续修改规则

1. 从冻结包复制到工作稿；记录父版本。新增数据或问法变化改dataVersion；参考或审核修改改referenceVersion。任何修改都冻结为新suiteVersion。
2. 标注者必须核对原文、引用、片段截止点及允许的替代答案。只把核对过的案例设为reviewed=true；对转录有歧义的案例保持待审。
3. 用freeze脚本创建新目录。脚本拒绝覆盖已有目录，manifest同时记录输入/参考/整体哈希、父版本、变更和审核数。
4. 对比代码时固定同一个案例包、模型digest、配置和评分版本；不能改参考后回头用新答案给旧实验重新打分，再称其为原评分。
5. 修过的这6条归为开发回归集。后续留出独立、未参与调参的案例做验收，避免只针对已知题优化；当前还没有独立留出集。

冻结示例（准备好新工作稿后执行，不能原样重用已存在版本）：

```sh
python3 scripts/freeze-chat-evals.py --version chat-v1.2-draft-r1 \
  --data-version chat-v1.2 --reference-version draft-r1 \
  --parent chat-v1.1-draft-r1 --change '填写实际输入变更和参考变更'
```

固定版本执行：

```sh
make eval EVAL_ARGS='--cases local/data/evals/versions/chat-v1.1-extract6-draft-r1/cases.json --suite-version chat-v1.1-extract6-draft-r1 --timeout 300 --context-tokens 16384'
```

新运行自动生成outputs/evals/snapshot-<runId>/：cases.json、manifest.json、被测Go源码、runner、评估器和Schema。manifest记录案例/源码哈希、Git状态、模型digest、服务版本、上下文、超时、重复次数和裁判配置；summary关联这个目录和原始结果；新运行不上传到旧实验平台。输出默认被Git忽略，不能只依赖Git留存：需要长期保留的运行，将整个snapshot、summary和结果一起归档。真实聊天的冻结案例、run-registry、报告和源码快照仅在本地保留，相关目录已被Git忽略；仓库只保留评测代码、Schema和管理说明。新克隆需要自行提供本地聊天及案例文件。

生成旧实验报告必须明确指定旧案例包，不能默认读取当前工作稿：

```sh
python3 scripts/report-chat-eval.py outputs/evals/experiment-340d01bb.jsonl \
  --cases local/data/evals/versions/chat-v1.1-extract6-draft-r1/cases.json \
  --output outputs/evals/reports/extract-v4-report.md
```

当前管理与本地快照流程已验证；完整产品评测说明见 ../docs/v2/Workflow评测说明.md。

## 2026-10-12耗时优化试验

基于extract-v4尝试v5（删去状态核对解释）和v6（最多24字解释），同一已审核extract6集各运行3次。观测平均耗时约4.26/4.32秒，旧基线约10.07秒；但骑回家被错误判为已完成或出现不一致过滤，所以两个版本均未部署，当前保留v4。缓存与机器负载未控制，不能把全部提速归因于修改。见 [试验报告](../outputs/evals/reports/latency-optimization-trial.md)。

## 缓存探索暂不接入

2026-10-12完成状态核对缓存探索，测试和报告保留于代码及outputs/evals/reports/review-cache-check.md。用户决定暂不接入；常规Workflow、make assistant与Phoenix评测默认关闭缓存，使用当前提取版本，不执行缓存模型指纹查询。scripts/check-review-cache.py显式开启experimental-review-cache，专门复现探索结果。

## reviewed-r2逐行动断言

按用户要求新增requiredSuggestions/forbiddenSuggestions；每个必需行动须匹配不同建议，标题包含全部关键词，并核对配置的负责人、时间与逐项来源。面包片段明确必须保留骑回家和做英语，禁止已完成购买，不能仅靠总数和来源集合通过。健身/理发也增加逐项检查。

冻结包为chat-v1.1-reviewed-r2及chat-v1.1-extract6-reviewed-r2，输入哈希与r1相同。旧输出只做独立的离线对照，旧评分不改写；v4当前保存输出中也有3条漏提取被发现。规则测试覆盖漏项、同数量错误行动、归属/时间/来源错误、合并行动和禁止行动。9项单元测试通过。报告在outputs/evals/reports/action-assertions-r2.md。

## 最新核对修复：extract-v9-evidence-first

保持r2输入和参考不变，v8加强行动状态关系后17/18参考规则通过；v9将证据和完整reason放在status之前，18/18参考规则通过。只测6个提取案例各3次，没有语义裁判或独立验收集，不能称总体准确率。当前调试台采用v9，缓存仍默认关闭。实验分别为2f7e3695、affc3250，报告位于outputs/evals/reports/status-relations-fix-report.md。

## 2026-10-14：录屏执行证据边界修复

当前执行版本为qa-v4-evidence-boundaries / extract-v19-evidence-boundaries。问答区分截图内身份、分享者与未确认问句；提取保留v9提示词主干，在程序中校验AI来源、真人提醒请求，以及有条件补查短回复接受。已校验的接受关系在片段末尾且没有检测到重复任务时直接保留；后续消息或已有重复任务仍独立核对。AI识别目前依赖fixture昵称明确标注（AI）/(AI)，未新增成员角色Schema。

旧reviewed-r2提取6题各3次，18/18执行、来源与参考规则通过。新录屏四个修复目标各3次逐输出核对：截图归属及问句返回证据不足，叫起床归给接受者，面试只保留原始提醒请求。新参考仍待审核，未修改评估器或旧评分，未启用语义裁判，最终版未重跑新24题完整版或旧17题问答。家务隐含承诺边界未扩展。短回复补查增加条件式模型调用，耗时需结合缓存与机器负载观察。报告与实验快照保留在outputs/evals/reports/recordings-20261012-execution-fix.md及outputs/evals/。

## 云模型与启动 profile

任务模型与调试台共用 Eino 调用层及默认模型配置。统一入口自动加载 `local/.env`，读取 `local/models.json` 的 `default_profile`：

```sh
make eval
# 临时切换模型或指定其他配置：
make eval EVAL_ARGS='--model-profile local-qwen'
make eval EVAL_ARGS='--model-config local/other-models.json --model-profile cloud-qwen'
```

配置路径相对仓库根目录。直接模型参数保留兼容；显式指定 provider/model 等参数且未选配置文件或 profile 时使用直接参数。通过直接 `uv run` 执行 runner 不加载环境文件，推荐使用统一入口。

每次实验保存所选公共配置和 `internal/llm` 源码快照；profile 在实验启动后冻结，修改原配置不会影响正在运行的实验。每个工作流统一使用所选模型。语义裁判 `--judge-model` 暂仍使用 Ollama，可与任务模型独立配置。

`--check-services` 验证配置、密钥存在性及所需本地模型，云端不发送推理请求，因此不代表云调用成功。云配置不会要求安装同名 Ollama 模型。默认不启用语义裁判，也不自动切换模型。目标模型不支持原生 Schema 时，显式选择 `json_object`；模型效果需用 reviewed 案例另外评测。
