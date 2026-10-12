# 聊天评测集的版本与变更管理

## 当前状态

用户导入的441条消息是原始证据。评测集从中截取片段，包含17条问答和6条待办提取；未新增聊天内容。所有参考均为AI编写、待人工审核（reviewed=false）。问题被用户确认或修改，不代表参考答案也已审核。

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

问答逻辑仍是qa-v1。v2/v3/v4首次修复验证各只测6条；随后8e938583用23条完整版每条重复3次补充检查。全部参考仍待审；执行/引用成功及重复输出一致不等于正式语义质量通过，也不证明生产环境稳定性。详细输出和开销见 [三次复测报告](../local/data/chat-eval/repeat3-report.md)。

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

新运行自动生成local/data/evals/output/snapshot-<runId>/：cases.json、manifest.json、被测Go源码、runner、评估器和Schema。manifest记录案例/源码哈希、Git状态、模型digest、服务版本、上下文、超时、重复次数和裁判配置；summary关联这个目录、原始结果和Phoenix实验。输出默认被Git忽略，不能只依赖Git留存：需要长期保留的运行，将整个snapshot、summary和结果一起归档。真实聊天的冻结案例、run-registry、报告和源码快照仅在本地保留，相关目录已被Git忽略；仓库只保留评测代码、Schema和管理说明。新克隆需要自行提供本地聊天及案例文件。

生成旧实验报告必须明确指定旧案例包，不能默认读取当前工作稿：

```sh
python3 scripts/report-chat-eval.py local/data/evals/output/experiment-340d01bb.jsonl \
  --cases local/data/evals/versions/chat-v1.1-extract6-draft-r1/cases.json \
  --output local/data/chat-eval/extract-v4-report.md
```

当前管理与本地快照流程已验证；完整产品评测说明见 ../docs/v2/Phoenix评测说明.md。
