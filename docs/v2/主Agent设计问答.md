# 主 Agent 核心设计问答

更新日期：2026-10-25。

本文记录仓库当前实现与扩展边界，不代表线上已部署。关联文档：[双人群聊接口](双人群聊接口.md)、[历史向量化与增量记忆](历史向量化与增量记忆.md)、[Workflow 评测说明](Workflow评测说明.md)。

## Q1：当前采用什么总体策略？

一个主 Agent 选择并执行工具。问答和行动事项的文字总结统一交给 `answer_question`；正式群聊的提醒操作交给 `set_reminder`。`extract_todos` 已移除默认注册，实现暂留。五类沟通障碍已支持后端确定路由、按需加载内置 Markdown Skill。当前没有独立 QA Agent、提取子 Agent、通用 Skill 动态发现或热加载；模块需要独立规划和持续调用多个工具时，再考虑拆 Agent。

```text
HTTP 保存消息与 AI 任务
  → jobs.Worker 领取任务、固定授权与原提问截止序号
  → 按入口加载上下文：主动请求的小窗口 / 被动监听的最近 N 条
  → execution.Service（身份、Trace、入口编排）
      ├─ 主动：用户 @AI / 快捷提问 / 既有请求补全 → main
      └─ 被动：普通群聊 → intervention.Judge.Discover
          ├─ skip → 静默结束
          ├─ intervene → 携带目标、窗口证据与操作权限 → 同一个 main
          └─ verify → 一轮定向检索与原文回查 → Judge.Decide
              ├─ 无需干预 / 无补充证据 → 静默结束
              └─ 需要干预 → 携带目标与近期、历史证据 → 同一个 main
  → main：根据已注册工具返回 name / arguments
  → tools.Registry：校验、超时、Trace、分发
      ├─ answer_question
      │   → QA 上下文函数 → 必要时 memory.Search → 回查原文
      │   → 问答模型 → 结构与引用校验 → 最终回答
      └─ set_reminder（正式群聊启用）
          → reminder.Service → 提醒草稿、保存、修改或取消
  → Worker 原子保存回复与完成状态 → 通知客户端
```

这里的主动、被动以用户为视角：用户主动唤起直接进入主 Agent；AI 被动监听普通聊天时先判断是否干预。兼容现有接口与数据库，后者仍使用 `triggerMode=proactive`（AI 主动介入）。

[`execution.entry`](../../services/api/internal/ai/execution/entry.go) 负责入口分流、一次只读核验和判断结果交接；[`intervention.Judge`](../../services/api/internal/ai/intervention/judge.go) 以独立提示词执行候选发现和补证后的最终判断，不执行业务工具或调用主 Agent。N 当前为 30，由 `ai.InterventionMessageLimit` 统一约束读取与判断窗口。5 秒等待、群开关和新消息取消旧任务仍由聊天领域负责。候选输出 `skip / intervene / verify`。verify 必须提出当前事项、一个核验问题和近期疑点来源；编排层最多调用一次 [`HistoryEvidenceProvider.Retrieve`](../../services/api/internal/ai/intervention/history.go)，通过 `SearchDocuments` 搜索当前群的活跃记忆与历史文档（最多 5 个），随后通过 `SourceEvidence` 读取最多 20 个来源 ID：直接来源优先，周边各 2 条在剩余额度内补充，总计最多 80 条。复用话题筛选前的首次 30 条窗口，再通过 `UnprocessedMessages` 补充记忆游标后、截止序号内且尚未持有的用户原文（最多 30 条，排除条件在 LIMIT 前生效）；不再固定重读最近 40 条。仍未实现同事项的专项后续修订检索，未找到修订不能证明旧约定持续有效。摘要只用于定位来源，不直接作为事实输入模型。核验读取最多 15 秒，最终新增原文最多 50 条，保留完整消息，条数超限时标记截断；不再设置独立字节上限，整个最终判断请求仍受模型上下文预算约束，超限时在调用模型前拒绝；这些均是上限，不保证历史覆盖完整。

