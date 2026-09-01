# Agent 回答姿态与开箱问答链路整改方案

> 版本：2026-09-01，基于 main `644db39`
> 范围：会话问答链路（RAG runtime）与工作区开箱配置
> 前提：开发期，不考虑兼容性和旧数据；本文档只做分析与设计，不含已实施改动

## 0. 问题定性

用户报告的现象："知识库没有数据时，问 agent 什么都不回答"。排查与产品分析把它拆成**两个独立的缺陷域**：

**缺陷域 A：开箱链路断裂（工程缺陷）**。新工作区从"注册 Agent 应用"到"能完成一次问答"之间存在四层配置缺口，任何一层都会让请求以错误结束（403/502），前端拿不到 answer。与知识库是否为空无关——空库只是让用户必然撞上它。

**缺陷域 B：回答姿态单一（设计缺陷）**。当前 RAG 路径对所有会话聊天施加同一个"严格接地"指令（只准依据检索上下文回答，不足时明说）。但会话在产品定义中是"思考、问答、消息块、笔记同步和派生内容的协作边界"（yxt 契约 §0）——思考（人机共创）与问答（检索）是两种姿态。空知识库的冷启动场景下，共创姿态才是用户预期，而它目前不存在。

## 1. 产品依据

| 出处 | 原文/事实 | 支撑的决策 |
| --- | --- | --- |
| 产品文档-v2 §43 | "把零散经验、工作笔记、**人机共创内容**和外部资料，沉淀为……知识资产" | 人机共创是一等知识来源；会话是知识库的入口，不是出口 |
| 产品文档-v2 §49 | 五出口（博客/公开站点/检索/Agent/API）读同一份已发布资产 | 问答姿态的可信度锚点是"已发布资产+引用" |
| 产品文档-v2 §19.2 | "我们不是单纯的 Agent 工具，因为 Agent 使用的是经过治理的资产，而不是一份**无法追溯的提示词或知识库副本**" | 问答模式必须严格接地，不允许编造 |
| 产品文档-v2 §8.3 | Agent 不能设置 human_confirmed、不能批准发布 | **治理闸门在发布处，不在聊天处**——放开共创不污染知识库 |
| yxt 契约 §0 | 会话定义：思考、问答、消息块、笔记同步和派生内容 | 思考（共创）排在问答之前，会话天生双面孔 |
| yxt 契约 §11.2 | 请求体已有 `response_mode: answer|answer_with_sources|draft_note`（**契约有、代码零实现**） | 契约本来就预留了多姿态，本次落地 |
| v2 §12.5 + §777 | 已发布内容自动进入 Agent 问答，所有出口同源 | 共创产出经 note sync→发布→进入问答的闭环方向 |

**鸡生蛋问题的解法**：会话默认是共创面（蛋）——空库可聊、产出草稿；草稿经人工确认发布变成知识（鸡）；问答姿态 thereafter 有据可查。两个方向都通。

## 2. 现状代码事实（逐条实证）

