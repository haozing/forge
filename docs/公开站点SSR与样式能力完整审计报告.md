# 公开站点 SSR 与样式能力 完整审计报告

> 审计基线：`21e974b..5d2e46a`（4 提交，75 文件，+8154/-84）
> 审计日期：2026-09-02
> 审计对象：① 两份设计文档（是否忠于项目理念与用户模型）；② 代码（是否符合设计文档）
> 前提：开发期，不考虑兼容性与旧数据
> 判定分级：**S（阻断）** 必须立即处理 / **A（重大）** 合入前必须处理 / **B（一般）** 记录并排期 / **C（建议）** 可选优化
> 声明：本审计由实现者执行，为对冲"自审偏宽"，全部安全断言均以对抗性实测取证，不以"代码看起来对"为通过依据

---

## 1. 审计标尺：项目理念提炼

从 `产品文档-v2.md` 与 `CMS呈现层前后端设计方案.md` 提炼六条不可妥协的标尺，作为设计文档与代码的共同准绳：

| # | 理念 | 出处 |
| --- | --- | --- |
| P1 | **资产是唯一权威**：站点是展示视图，不复制正文，只读已发布指针 | v2 §7.1/§7.4 |
| P2 | **单一可见性判断**：内容筛选/标签/搜索必须复用统一查询服务与权限层，不允许第二套 | v2 §7.3、CMS §2.1 |
| P3 | **模板只接 ViewModel**：不接原始资产 DTO、无模板内查询/代码 | CMS §3.3 |
| P4 | **Agent 产候选、人守发布闸门**：Agent 默认只能写受控草稿/候选；发布归人 | v2 §8.3 |
| P5 | **动作权限与数据范围分离**；Agent 用独立策略实时校验，不继承成员角色 | v2 §10.2/§10.4 |
| P6 | **封闭声明式样式空间**：首期不支持自定义 CSS/组件/任意模板；一切走固定 Schema | CMS §2.3 |

用户模型（两份设计文档自我声明的）：**零开发知识的小白，Agent 是唯一操作入口**——方案中任何"靠用户手工完成"的通道一律不成立（二期方案头部前提）。

---

## 2. 设计文档审计

### 2.1 一期《公开站点SSR投递与样式参数空间设计方案》（r2）

| 核验 | 结论 | 依据 |
| --- | --- | --- |
| 忠于 P1（只读已发布指针） | ✅ | §1.2 非目标"Delivery 只读已发布版本"；§7.4 明确 Release 只固定配置快照、不固定内容版本，并对 v2 §7.2"不提供站点级版本固定"做了诚实的边界澄清 |
| 忠于 P2（单一可见性） | ✅ | §4.2/§5.3 红线"必须经 PublicSiteQuery，禁止直连资产表 SQL"，且现状核验部分引用了真实代码位置（PublicReader、scope_compiler） |
| 忠于 P3 | ✅ | §5.1"模板只接收 ViewModel struct（编译期保证）" |
| 忠于 P4/P5 | ✅ | §8.3 权限决策明确不用 admin-only 的 site.manage，工具定为 ReadOnly；确认发布永远是人 |
| 忠于 P6 | ✅ | L1 是封闭参数空间；§1.2 非目标明确"不做自定义 CSS"（本期） |
| 适配小白用户 | ⚠️ 部分缺陷（已在一期实施中被用户纠偏） | 初版 §4.6 写"Agent 不产出 L2 CSS"，与"小白唯一入口 Agent"的用户模型直接冲突——用户质询后修订为双产物+清理器模型（r2 修订记录第 4 条留痕）。**教训已吸收进二期方案头部前提** |
| 审计修订落实 | ✅ | 首轮审计（本会话早期）的 8 项修订（事件名更正、缓存瘦身、删 PURGE 端点、DDL 复合键等）全部落入 r2 正文与 §15 修订记录 |

**结论：一期设计文档通过。** 唯一重大缺陷（Agent 不写 CSS）在评审闭环中被纠正且留了审计痕迹。

### 2.2 二期《公开站点样式与页面能力扩展设计方案》