检索全部使用服务端群 ID、请求人和 `context_end_seq`，不能由模型改变。最终判断只有原文，没有摘要；必须同时引用近期疑点和新增历史来源，且禁止 `execute_request`。约束、目标、风险障碍至少两条不同来源，避免用一条消息同时充当基准和偏离证据。核验能力未配置、缺少可信截止序号或未找到新增原文时静默；读取或模型失败时不进入主 Agent，不向群里发送错误。最终证据合并回执行上下文，并通过 RelatedMessageIDs 固定保留；判断通过后主 Agent 查证仍可静默。检索覆盖始终标记为 partial，不能宣称已排除所有历史修订。

## Q2：主 Agent 当前负责什么？

[`ExecuteMain`](../../services/api/internal/ai/agent/main/main_agent.go) 负责工具选择、执行和受限循环，不承担入口分流或前置干预判断，不再包含 QA 提示词、答案生成、历史召回或引用校验。主提示词版本为 `main-v24-communication-repair`。

模型只返回工具调用：

```json
{"name":"answer_question","arguments":{"question":"我们之前定了去哪儿？","context":"history"}}
```

工具名称、描述与参数 Schema 从统一注册表加载。未知工具、未知参数和旧的直接回答协议都会被拒绝。普通自动问答调用两次对话模型：一次选择工具，一次 QA 生成答案；QA 结果直接作为最终结果保存。

## Q3：日常问答和历史问答如何统一？

统一在 [`tools/qa`](../../services/api/internal/ai/tools/qa/tool.go) 的 `answer_question` 中，使用不同的上下文策略：

| context | 信息选择 | 回答要求 |
| --- | --- | --- |
| `general` | 当前问题与起始上下文，不检索历史 | 可以使用一般知识；涉及群聊事实时引用原文 |
| `recent` | 起始上下文加截至原提问的最近 40 条消息 | 根据近期原文回答；证据不足时说明不足 |
| `history` | 近期原文加历史检索、来源原文及相邻消息 | 历史事实有来源；不把提议当决定 |
| `mixed` | 与 history 相同，并允许一般建议 | 区分群聊事实与建议 |

旧 `qa/help` 模式入口已删除，问答工具继续由 Main Agent 按需选择。问答提示词与输出 Schema 位于 [`qa/prompts.go`](../../services/api/internal/ai/tools/qa/prompts.go)，版本 `answer-question-v2-hybrid`；问答尚无独立 Skill 加载，当前内置 Skill 只覆盖五类沟通障碍。

## Q4：不知道用户意图时，默认上下文怎么选？

先构建固定的小上下文，不先加载整段历史：

| 信息 | 默认规则 |
| --- | --- |
| 系统规则、工具目录、调用 Schema | 程序模板与工具注册表 |
| 当前提问 | 完整 ID、发送者、发送时间、正文和问题 |
| 身份 | 当前群组、提问人、成员 ID 与昵称、AI 身份 |
| 时间 | 原提问时间、执行时间、`Asia/Shanghai` 时区 |
| 直接关联原文 | 按已有 `reply_to_id` 补取并保留 |
| 近期背景 | 从近到远选择完整消息，最多 12 条、合计 4 KiB |
| 覆盖信息 | 选择策略、是否省略更早消息 |

当前问题与直接引用不占背景的 4 KiB，但仍计入总请求预算。4 KiB 是序列化字节预算。默认不带全部待办、长期记忆或历史摘要。

## Q5：信息装入与裁剪顺序是什么？

保留当前提问和直接引用，再装入近期背景。背景下一条完整消息放不下时停止，不截断正文。主 Agent 调用前将实际工具目录与 Schema 计入预算，必要时继续删除可选背景。