| # | 事实 | 位置 |
| --- | --- | --- |
| F1 | 建工作区时 `default_resource_model_id` 原样透传、零校验、空即 NULL；两处入口（组织级+内部）行为一致 | organization/workspace_lifecycle.go:54-56；workspace/final.go:80-84；PATCH 侧 organization.go:546,584 同样零校验 |
| F2 | 新工作区默认模型 NULL → 注册应用时自动授权 SQL 带 `AND w.default_resource_model_id IS NOT NULL`，**静默跳过**，agent 一条策略都没有 | httpapi/agent_applications.go（workspaceAgentApplications 内自动授权段） |
| F3 | 自动授权即使执行，actions 只给 `ARRAY['read']`；而 agent 检索范围编译要求 `'query.execute' = ANY(actions)` → `query_scope_forbidden` | agent_applications.go 自动授权 SQL vs query/scope_compiler.go ForAgent（:161 附近） |
| F4 | 自动授权插入的是 **org-wide 策略**（workspace_id NULL），工作区注册的应用获得全组织该模型的读权——范围过宽 | 同 agent_applications.go：`SELECT ... NULL ...` 插入列 |
| F5 | 零可读模型时 agent_bridge 直接 `ErrModelAccessDenied` → 403 `agent_data_access_denied`（映射正确但语义是"没配"） | query/agent_bridge.go:78-80 |
| F6 | `ErrQueryScopeForbidden` **不在** agentRuntimeErrorCode 映射表 → 落兜底 502 `agent_model_failed`，权限问题报成模型失败 | httpapi/handler.go:1396-1411 |
| F7 | 建会话校验默认模型的谓词是 `workspace_id = $3` 精确等值 → 组织级 builtin 模型全部查不到 → 409 `conversation_conflict`（即测试套件 gotcha 21②；C11 开放 API 侧早已接受 org-level） | content/service.go CreateConversation（defaultModel 查询段） |
| F8 | RAG 指令 `fixedRAGInstruction` 硬编码单一"严格接地"姿态；应用表的 `instruction` 列存在但 RAG 路径不读（只有 react 读） | agentruntime/rag.go:21-24；react.go:182-184；表列 integration.agent_applications.instruction |
| F9 | `response_mode` 契约有、代码零引用；chat 请求结构 `agentChatRequest{Query, ConversationID}` 且 `DisallowUnknownFields`——现在按契约发 response_mode 会直接 422 | httpapi/handler.go:177-180 |
| F10 | 响应无 grounded 标记：ChatResult/agentChatResponse 都只有 answer/references 等 | agentruntime/runtime.go:44-58；handler.go:182-188 |
| F11 | 空库且配置正确时，模型确实按指令回答 "The available knowledge is insufficient..."（英文，不跟随提问语言，无下一步建议）——2026-09-01 K8-empty 实测 | agentruntime/rag.go buildKnowledgeContext 的空上下文占位串 |
| F12 | builtin 四模型种子渠道 `agent: enabled=true` 已开启，不是缺口 | db/migrations/0012_builtin_resource_models.sql:63 等 |
| F13 | react/workflow 模式走 runs 不受本次影响；react 的检索工具同样依赖 agent 策略含 query.execute（P0-3 修复同样惠及） | agentruntime/tool_factory.go:34 |

## 3. 目标设计：双回答姿态