| 核验 | 结论 | 依据 |
| --- | --- | --- |
| 小白+Agent 唯一入口前提 | ✅ | 头部明文确立，§11 写入验收口径："任何需要用户理解 CSS/JSON/参数名的步骤都算设计缺陷" |
| P6 冲突：L2 custom_css | ⚠️ **文档体系性缺口（B-05）** | CMS §2.3 明文"第一版不支持自定义 CSS、自定义组件"。二期以"受控白名单+预览+Release"引入 custom_css，安全论证成立（详见 §4.2），但 **CMS 方案未同步修订**——三份文档并存时读者会得到矛盾指令。二期自身在 §1.2 把它列为非目标的反面（"L2 是本方案主体"），演进链条在二期内部完整，缺的是向 CMS 方案的回写 |
| P4 张力：preset_save 是 LowWrite | ⚠️ 需产品确认（B-06） | v2 §8.3 字面："Agent 只能更新受控 AssetDraft 或生成未发布候选版本"。`site_style_preset_save` 写的是**组织预设数据束**——不是 AssetDraft 也不是候选版本，字面超出授权清单；但语义上无生效影响（套用仍需人 PATCH+发布），符合"人在闸门"的精神。判定：**精神符合、字面越界**，应在 v2 §8.3 补一句"只读样式工具可保存无生效影响的数据束"或将 preset_save 改为仅产候选 |
| 每项能力落回"改库即生效" | ✅ | §2 总览明确全部操作面是数据库；实施验证（itd_p9 32/32）证实 |
| 版本化边界 | ✅ | custom_css/comments_mode 随 Release 快照（§4.2/§8.2） |

**结论：二期设计文档基本通过**，两处 B 级文档级缺口（CMS 回写缺失、preset_save 的理念字面越界）需要补文档而非改代码。

---

## 3. 代码审计总览（对照设计文档逐域）

### 3.1 投递读路径与缓存（M1/M2）

| 设计要求 | 实现 | 判定 |
| --- | --- | --- |
| 模板只接 ViewModel（P3） | 全部模板接收 `HomeVM/ListVM/DetailVM/...` 具体类型；markdown 经 goldmark+bluemonday（delivery/markdown.go）后以 `template.HTML` 注入；JSON-LD 由 `json.Marshal` 转义 `<>&`（service.go articleJSONLD） | ✅ 符合 |
| 单一可见性（P2） | 页面数据全部经 `PublicReader`（HomeWithConfig/Posts/Post/Section/Tags/About），可见性由内部 ForPublicSite/AuthorizePublicSiteAsset 决定；pipeline 只算缓存档位不判可见性 | ✅ 符合 |
| 缓存键含 tier、member 档 private | PageKey 四段键（cache.go）；pipeline 对 member/非 public 站强制 `private`（service.go） | ✅ 符合 |
| 失效链（事件→表→轮询，无内部 HTTP） | worker `delivery.cache` 消费者写 `delivery.cache_invalidations`（invalidator.go），api 2s 轮询（poller.go）；事件覆盖 asset.*/site.*/comment_created/membership_changed/tag.* | ✅ 符合 |
| CSP/安全头 | delivery_routes.go `deliveryCSP` + nosniff + Referrer-Policy + X-Robots-Tag | ✅ 符合 |
| **B-07（A 级，审计发现并已修复）媒体路由独立授权链** | `/sites/{slug}/media/{id}` 的授权 SQL（pages2.go）**没有复用** AuthorizePublicSiteAsset/ForPublicSite，是一条独立的可见性判断——形式上触碰 P2 红线。缓解事实：它判断的是"附件是否为已发布版本的封面且绑定本站"，资产可见性依赖**写入侧不变式**（绑定门禁 ErrBindingTargetInvalid 保证可见性≤站点 scope）。判定：范围窄（仅封面附件可达性），但属于设计文档"不允许第二套"的字面违反。**修复中一并补上 `cover.status='clean'`（见 B-04）**；根治（复用 Authorizer）列为 B 级排期 | ⚠️ 字面违反/实质等价 |
| **B-08（B 级）渲染侧对损坏样式文档降级而非拒绝** | 设计 §7.2"非法值写入与渲染两侧都拒绝"；代码 `style()` 对解析失败**降级到 calm 默认**（service.go，理由：避免损坏行让整站 500）。语义偏差已留注释，设计文档未回写此决策 | ⚠️ 偏差已记录未回写 |

### 3.2 样式系统 L1/L2（M3/N2）

| 设计要求 | 实现 | 判定 |
| --- | --- | --- |
| L1 封闭枚举、双侧校验 | style.go 五张枚举表 + 区间/正则；写入侧（PATCH/预设/Agent 工具）与渲染侧（ParseStyleConfig）同源 | ✅ |
| WCAG 4.5:1 双侧强制 | `CheckContrast` 在 ParseStyleConfig 与 MergeStylePatch 终点强制（D-01 修复后闭环，单测钉死） | ✅ |
| 深合并保兄弟、null 重置、拒未知键 | MergeStylePatch/deepMergeStyle + 单测矩阵 | ✅ |
| L2 白名单清理器 | css.go 词法级（gorilla/css）三层白名单 + 单测矩阵 | ✅ 结构符合 |
| **B-01/B-02/B-03（A 级，本次审计实测发现并已修复）清理器绕过** | 见 §4.1 安全专项 | ⚠️ **审计前存在，已修复+回归** |
| custom_css 随 Release 快照、预览支持候选 | release.go ReleaseConfig.CustomCss、preview.go 候选清理后渲染 | ✅ |