QA 独立构建自己的上下文：保留当前问题与直接引用，补充近期和检索原文，按预算选择完整证据，最多 80 条，再按发送时间排列，当前问题放在最后。检索结果可能被预算裁减，不能宣称覆盖全部群聊或所有后续修正。

## Q6：上下文如何通过函数扩展？

起始上下文使用 [`aicontext.ContextInjector`](../../services/api/internal/ai/context/policy.go)，由任务执行器装配；QA 使用自己的 `qa.ContextProvider`：

```go
type ContextProvider func(context.Context, ContextRequest) (ai.Context, error)
```

QA 请求包含服务端 Scope、问题、上下文模式与预算。生产装配传入 `qa.HistoryContext(memoryService, memoryStore)`；本地调试和离线评测默认使用传入快照，不查询生产数据库。

函数返回后仍执行群组/请求人、必需原文、提问元数据及最终预算检查。函数收到上下文副本，不能通过原地修改绕过必需信息检查。

## Q7：历史检索具体怎么取？

QA 内部通过 `memory.Service.SearchDocuments` 查询当前群的 `history` 文档：向量与关键词混合检索，RRF 合并，最多取 5 个片段；来源 ID 去重后最多 20 个，`SourceEvidence` 优先读取直接来源，再补前后各 2 个 seq 范围内的消息，总计最多 80 条。随后通过 `UnprocessedMessages` 补读记忆游标后、原提问截止内且尚未持有的用户原文，最多 30 条；不再固定读取最近 40 条。最终预算选择优先保留请求、必要引用和直接来源，其余背景按真实时间从新到旧选择，整体最多 80 条，不截断原文。

摘要或片段只用于定位来源；答案模型使用回查原文，不能引用记忆文档 ID。当前不额外暴露 `memory_call/search_history` 工具，也不提供 memory HTTP 接口。

相邻消息不保证包含全部更正，当前没有日期/人物结构化参数提取、独立多轮补证或 Rerank。向量未就绪时只可能有关键词和近期证据，不能保证旧事实一定召回。

## Q8：超出模型容量时怎么处理？

每次独立调用前，本地检查系统提示词、完整消息、角色、Schema、协议余量和 2048 输出预留。必需信息仍超限时直接返回错误，该次请求不经过模型；先前已完成的路由调用不会撤销。

起始上下文、主 Agent、QA 和模型适配器都有检查。QA 在检索前先检查必需信息，避免必然超限的请求继续调用 Embedding。预算使用保守 UTF-8 字节估算，尚未使用专用 tokenizer。见 [`llm/budget.go`](../../services/api/internal/ai/llm/budget.go)。

## Q9：怎样防止跨群与重试读取未来消息？

群组、请求人和 `context_end_seq` 来自后端任务，通过 `tools.Scope` 注入，不在模型参数里。Worker 将原始截止序号传到 QA，近期原文、来源回查和搜索均受 `seq <= context_end_seq` 限制。

检索会排除包含截止之后来源的整个文档，防止后来生成的片段泄漏未来内容。数据库查询再次校验群成员与群状态。重试沿用原截止范围；相对日期以 `request.sentAt` 为基准，执行时间不改变原问题含义。

## Q10：QA 如何校验答案和引用？

工具严格解码 `status/answer/sourceMessageIds`，状态为 `answered`、`insufficient_evidence` 或 `conflicting_evidence`。非 general 模式的有依据回答必须有引用；引用必须属于最终传给 QA 的原文集合，未知、重复和跨群来源都会失败。

`basis` 由实际引用与模式生成，区分 `general/history/mixed`。`HistoryScope.historicalSearch` 标识是否检索，避免检索过后还显示“未检索更早记录”。这些校验保证来源合法，不等于证明模型结论语义正确，仍需案例评测。

## Q11：工具、skill 和子 Agent 的边界？

