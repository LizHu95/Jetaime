# 主 Agent 核心设计问答

更新日期：2026-10-22。

本文记录仓库当前实现与扩展边界，不代表线上已部署。关联文档：[双人群聊接口](双人群聊接口.md)、[历史向量化与增量记忆](历史向量化与增量记忆.md)、[Workflow 评测说明](Workflow评测说明.md)。

## Q1：当前采用什么总体策略？

一个主 Agent 选择并执行工具。问答和行动事项的文字总结统一交给 `answer_question`；正式群聊的提醒操作交给 `set_reminder`。`extract_todos` 已移除默认注册，实现暂留。当前没有独立 QA Agent、提取子 Agent 或动态 skill 加载器；模块需要独立规划和持续调用多个工具时，再考虑拆 Agent。

```text
HTTP 保存消息与 AI 任务
  → jobs.Worker 领取任务、固定授权与原提问截止序号
  → 按入口加载上下文：主动请求的小窗口 / 被动监听的最近 N 条
  → runtime → orchestration.Entry
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

[`orchestration.Entry`](../../services/api/internal/ai/orchestration/entry.go) 负责入口分流、一次只读核验和判断结果交接；[`intervention.Judge`](../../services/api/internal/ai/intervention/judge.go) 以独立提示词执行候选发现和补证后的最终判断，不执行业务工具或调用主 Agent。N 当前为 30，由 `ai.InterventionMessageLimit` 统一约束读取与判断窗口。5 秒等待、群开关和新消息取消旧任务仍由聊天领域负责。候选输出 `skip / intervene / verify`。verify 必须提出当前事项、一个核验问题和近期疑点来源；编排层最多调用一次 [`HistoryEvidence`](../../services/api/internal/ai/intervention/history.go)，搜索当前群的活跃记忆与历史文档（最多 5 个），随后读取最多 20 个来源 ID 的原文及相邻消息，并补最多 40 条近期原文检查修订。摘要只用于定位来源，不直接作为事实输入模型。核验读取最多 15 秒，最终新增原文最多 50 条，保留完整消息，条数超限时标记截断；不再设置独立字节上限，整个最终判断请求仍受模型上下文预算约束，超限时在调用模型前拒绝；这些均是上限，不保证历史覆盖完整。

检索全部使用服务端群 ID、请求人和 `context_end_seq`，不能由模型改变。最终判断只有原文，没有摘要；必须同时引用近期疑点和新增历史来源，且禁止 `execute_request`。约束、目标、风险障碍至少两条不同来源，避免用一条消息同时充当基准和偏离证据。核验能力未配置、缺少可信截止序号或未找到新增原文时静默；读取或模型失败时不进入主 Agent，不向群里发送错误。最终证据合并回执行上下文，并通过 RelatedMessageIDs 固定保留；判断通过后主 Agent 查证仍可静默。检索覆盖始终标记为 partial，不能宣称已排除所有历史修订。

## Q2：主 Agent 当前负责什么？

[`ExecuteMain`](../../services/api/internal/ai/agent/main/main_agent.go) 负责工具选择、执行和受限循环，不承担入口分流或前置干预判断，不再包含 QA 提示词、答案生成、历史召回或引用校验。主提示词版本为 `main-v4-tools`。

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

旧 `qa/help` 模式分别映射到同一个工具的 `history/general`，没有两套问答实现。问答提示词与输出 Schema 位于 [`qa/prompts.go`](../../services/api/internal/ai/tools/qa/prompts.go)，版本 `answer-question-v1`；尚未做动态 skill 加载。

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

QA 内部通过 `memory.Service.Search` 查询当前群的 `history` 文档：向量与关键词混合检索，RRF 合并，最多取 5 个片段；随后按来源 ID 回查原始消息，并补取前后各 2 个 seq 范围内的消息。另补截至原提问最近 40 条原文，覆盖尚未完成索引的近期事实。

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

- [`bootstrap/service.go`](../../services/api/internal/ai/bootstrap/service.go)：装配判断器、共享主 Agent 和入口编排层，将入口以 main 身份注册到 runtime，使用调用方提供的工具注册表。
- [`orchestration/entry.go`](../../services/api/internal/ai/orchestration/entry.go)：区分主动、被动入口；被动判断通过后交接目标、证据和写操作权限，合并判断与执行步骤。
- [`intervention/judge.go`](../../services/api/internal/ai/intervention/judge.go)：候选发现和核验后最终判断，每阶段最多一次结构修复与证据校验；正文分别维护在 recognition.md 和 verification.md。
- [`tools/registry.go`](../../services/api/internal/ai/tools/registry.go)：统一工具定义、名称白名单、调用协议、超时、Trace 与执行。
- [`qa/tool.go`](../../services/api/internal/ai/tools/qa/tool.go)：声明 `answer_question` 的描述、参数与处理函数。
- [`main_agent.go`](../../services/api/internal/ai/agent/main/main_agent.go)：执行主 Agent 工具选择与受限循环。
- [`runtime`](../../services/api/internal/ai/runtime/runtime.go)：按 Agent ID 调度，与工具注册表分工不同。

添加工具时在装配处注册即可，不向聊天 Worker 添加意图分支。

## Q13：待办提取现在需要单独 Agent 吗？

不需要。`extract_todos` 已下掉默认工具注册，主 Agent 当前不会调用它；行动事项请求由 QA 作文字总结。暂留的提取实现包装既有固定提取、证据与行动状态核对流程，输出待确认建议，不创建任务或提醒。实现位于 [`tools/extract/workflow.go`](../../services/api/internal/ai/tools/extract/workflow.go)，工具定义、提取、证据校验与行动状态核对均由 `ai/tools/extract` 封装，主 Agent 可以直接撰写普通回答，通过 respond 校验后返回；需要历史检索或业务操作时才选择相应工具。

直接 `extract` 模式与 `/extract` 命令特判已移除，默认工具目录中也已移除 `extract_todos`。旧排队/重试任务在 Worker 读取后转入 main，原提问截止不变；结果中的 `kind=extract` 仍表示待办建议类型，不是可手选的入口。

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
| 旧 qa/help 模式 | 复用 answer_question |
| 固定待办提取工具 | 默认注册已移除，实现暂留 |
| 独立问答 skill 加载 | 未实现 |
| 群聊已有任务状态、完整去重 | 未接通 |
| 提醒创建、修改、取消工具 | 正式群聊注册 set_reminder，调用提醒领域 Service |
| 创建正式待办任务工具 | 未实现 |
| Rerank、多轮补证、复杂组合任务 | 未实现 |

验证覆盖工具路由、预算拒绝、检索引用、后发消息隔离与旧模式兼容。真实模型召回质量和回答正确率仍需独立评测。

## Q18：后续怎么扩展？

先补真实案例评测，再按需求增加：QA 内部的人物/时间过滤和补证、按需加载问答 skill、正式任务状态读取与写操作工具。如果主 Agent 还需要独立使用历史数据完成其他任务，再注册独立历史检索工具。模块需要自主规划和多轮协作时，再拆子 Agent。

本次没有修改数据库结构、重置记忆游标或改变向量化 pipeline，不需要重新处理已完成历史。