### 3.3 能力扩展 N3-N6

| 设计要求 | 实现 | 判定 |
| --- | --- | --- |
| 封面与内容同版本物化 | Link 弄脏草稿（D-13 修复）→ commit 物化 role；草稿显式 > 上版继承（draft_service.go/member.go） | ✅（itd_p9 N3 实证） |
| 封面资格（image/*、≤5MB、每草稿一封面） | attachment Link extraClause + 版本侧部分唯一索引（0016） | ✅（gaps G5 实证） |
| 媒体授权链含 clean | **B-04（A 级，审计发现并已修复）**：SQL 漏 `status='clean'`——附件若后续被重扫标脏仍会供图 | ⚠️ 已修复 |
| 预设拷贝语义（展开+CSS 随行） | ExpandStylePreset 写入时展开、delete uuid 键（D-09）、CSS 拷贝（D-10） | ✅（N4 实证） |
| about/archive 纯服务端、复用读路径 | About 复用 Post 管线；archive 走 Posts 分页分组（pages2.go） | ✅ |
| 评论成员制/先审后显/冷却/失效 | comments.go + gaps G3/G4/G9 实证（G9 即 D-16 修复的确定性回归） | ✅ |
| **B-09（B 级）评论写入不复核资产对评论者的可见档** | CreateComment 的 bound CTE 只查"绑定+已发布"，不查该资产对评论者是否可见（org 成员可对 workspace-only 资产评论 201，但其页面对自己不渲染该资产）。防探测语义未被破坏（评论不存在 404 与绑定不存在同码），属一致性瑕疵 | ⚠️ 记录排期 |

### 3.4 Agent 工具（M5/N2 工具面）

| 设计要求 | 实现 | 判定 |
| --- | --- | --- |
| 工具只读/产候选，发布归人（P4） | suggest 返回候选（含已清理 CSS），发布路径全部在人侧；preset_save 见 B-06 | ✅（preset_save 除外） |
| 能力门 | capability `site.style`；suggest=ReadOnly、preset_save=LowWrite 需 `allow_low_write`（gaps G8 教训留档） | ✅ |
| 范围锁定 | 站点解析 cast 安全三态（uuid/slug/name）且严格 org+workspace 域内（D-17 修复） | ✅（G8 实证） |
| 候选 CSS 工具层先清理、自愈回路 | style_suggest.go SanitizeCSS→被剥候选带原因回传（builtins.go detail 字段） | ✅ |
| **B-10（C 级）工具错误 detail 可能携带内部信息** | err.Error() 截 400 字符回传模型——我们自己的错误是受控文本，但透传的底层错误（如 SQLSTATE）可能含 schema 细节。仅回传给会话内模型，不直接暴露公网 | C 建议：detail 白名单化 |
| Agent 不直接访问数据库（v2 §8.2） | 工具闭包内 SQL 属**服务端代执行**，与既有 getSchema 等工具同款先例；Agent 进程本身无库凭据 | ✅ 符合既有架构解释 |

---

## 4. 安全专项（对抗性实测取证）

### 4.1 清理器绕过三连（B-01/02/03，A 级，已修复）

审计不采信"看起来封死"，对 SanitizeCSS 打了 12 发对抗载荷，**三发穿透**：

| 编号 | 载荷 | 原理 | 危害 |
| --- | --- | --- | --- |
| B-01 | `background-image: u\72 l(https://evil.example/x)` | CSS 反斜杠转义：浏览器把 `u\72 l(` 重组为 `url(`，躲过一切字面量黑名单（`expr\65 ssion(` 同理重组 expression） | 外传/追踪/旧浏览器代码执行 |
| B-02 | `background-image:url\n(https://...)` | CSS 允许 url 与括号间换行；tokenizer 不识别为 URI token，value 里字面是 `url␊(` 不含 `url(` | 同上 |
| B-03 | `font-family: '</style><script>alert(1)</script>'` | 字符串值携带闭合标签；custom_css 以 template.CSS 内联进 `<style>`，`</style>` 闭合样式块注入任意 HTML（CSP `default-src 'none'` 挡住脚本执行，挡不住结构/钓鱼注入） | 页面结构注入 |

**修复**（css.go，一处防线封三洞）：值与选择器中 `反斜杠`、`<`、`>`、换行一律拒绝 + `url` 字样整体禁（不限括号形态）——白名单值域（颜色/数字/标识符/字符串字体名）不需要其中任何一个。已入单测矩阵（TestSanitizeCSSAuditBypasses），合法值（`"PingFang SC"`、`-4px`）回归无杀。**根因归档：字面量黑名单对可编码语法永远输——字符级白名单才是正确防线。**

### 4.2 媒体授权缺口（B-04，A 级，已修复）

媒体路由 SQL 漏 `cover.status='clean'`：上传时 clean、后续重扫标脏的附件会继续经公开路由供图。已补；与 B-07（独立授权链）同源——根治是复用 AuthorizePublicSiteAsset，列 B 级排期。

### 4.3 一期红线逐条复核

| 红线 | 状态 |
| --- | --- |
| 模板只接 ViewModel / 无模板内代码 | ✅ |
| Markdown 服务端清理 + 自动转义 | ✅（对抗载荷含 `<script>` 注入 markdown，实测剥除） |
| 样式仅 CSS variables + 内联注入、无外链字体 | ✅（B-03 修复后注入面闭合） |
| 预览成员鉴权 + noindex + no-store | ✅（p8 PV1 实证） |
| 未发布版本永不出现在 HTML 面 | ✅（全链 current_published_version_id；详情 re-check） |
| tier 缓存分档防泄漏 | ✅（p5 泄漏矩阵 + G2 member 可见复跑） |

---

## 5. 偏差汇总表

| 编号 | 级别 | 域 | 内容 | 状态 |
| --- | --- | --- | --- | --- |
| B-01 | A | L2 清理器 | CSS 转义序列绕过（u\72 l→url） | **已修复+单测钉死** |
| B-02 | A | L2 清理器 | 换行 url 绕过 | **已修复（同 B-01 防线）** |
| B-03 | A | L2 清理器 | 字符串携带 `</style>` 注入 | **已修复（同 B-01 防线）** |
| B-04 | A | 媒体路由 | 授权 SQL 漏 status='clean' | **已修复** |
| B-05 | B | 文档一致性 | CMS §2.3"不支持自定义 CSS"未随二期 L2 回写修订 | 待补文档 |
| B-06 | B | 治理 | preset_save 写库超出 v2 §8.3 字面授权清单（语义无害） | 待产品确认+v2 补句 |
| B-07 | B | 媒体路由 | 独立授权链触碰"单一可见性"字面（靠写入侧不变式兜底） | 排期：复用 Authorizer |
| B-08 | B | 渲染容错 | 损坏样式文档渲染侧降级而非设计要求的拒绝 | 记录；设计文档回写该决策 |
| B-09 | B | 评论 | 写入不复核资产对评论者的可见档（一致性瑕疵，非泄漏） | 排期 |
| B-10 | C | Agent | 工具错误 detail 未白名单化（可能带 SQLSTATE 给模型） | 建议 |
| — | C | 一期实现 | 预取器层简化为直调 PublicReader；related/pager/breadcrumbs/JSON-LD BreadcrumbList 未实现；分页无 rel=prev | 记录（一期验收报告 §7 已列） |

**无 S 级（阻断）项。** 4 个 A 级全部为本次审计新发现并已修复回归（B-01/02/03 为审计对抗实测抓出——此前的验收测试未覆盖编码绕过类载荷，这一教训已写入 §6）。

## 6. 测试充分性评估

- 既有覆盖（itd_p8 45 + p9 32 + gaps 20 + 单测矩阵）对**功能语义**覆盖充分；本次审计证明其对**编码类安全绕过**存在盲区（三发穿透全部来自此前测试矩阵之外的载荷形态）。已把对抗载荷固化进单测矩阵（css_test.go），后续清理器改动自动回归。
- 修复后终态：单测全绿 + p9 32/32 + p8 45/45 复跑通过。

## 7. 结论

1. **设计文档层**：一期、二期均忠于项目理念与小白用户模型；一期的"Agent 不写 CSS"缺陷已在评审中纠偏留痕；二期遗留两个文档级缺口（CMS 回写、preset_save 理念句）。
2. **代码层**：结构与治理红线符合设计；本次审计以对抗实测抓出 4 个 A 级安全/授权缺口（清理器三绕过 + 媒体 clean 缺失），全部当场修复并固化回归——**在修复前，custom_css 理论上可被编码绕过实现外传与结构注入**，这是本审计最有价值的发现，也验证了"审计必须实测、不能采信实现者自述"。
3. 建议后续：B-05/B-06 补文档决策；B-07/B-09 排期根治；清理器未来任何扩展沿用"字符级白名单优先于字面量黑名单"原则。