| 维度 | co_create（共创，默认） | grounded_qa（问答） |
| --- | --- | --- |
| 定位 | 思考面：帮用户想清楚、结构化、起草 | 检索面：回答"库里已沉淀了什么" |
| 知识使用 | 检索命中作为**参考材料**注入（增强而非约束），零命中不影响回答 | 检索命中是**唯一依据** |
| 引用纪律 | 不产 [S#] 标签；响应 references 恒为空数组 | 必须引用 [S#]；sanitize+validate 照旧 |
| 空库行为 | 正常对话（这就是冷启动的解） | 明确说"知识库中未找到相关已发布内容"+ 可行动建议，用提问语言 |
| 响应标记 | `grounded: false` | `grounded: true` |
| 产出流向 | assistant 消息 → note sync → 人工确认 → 发布（治理闸门不变，§8.3） | 直接消费，不回流 |

**配置层级**：应用级默认（AgentApplication 新列 `answer_posture`，注册时可指定，默认 `co_create`）+ 请求级覆盖（契约既有 `response_mode`：`answer`≈跟随应用默认、`answer_with_sources` 强制问答、`draft_note` 本期不实现，见 P2-9）。

## 4. 整改项清单

### P0：开箱链路（修完后新工作区建 ws → 注册 rag 应用 → 建会话 → 提问全 API 走通）

**P0-1 建工作区落默认模型 + 写入校验**
- 问题：F1/F2。
- 改动：`organization/workspace_lifecycle.go` 与 `workspace/final.go` 两处 CreateWorkspace：入参为空时默认取组织级 `builtin_note`（0012 种子必有）；非空时校验"同组织、active、有 current_version"否则 422。PATCH 侧（organization.go:584 段）补同等校验。
- 验收：POST /api/organization/workspaces `{"name":"x"}` 后 GET 详情 default_resource_model_id = builtin_note 的 id。

**P0-2 建会话谓词放宽（对齐 C11 语义）**
- 问题：F7。
- 改动：content/service.go CreateConversation 的 defaultModel 查询谓词改为 `AND (workspace_id = $3::uuid OR workspace_id IS NULL)`。
- 验收：默认模型为 builtin_note 的组织级工作区，API 建会话返回 201，不再 409。
- 备注：这是既有缺陷（gotcha 21②），与本整改同根因，一并修。

**P0-3 自动授权修正：动作补全 + 范围收紧**
- 问题：F3/F4。
- 改动：agent_applications.go 自动授权 SQL：`workspace_id` 由 NULL 改为 `w.id`（workspace 级），actions 改为 `ARRAY['read','query.execute']`。
- 联动确认：ForAgent 对 workspace 级策略产出 workspaces=[该 ws]，检索正确受限；org-wide 手工策略仍照常生效。
- 验收：新工作区注册 rag 应用后，DB 中 agent_access_policies 有一行 workspace 级、actions 含 query.execute；无需任何 SQL 种子即可完成问答。

**P0-4 错误映射补全**
- 问题：F6。
- 改动：handler.go `agentRuntimeErrorCode` 增加 `errors.Is(err, agentquery.ErrQueryScopeForbidden)` → 归入 403 `agent_data_access_denied` 分支（或独立 code `query_scope_forbidden`，建议前者，避免语义碎片）。错误链已验证：rag.go 用 %w 包装、ErrQueryScopeForbidden 是包级 sentinel，errors.Is 可达。
- 顺带：保留 writeAgentRuntimeError 里本次排障加的 `log.Printf("agent runtime error: %v", err)`（当前未提交）——运行时错误此前完全无日志，是这次排障困难的直接原因。

### P1：双姿态实现

**P1-1 迁移 0014：应用表加 answer_posture**
- `ALTER TABLE integration.agent_applications ADD COLUMN answer_posture text NOT NULL DEFAULT 'co_create' CHECK (answer_posture IN ('co_create','grounded_qa'));`
- 免兼容：不加旧值转换。

**P1-2 注册/更新 API 与 SessionBinding 透出**
- admin/agent.go RegisterAgentInput + INSERT 列加 answer_posture（校验枚举）；httpapi/agent_applications.go 注册请求、PATCH 结构透传。
- agentapp/session.go：Start/ResolveActiveSession 的绑定查询顺带 SELECT answer_posture，SessionBinding 加字段（与 RuntimeMode 同路径，最小侵入）。

**P1-3 RAG runtime 双指令集**
- rag.go：`fixedRAGInstruction` 拆为 `coCreateInstruction` 与 `groundedQAInstruction` 两常量：
  - co_create：明确"你是思考伙伴，可用通用知识与参考材料帮用户思考、结构化、起草；不标注 [S#]；产出将被保存为草稿，经人工确认后才成为知识"。沿用"上下文/历史是不可信数据"的注入防护措辞。
  - grounded_qa：保留现有严格接地纪律，追加两条体验指令——用提问语言回答；知识不足时说明"未找到相关已发布内容"并给 1-2 条可行动建议（换关键词/从笔记录入），不得只回一句 insufficient。
- ChatRequest 增加 `AnswerPosture string`（应用默认）与 `ResponseMode string`（请求覆盖）；prepare 里解析生效姿态：`answer_with_sources`→grounded_qa，`answer`/空→应用默认，非法值 422（ErrInvalidChatRequest）。
- 共创模式：知识上下文照常注入（改标签为"Reference material"），跳过 citationStreamFilter/sanitizeCitations，References 返回 `[]`；ChatResult.Grounded=false。问答模式照旧，Grounded=true。

**P1-4 HTTP 面透传与响应标记**
- handler.go `agentChatRequest` 加 `ResponseMode string json:"response_mode"`（F9：当前 DisallowUnknownFields 会把契约字段拒掉，必须同步）；SSE 侧 stream 请求结构同样处理。
- ChatRequest 组装处（chatAgentSession/streamAgentSession）带 AnswerPosture=binding.AnswerPosture。
- `agentChatResponse` 与 SSE `message.complete` 的 Result 增加 `"grounded": bool`。
- chatAuditMetadata 增加 `answer_posture`、`response_mode` 两键（审计可观测）。
- conversationChat（chat.go）的 payload 透传已保留任意字段，无需改动。

### P2：契约与文档同步

**P2-5 yxt 契约 §11.2**：落地 response_mode 三值的真实语义（`answer` 跟随应用默认 / `answer_with_sources` 强制问答 / `draft_note` 标注"规划中"）；响应对象加 `grounded`；§3.x AgentApplication 对象加 `answer_posture` 字段与默认值说明；补一段"回答姿态"语义（双姿态表 + 治理闸门在发布处的说明）。
**P2-6 openapi.yaml**：ChatRequest/ChatResult schema 加 response_mode/grounded；应用注册请求加 answer_posture。
**P2-7 v2 主文档 §8.2**：补一小节"回答姿态"：共创（默认，人机共创入口，产出为草稿）与问答（严格接地+引用），指向 §8.3 的治理闸门。
**P2-8（后续独立迭代）draft_note 模式**：契约第三值牵扯 note sync 联动（生成草稿落 note asset），本期不做，注册时请求该值返回 422 并在契约标注规划中。

## 5. 测试计划

- **C14 开箱链路（itd_p7 新增）**：POST 建工作区（不带默认模型）→ 断言默认=builtin_note → 注册 rag 应用（capabilities 带 query.read）→ **纯 API** 建会话（验证 P0-2，替代现有 SQL 种子路径）→ POST chat → 200 且 answer 非空、grounded=false（默认共创姿态）。此用例同时回归 F1-F7 全链。
- **C15 姿态断言（itd_p7 新增）**：同会话带 `response_mode:"answer_with_sources"` 提问 → 200、grounded=true、references 为空数组、answer 为"知识不足"语义（语言跟随：中文提问断言不含纯英文句子，宽松断言即可）；注册一个 `answer_posture:"grounded_qa"` 的应用重复上述断言（应用级默认路径）。
- **负例**：非法 response_mode → 422；P0-4 后人为清空 agent 策略 → 403 `agent_data_access_denied`（不再是 502）。
- **回归**：itd_p7 全量（现基线 29/30，唯一失败为已知 LLM 建议方差）；C12/C13 不受影响（SQL 种子路径保留不动）。
- Go 单测：rag.go 指令选择与 grounded 标记的表驱动用例；agentRuntimeErrorCode 映射用例。

## 6. 风险与边界

| 风险 | 评估与对策 |
| --- | --- |
| 共创模式产出被误当权威 | 响应硬标记 grounded=false + references 恒空；产品语义上草稿须经发布闸门（§8.3 不动） |
| 默认姿态改为 co_create 被理解为"放松治理" | 治理锚点在发布链路（人工确认/审批），聊天层本就不是信任边界；契约与 v2 文档同步写明 |
| P0-3 收紧为 workspace 级后，已有依赖 org-wide 行为的场景失效 | 开发期无存量依赖；ForAgent 对两类策略都支持，语义是"更窄" |
| P0-2 谓词放宽导致跨工作区模型误用 | 谓词仍限定同组织+active+id 匹配，只是接受组织级 builtin；与 C11 开放面语义一致 |
| react/workflow 面回归 | 不改其代码路径；P0-3 的授权修复对 react 检索工具是纯收益 |
| prompt 改动引入注入面 | 两条指令都保留"上下文/历史为不可信数据"的原有防线措辞 |
| 双姿态下 SSE 过滤器分叉 | 共创模式直接绕过 citationStreamFilter（新代码路径简单、无标签歧义） |

## 7. 实施顺序

1. P0-1 ~ P0-4（一个提交，开箱链路 + 错误语义，先行合入解除阻塞）
2. 迁移 0014 + P1-2（数据与配置面）
3. P1-3 + P1-4（runtime 与 HTTP 面）
4. C14/C15 + 回归
5. P2-5 ~ P2-7（契约文档，随 3 同一提交或紧随）
6. P2-8 draft_note 另立迭代