| 形式 | 职责 |
| --- | --- |
| Tool | 执行明确操作，可包含独立上下文和多次模型调用 |
| Skill | 按需加载任务规则、步骤与证据要求 |
| 子 Agent | 在独立上下文中自主规划、持续调用工具并调整步骤 |

`answer_question` 当前是工具；历史与日常问答是内部策略。不能仅因工具调用模型，就把它视为独立 Agent。

## Q12：能力路由在哪里声明？

- [`execution/service.go`](../../services/api/internal/ai/execution/service.go)：装配判断器、共享主 Agent 和入口编排层，将入口装配为统一的 execution.Service，使用调用方提供的工具注册表。
- [`execution/entry.go`](../../services/api/internal/ai/execution/entry.go)：区分主动、被动入口；被动判断通过后交接目标、证据和写操作权限，合并判断与执行步骤。
- [`skills/communication.go`](../../services/api/internal/ai/skills/communication.go)：发现并校验标准 Skill 包，从 YAML 元数据构建五类沟通障碍的路由，按已注册身份加载内置策略；识别器不选择文件路径或生成策略正文。
- [`intervention/judge.go`](../../services/api/internal/ai/intervention/judge.go)：候选发现和核验后最终判断，每阶段最多一次结构修复与证据校验；正文分别维护在 recognition.md 和 verification.md。
- [`tools/registry.go`](../../services/api/internal/ai/tools/registry.go)：统一工具定义、名称白名单、调用协议、超时、Trace 与执行。
- [`qa/tool.go`](../../services/api/internal/ai/tools/qa/tool.go)：声明 `answer_question` 的描述、参数与处理函数。
- [`main_agent.go`](../../services/api/internal/ai/agent/main/main_agent.go)：执行主 Agent 工具选择与受限循环。
- [`execution.Service`](../../services/api/internal/ai/execution/execute.go)：统一执行 main，校验执行身份与上下文，记录 invocation ID 和 Trace。

添加工具时在装配处注册即可，不向聊天 Worker 添加意图分支。

## Q13：待办提取现在需要单独 Agent 吗？

不需要。`extract_todos` 已下掉默认工具注册，主 Agent 当前不会调用它；行动事项请求由 QA 作文字总结。暂留的提取实现包装既有固定提取、证据与行动状态核对流程，输出待确认建议，不创建任务或提醒。实现位于 [`tools/extract/workflow.go`](../../services/api/internal/ai/tools/extract/workflow.go)，工具定义、提取、证据校验与行动状态核对均由 `ai/tools/extract` 封装，主 Agent 可以直接撰写普通回答，通过 respond 校验后返回；需要历史检索或业务操作时才选择相应工具。

直接 `extract` 模式与 `/extract` 命令特判已移除，默认工具目录中也已移除 `extract_todos`。历史 qa/help/extract 任务由迁移 `000016_main_execution_only` 统一改为 main，原提问截止不变，Worker 不再承担旧模式转换；结果中的 `kind=extract` 仍表示待办建议类型，不是可手选的入口。

保留的提取实现按授权快照执行，超限拒绝；精确的检索式提取范围未实现。群聊 `Tasks` 仍为空，不能承诺根据正式任务状态完整去重。

## Q14：工具结果会返回主 Agent 吗？

框架支持非终结工具返回结构化 Data，主 Agent 携带结果继续选择工具，最多 3 步。工具调用继承 Worker 的截止时间，统一执行器另外限制单工具最多 2 分钟，每次模型请求重新检查容量。

当前 QA 与提取都是终结工具：结果直接保存，不再调用主 Agent 改写。这保留已校验的引用并避免第三次调用。框架有受限循环，不代表当前已经实现多能力组合任务。

## Q15：Worker 负责什么？

[`jobs.Worker`](../../services/api/internal/ai/jobs/worker.go) 管理 PostgreSQL 任务领取、租约、超时、重试、Trace、结果落库。两个执行 goroutine 支持不同群并行、同群串行。完成时校验 attempt 与租约，阻止旧执行者重复写入。

消息快照适配在 [`chat/context_store.go`](../../services/api/internal/chat/context_store.go)，任务提交在 [`chat/job_store.go`](../../services/api/internal/chat/job_store.go)。问答策略位于 QA 工具，不在 Worker。

## Q16：HTTP、WebSocket 和数据库各负责什么？

HTTP 发送、读取和重试；数据库保存权威状态与任务；后台执行完成后，WebSocket 通知客户端补拉。消息回复与任务成功原子提交，不依赖浏览器连接。模型当前非流式。工具与 memory 使用进程内接口，无新增 HTTP 路由。

## Q17：当前实现边界？

| 能力 | 状态 |
| --- | --- |
| main + 统一工具目录与受限循环 | 已实现 |
| QA 提示、模型生成、证据校验独立于 main | 已实现 |
| 默认函数注入与逐次容量保险 | 已实现 |
| QA 历史向量/关键词检索、原文与相邻消息回查 | 已实现，带原提问 seq 边界 |
| 旧 qa/help 模式 | 入口已删除，历史任务迁移为 main |
| 固定待办提取工具 | 默认注册已移除，实现暂留 |
| 独立问答 skill 加载 | 未实现 |
| 群聊已有任务状态、完整去重 | 未接通 |
| 提醒创建、修改、取消工具 | 正式群聊注册 set_reminder，调用提醒领域 Service |
| 创建正式待办任务工具 | 未实现 |
| Rerank、多轮补证、复杂组合任务 | 未实现 |

验证覆盖工具路由、预算拒绝、检索引用、后发消息隔离与旧模式拒绝。真实模型召回质量和回答正确率仍需独立评测。

## Q18：后续怎么扩展？

先补真实案例评测，再按需求增加：QA 内部的人物/时间过滤和补证、按需加载问答 skill、正式任务状态读取与写操作工具。如果主 Agent 还需要独立使用历史数据完成其他任务，再注册独立历史检索工具。模块需要自主规划和多轮协作时，再拆子 Agent。

本次没有修改数据库结构、重置记忆游标或改变向量化 pipeline，不需要重新处理已完成历史。

候选发现同时输出 `topicContext={topic,continuity,messageIds,anchorMessageIds}`，continuity 为 `new_topic/continuation/uncertain`。结合事项、时间、指代和直接引用判断连续性，跨天不自动接续，同一天也不自动连续。skip 时可为空，其他动作必须选择当前话题原文，并将触发消息及定义事项的原文列为锚点。编排层按该范围筛选近期上下文，锚点通过 RelatedMessageIDs 固定保留，不受最多6条背景裁剪影响；介入目标和核验疑点的近期证据必须属于此话题。推荐地点绑定来源消息，仅保留来源也在当前话题中的地点，避免昨天的餐厅推荐污染今天的出差问题。主 Agent 同时接收明确话题名称；历史核验仍检查补证是否适用于同一事项。

主动 @AI 请求仍直接进入主 Agent，不经过被动干预判断。起始上下文缺少“我要去吗”“我考虑的是……”等追问所指的事项时，主 Agent 可调用只读 [`read_recent_messages`](../../services/api/internal/ai/tools/recent/tool.go)：按原提问截止序号补读本群最近30条消息中的真人原文，以及当前显式引用的AI原文，不依赖记忆模型或向量索引。新原文只在执行上下文保留一份，工具观察仅携带ID和覆盖标记，后续引用校验可以使用这些来源；整个请求仍受模型上下文预算约束。跨天是否接续以实际事项、指代、参与者和约束为主，时间只是辅助，不要求用户明说“接着昨天”。决策建议需要围绕当前作者给有条件的倾向和下一步，不把出差同行问题转为附近休闲活动，也不把另一成员的报销当成当前作者的费用减免。


## Q19：五类情感沟通障碍如何主动帮助？

在原八类协作障碍之外，识别器支持理解错位、支持错配、表达升级、节奏冲突、修复受阻，对应 `understanding_mismatch/support_mismatch/communication_escalation/pacing_conflict/repair_blocked`。群主动开关开启后，充分证据和具体帮助价值即可触发直接发言，无需成员先向AI求助或点击确认。每类要求至少两位不同真人的原文；程序校验说话人和来源，具体语义仍由模型判断。

理解对齐以最新回应为准：首次澄清尚待回应、对方已开始理解或只是理解后不同意时不插话；原意已明确、对方在澄清后仍明显曲解时，可以直接介入，不要求表达者再重复一次，也不要求双方互相攻击。证据保留原始表达、必要澄清和后续偏离回应，Skill先温和复述原话与解读之间的差异；原意清楚时无需追问为什么这样想，不推断“只是关心你”等未表达动机。仅有“不了解我”“想控制我”等指责不自动追加表达升级标签，避免无充分证据的次要障碍干扰本轮目标。

具体家务请求与另一方明确表达的工作负担、分工边界冲突归入既有 `conflict`，不因出现情绪就强套 `support_mismatch`；后者限于明确的倾听、安慰或共同解决问题需要与回应不匹配。介入目标须接住双方已表达的关注点，再提出一个共同协商的下一步，不能预设某方先执行任务，也不将一方认为分工不均当作已核实事实。

主要沟通障碍放在 `breakdowns` 首位。识别、必要补证及冷却去重通过后，由后端选择一个 Skill：

| 主要障碍 | Skill ID | 独立策略正文 |
| --- | --- | --- |
| understanding_mismatch | check-understanding | [理解对齐](../../services/api/internal/ai/skills/communication/check-understanding/SKILL.md) |
| support_mismatch | clarify-support | [支持需要确认](../../services/api/internal/ai/skills/communication/clarify-support/SKILL.md) |
| communication_escalation | focus-on-event | [聚焦事件与请求](../../services/api/internal/ai/skills/communication/focus-on-event/SKILL.md) |
| pacing_conflict | negotiate-pause | [沟通节奏协调](../../services/api/internal/ai/skills/communication/negotiate-pause/SKILL.md) |
| repair_blocked | clarify-unresolved-need | [修复诉求澄清](../../services/api/internal/ai/skills/communication/clarify-unresolved-need/SKILL.md) |

每份 Skill 是独立目录，使用 [Agent Skills 标准](https://agentskills.io/specification) 的 `SKILL.md` 入口和 YAML frontmatter；名称与目录一致，使用小写字母、数字和连字符，description 说明能力、触发条件及易混淆场景。示例：

```yaml
---
name: negotiate-pause
description: 尊重明确暂停请求并协调恢复沟通；用于一方要求暂停、另一方明确坚持立即继续且尚未协调的互动。
metadata:
  version: "v2"
  jetaime-breakdown: pacing_conflict
---
```

`metadata.version` 与 `metadata.jetaime-breakdown` 是项目要求，放在标准允许的自定义 metadata 中，不是 Agent Skills 的通用必填字段。加载器从这些字段构建路由，不在 Go 中重复维护名称、版本或障碍映射。启动时只保留发现的元数据，选中后才加载正文；元数据非法、目录与名称不匹配或障碍路由重复时启动失败。当前五份策略均短小且自包含，无需创建可选 scripts、references、assets 或 UI 文件。每份正文保留目标、证据、工作方法和停止条件，具体章节是项目组织方式，不是标准强制格式。

标准化版本为 `v2`（此前策略文件格式为 `v1`）；理解对齐和支持需要确认当前为 `v3`，其余三份仍为 `v2`。策略以 `go:embed` 随二进制发布；修改正文或路由须更新其 `metadata.version` 并重新构建。其余八类协作障碍保持现有目标驱动执行；沟通障碍不是首位时不附加其他 Skill，不混装多个策略。`intent` 协议保持不变，不另建 Agent 或业务工具。

编排层将 `SkillRef{ID,Version,Breakdown}` 放入后端拥有的 `RequestContext.Skill`（不参与 JSON 编解码），ID 使用 Skill 的标准 name，不再将工作指令拼入用户问题。主 Agent 请求构造器按该身份读取正文并剥离 YAML 头部，连同通用沟通约束加入系统消息，所有工具回合、参数修复、上下文裁剪和最终收尾均保留该 Skill，并在每次模型调用前计入完整请求预算；预算不足时拒绝调用，不丢掉策略或必要证据。未知身份、错误版本和路径伪造被拒绝。Skill 只指导工作方法，不改变工具注册表、引用验证、群权限或写操作授权。@AI、既有请求补全及 `execute_request` 不加载沟通 Skill。

运行记录包含所选 Skill 元数据，Trace 用 `select_skill` 步骤记录选择，并以 `ai.skill.execute` 包裹主 Agent。其模型、工具及预算子节点携带 `skill.id`、`skill.version`、`skill.breakdown` 和 `skill.selection_reason=primary_breakdown`，关闭正文采集时也可查询；策略正文不作为聊天消息或消息 API 结果返回。不支持动态发现、模型生成 Skill 正文或文件系统热加载。

主 Agent 通常用1至3个短句直接回应，最多一个问题或建议，不输出分类标签、消息ID，不猜测心理，不要求立即原谅或双方各退一步。通用沟通约束对所有只读主动介入生效，包括没有加载沟通Skill的 `conflict`；涉及双方负担时并列接住各自明确关注点，避免只提两人名字却单方面要求一个人改变。成员自行修复、拒绝介入或AI已给同样帮助且没有新卡点时保持沉默；不因无人回应而追问。拒绝与语义修复判断仍限于识别窗口；群级2分钟冷却及24小时规范化话题去重已经持久化，边界见[群级主动 Agent](双人群聊接口.md#群级主动-agent)。暂停恢复时间由成员确认，不自动创建提醒；明确暴力或胁迫优先人身安全。

当模型提出沟通介入，但合法证据仅来自同一位真人时，证据门禁将其转换为成功静默：候选返回 `skip`，核验后判断返回 `shouldIntervene=false`，清空障碍和意图，不进入主 Agent，不重试模型。识别步骤以 `succeeded` 记录，输出中保留 `outcome=candidate_rejected`、`reasonCode=insufficient_participant_evidence`、拒绝原因和模型原始输出，区分于模型主动选择skip；内容采集开启时也可在Phoenix查看这些诊断。这是候选未通过门禁，不是工作流故障。未知或越界消息引用、AI冒充真人证据、格式修复失败、模型调用失败及操作越权仍为错误；先校验全部硬约束，不允许证据不足掩盖其他错误。既有历史Trace不回写。

案例位于 `services/api/internal/ai/intervention/testdata/communication.json`，覆盖7个沟通正例、1个分工冲突正例和13个无需介入的反例。脚本模型测试验证Schema、两位真人证据及识别到直接respond的执行链路；不代表真实模型识别质量。真实识别测试可使用：

```sh
cd services/api
JETAIME_PROACTIVE_MODEL_CONFIG=/absolute/path/to/models.json go test ./internal/ai/intervention -run '^TestRealModelCommunicationRecognition$' -v -count=1
# 真实识别 → 服务端路由 → 主Agent回复；仅注册回复/静默工具，不发送消息或执行业务写操作。
JETAIME_PROACTIVE_MODEL_CONFIG=/absolute/path/to/models.json go test ./internal/ai/execution -run '^TestRealModel(UnderstandingIntervention|BalancedHouseholdIntervention)$' -v -count=1
```

可用 `JETAIME_PROACTIVE_MODEL_PROFILE` 指定配置中的模型。此功能为仓库实现，不代表已经部署。
