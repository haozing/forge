# 站点字段展示白名单（model_views）实施方案

日期：2026-09-10
后端基线：`4bfc335` + 2026-09-10 已实施批次（通知修复、/audit、SSE Flusher 修复等）
前端基线：zck-web `src/features/site`（公开站点管理模块已上线）
范围：把工作区资源模型的结构化字段接入公开站点渲染——按站点、按模型配置"卡片展示哪些字段、详情展示哪些字段"，并收口公开 JSON 的字段暴露面。

一句话：**建模在资源模型（已交付），消费出口是站点（本方案补最后一公里）**——对齐产品文档 v2 §7.3"Markdown、结构化字段和附件的安全渲染"与《CMS呈现层前后端设计方案》§7.2/§8.4 的 `model_views` 白名单契约。

---

## 0. 现状与问题（基于真实代码）

### 0.1 字段已经流到站点门口，但没人渲染、没人管

| 环节 | 现状 | 证据 |
| --- | --- | --- |
| 建模 | 资源模型字段体系完整：13 种类型（string/text/markdown/integer/number/boolean/date/datetime/enum/multiselect/object/array/asset_reference），键名正则 `^[a-z][a-z0-9_]{1,63}$`，版本化 field_schema + list_schema | `internal/resourcemodel/schema.go:325-334`、`db/migrations/0012` |
| 公开读投影 | `ProjectFields(fields, ParseFieldSchema(row.FieldSchema))` 已把版本字段收敛到 schema 声明的键（未声明键被剥掉） | `internal/site/public.go:591,1348-1363` |
| 公开详情 JSON | `PublicPostContent.Fields map[string]json.RawMessage` 整包返回——**schema 声明的全部字段无差别暴露给任何访客**，站点无法控制 | `public.go:391`；`writePublicSiteError` 面 `publicSitePost` 直接 `writeData` |
| 列表/卡片 | `PublicPost` 没有 fields，卡片完全没有字段展示 | `public.go:335-347` |
| SSR 渲染 | `DetailVM` 无字段条目；全部模板零引用 `.Fields`——字段到了渲染层门口但从未显示 | `viewmodel.go:135-157`、`ResolveDetail:390-409` |
| 站点配置 | 无任何站点级字段展示配置（CMS 方案的 `model_views: {card_fields, detail_fields, filter_fields}` 与 `configure_model_view` 命令未实施） | 全仓无 `model_views` |
| 可复用的候选集 | 模型级 `list_schema.columns`（如 builtin_shot：`["shot_size","camera_angle","lens_mm","location"]`）已有校验器（引用未声明字段即 `unknown_field`） | `resourcemodel/schema.go:535-561`、`0012` 种子 |

**问题定级**：P1 功能缺口（文档承诺的结构化字段渲染不存在）+ 一个暴露面问题（当前"schema 声明即公开"，站点对哪些字段对外没有发言权；模型里加一个 `internal_note` 之类的字段就会立刻出现在公开 JSON 详情里）。

### 0.2 目标

1. 站点管理员按模型勾选"卡片字段 / 详情字段"，配置进站点配置并随 Release 快照冻结/回滚。
2. 详情页 SSR 渲染白名单字段（分类型安全呈现），列表卡片渲染前 N 个卡片字段。
3. 公开 JSON（详情 `fields`、列表条目 `fields`）只返回白名单命中的字段——**白名单为空 = 零字段输出（fail-closed）**，修复 0.1 的暴露面。
4. 预览（含 base_url 模式）自动反映白名单。

> 实施口径修正（按"干净实现、不考虑旧数据"指令）：公开 JSON 的 `fields` 从"过滤后的 map"升级为**带类型与顺序的数组** `[{key, type, value}]`（`value` 为原始 JSON 值），白名单顺序即数组顺序——比文档原稿的 map 形态更干净，且无兼容包袱。

### 0.3 明确不做（范围裁定）

- `filter_fields`（公开筛选白名单）——等"栏目×内容类型"（P3）一起做，当前公开面没有自定义字段筛选入口。
- 字段 label/i18n——field_schema 条目只有 `key/type/options/default/unique`，没有 label 键；显示用英文 key，等模型层引入 label 后自然继承。
- `markdown` / `object` / `array` / `asset_reference` 类型进白名单——v1 只允许标量类型（渲染矩阵见 §4），复杂类型的呈现需要单独设计。
- 站点级内容类型/文章模型——产品文档 §15 明确排除，不在本方案。

---

## 1. 数据模型

### 1.1 迁移 0028

```sql
-- 0028_site_model_views.sql
-- Site-level per-model field display whitelist (CMS plan §7.2 model_views).
-- '{}' = no fields are published for any model (fail-closed default; the
-- pre-whitelist behavior exposed every schema-declared field on the public
-- detail JSON). Existing rows need no backfill: empty keeps the new
-- fail-closed semantics.
ALTER TABLE site.public_sites
    ADD COLUMN model_views jsonb NOT NULL DEFAULT '{}';
```

幂等安全，可重复执行（ALTER ADD COLUMN IF NOT EXISTS 视部署工具而定，保持与既有迁移风格一致用裸 ALTER）。

### 1.2 存储形状

```json
{
  "<resource_model_uuid>": {
    "card_fields":   ["shot_size", "lens_mm"],
    "detail_fields": ["shot_size", "camera_angle", "lens_mm", "location", "rating"]
  }
}
```

- 键 = 本工作区 resource_model 的 UUID（字符串）。
- 两个数组都可选；数组内为 field key，**顺序即渲染顺序**（数组序，非 schema 序——管理员可调展示先后）。
- 上限：`card_fields ≤ 4`（卡片版面约束）、`detail_fields ≤ 12`；去重；两数组允许交集（卡片是详情的精选）。
- `Site` 结构体加 `ModelViews map[string]ModelView`（json tag `model_views`），`siteColumns` 追加 `model_views`——Create/Update/Get/loadSite/preview 全部扫描点自动带上（单一事实源模式，`service.go:143-146`）。
- `ReleaseConfig` 加 `ModelViews json.RawMessage \`json:"model_views"\``——快照/回滚自动携带（`release.go:44-51,163-176` 现有机制照抄 homepage_config 的位置）。

---

## 2. 写侧：PATCH 校验（`internal/site/service.go` UpdateSite）

`UpdateSiteInput` 加 `ModelViews *json.RawMessage`（指针语义，nil 不变）。校验流程（全部在现有 `ErrInvalidInput → 422 validation_failed` 通道内，结构化细节走 `writeErrorDetail`）：

1. **形状**：必须是 JSON 对象；每个键是合法 UUID；每值是对象，`card_fields`/`detail_fields` 是字符串数组；去重后 card ≤ 4、detail ≤ 12；非法 → 422（details: `model_id`/`field`）。
2. **模型归属**：键必须是**本工作区、status='active'** 的资源模型 id——单查询 `SELECT id FROM model.resource_models WHERE organization_id=$1 AND workspace_id=$2 AND id = ANY($3) AND status='active'`，缺失键 → 422 `unknown_model`（details 携带 model_id）。
3. **字段存在性**：对每个键，取该模型**当前版本 field_schema**（`SELECT mv.field_schema FROM model.resource_models rm JOIN model.resource_model_versions mv ON mv.id = rm.current_version_id WHERE ...`），用现成的 `ParseFieldSchema` 得到 `map[key]type`；白名单数组中出现 schema 未声明的 key → 422 `unknown_field`。
4. **类型准入**（v1 渲染矩阵，见 §4）：
   - `detail_fields` 允许：`string, integer, number, boolean, date, datetime, enum, multiselect`（8 种标量）；
   - `card_fields` 允许：`string, integer, number, boolean, date, enum`（6 种；multiselect/text 不进卡片，卡片只有一行 meta 空间）；
   - 不合规 → 422 `unsupported_field_type`（details 携带 field 与 type）。
5. 通过后整包存 `model_views`；PATCH 其余语义（If-Match 必填、revision+1、`appendSiteEvent "updated"`、audit、`delivery.cache` 失效链）全部自动继承——`applySiteUpdate` 加一个 set 子句即可（`service.go:505-530` 模式）。

**发布时（PublishRelease）**：快照序列化前对 `ModelViews` 做**剪枝而非拒绝**——按发布时刻各模型当前 field_schema 过滤掉已不存在的字段/已失效的模型键；剪枝结果进 `site_releases.config`。理由：渲染正确性优先，模型改版不应卡死站点发布；剪枝行为记入 release 审计 metadata（`model_views_pruned: N`）。

**渲染时**：再与绑定发布版本自己的 field_schema 取交集（fail-open：白名单声明了但该版本没有的键直接跳过）——三层口径统一为"**站点白名单 ∩ 版本 schema**"。

---

## 3. 读侧：白名单过滤（`internal/site/public.go`）

新函数（复用 ProjectFields 之后调用）：

```go
// WhitelistFields narrows schema-projected fields to the site's per-model
// display whitelist. Empty/absent whitelist = zero fields (fail-closed;
// CMS plan §8.4: fields ride the release whitelist, never raw).
func WhitelistFields(projected map[string]json.RawMessage, view ModelView) map[string]json.RawMessage
```

接入点：

| 面 | 改动 | 位置 |
| --- | --- | --- |
| 公开详情 JSON | `Post(...)` 组装 `PublicPostContent.Fields` 时按 `site.ModelViews[modelID].DetailFields` 过滤；白名单空 → `fields` 为空对象 | `public.go:580-600` 附近，投影调用处追加 |
| 公开列表 JSON + 卡片渲染 | `PublicPost` 增加 `Fields map[string]json.RawMessage \`json:"fields,omitempty"\``；`boundVersionRows` 列表查询补两列（发布版本 `pv.fields`、`pv.field_schema`——版本冻结语义与详情一致：字段结构取**绑定版本**登记的 schema，非模型当前头）| `public.go:948-1011` |
| SSR 详情 | `ResolveDetail` 用 `content.Fields` + 字段类型构建 `DetailVM.Fields []FieldValueVM`（§4） | `viewmodel.go:390-409` |
| SSR 卡片 | 首页/列表 partial 的卡片 meta 区渲染 `card_fields`（最多 4 项） | `templates/partials/` 卡片模板 |
| 相关文章（G7） | 不带字段（PublicPost 列表投影自动带上卡片字段，区块模板不渲染即可，零改动） | — |

**fail-closed 迁移说明**：上线后所有未配置 model_views 的站点，公开详情 JSON 的 `fields` 从"全部 schema 字段"变为"空"——这是**行为收紧**（安全修复），需要在发布说明里明示；已发布的 Release 快照不含 `model_views` 键 → 解析为零值 → 同样 fail-closed，不会因回滚到旧快照而重新放大暴露面。

---

## 4. 渲染矩阵与 VM

```go
// FieldValueVM 是详情页字段表的一行。
type FieldValueVM struct {
    Key   string // 英文键（field_schema 无 label，v1 直接显示 key）
    Type  string
    Value string // 已按类型格式化的展示文本；模板 html/template 自动转义
}
```

| type | 详情呈现 | 卡片呈现 |
| --- | --- | --- |
| string | 原文（自动转义） | 原文 |
| integer / number | 数字文本 | 数字文本 |
| boolean | "是"/"否" | 同左 |
| date | ISO 日期（`FormatDate` 复用） | 短日期 |
| datetime | `formatDateTime` | 短日期 |
| enum | options 里 value 原文 | 同左 |
| multiselect | 逗号连接（详情）；卡片不允许 | — |

- 模板：`templates/partials/` 新增 `fields-table.html`（dl 两列：key / value），detail 页挂在正文与标签之间；卡片 partial 在 summary 行下加一行 meta（最多 4 项，` · ` 分隔）。
- 值缺失（版本没有该键或 null）：整行跳过，不渲染空行。
- 值长度护栏：string 超过 200 字符在卡片截断（详情不截）。
- 安全：全部经 `html/template` 自动转义；不引入任何 link/URL 渲染（无 url 类型），markdown/object/array/asset_reference 在写侧就拒绝进白名单。

---

## 5. API 与契约

- **PATCH `/api/workspaces/{ws}/sites/{siteId}`**：body 新增 `model_views`（整体替换语义，与 homepage_config 一致；不需要 patch 合并——前端总是提交完整对象）。
- **GET** 站点/列表/预览快照：`Site` 序列化自动携带 `model_views`。
- **Releases GET**：`config` 内自动携带（ReleaseConfig 序列化）。
- **openapi.yaml**：`SitePatch`/`PublicSite` 增加 `model_views`；`SiteRelease` 的 config 描述补一句；`PublicPostContent.fields` 描述改为"站点 model_views 白名单内的字段"。
- **字段级契约测试**：仿照 `notifications_test.go` 的双向比对模式，对 `Site` 结构体 json tag 与 openapi `PublicSite` properties 做双向断言（顺带把 2026-09-10 已补齐的 etag/model_views 等字段锁死，防再漂移）。
- 无新增路由，routerTruth 不变。

---

## 6. 前端（zck-web `src/features/site`）

### 6.1 站点详情新增「字段展示」Tab（仅 `site.manage` 可见，与内容绑定/外观同权限）

- 数据源：
  - 模型清单：`GET /api/workspaces/{ws}/resource-models`——**已有接口已返回 `field_schema`**（`Model.FieldSchema`，`resourcemodel/service.go:53,131`），零后端改动；新增 `site/api/queries.ts` 的 `fetchModelFieldSchemas(workspaceId)`（status='active' 过滤）。
  - 当前配置：`useSiteQuery` 的 `site.modelViews`。
- UI：左侧模型列表（含绑定状态提示：未绑定内容的模型也可配置，绑定后即生效）；右侧该模型的两组勾选列表——「卡片字段（≤4）」「详情字段（≤12）」，候选项 = 该模型当前 field_schema 的字段 key（标量类型可勾选，复杂类型灰置并注明原因），默认勾选建议取该模型 `list_schema.columns` 与标量类型的交集。
- 保存：整包 `model_views` PATCH（If-Match，复用 `usePatchSiteMutation`，input 类型加 `model_views`）；422 时按 details 展示"模型 X 的字段 Y 不存在或类型不支持"。
- 字段类型标注：候选项显示 `key` + 类型徽章（badge）。

### 6.2 其余接入点

- `model/site.ts`：`Site` 加 `modelViews: Record<string, { cardFields: string[]; detailFields: string[] }>` + normalize（容忍缺省 `{}`）。
- 预览对话框：后端 `RenderPreview` 用站点工作行渲染，`model_views` 自动生效——前端零改动，验收时确认预览能看到字段表。
- Mock：`site/mocks/fixtures.ts` 的 site fixture 加 model_views 样例 + handlers 的 PATCH 支持；新增字段展示 Tab 的单测（勾选/上限/复杂类型灰置）与一条 e2e 步骤。
- 文案：勾选区顶部注明"白名单为空时，站点与公开接口不输出任何结构化字段"。

---

## 7. 测试与验收

**Go 单测**（`internal/site`）：
1. `WhitelistFields` 矩阵：空白名单=全空；命中/未命中/值缺失；两数组独立。
2. UpdateSite 校验矩阵：unknown_model / unknown_field / unsupported_field_type / 超限 / 非法 UUID / 正常路径；If-Match 冲突不回归。
3. PublishRelease 剪枝：模型删字段后发布，快照中该键消失且审计含 `model_views_pruned`；回滚恢复旧白名单。
4. 渲染 VM：8 种类型的 Value 格式化；multiselect 连接；缺值跳行。

**Go httpapi 契约**：
5. PATCH model_views → 200 + GET 回读一致 + ETag/revision 前进。
6. `Site` ↔ openapi `PublicSite` 字段双向比对（新测试文件 `site_schema_test.go`）。
7. 公开详情 JSON：白名单外字段不再出现（对 0.1 暴露面的回归钉死）。

**前端**：vitest（normalize/勾选上限/Tab 权限）+ e2e（配置字段 → 详情预览可见 → 发布后公开 JSON 只含白名单字段）。

**验收口径（一句话）**：给 builtin_shot 模型配 `card_fields=[shot_size]`、`detail_fields=[shot_size,camera_angle,lens_mm]`，绑定一篇带这些字段的已发布 shot；详情页正文下方出现字段表、列表卡片出现 meta 行、`GET /api/public/sites/{slug}/posts/{path}` 的 `fields` 只含这三个键；发布新 Release 后配置冻结，回滚后白名单跟着回滚。

---

## 8. 落地顺序

| 阶段 | 内容 | 交付 |
| --- | --- | --- |
| 1 | 0028 迁移 + Site/ReleaseConfig/siteColumns + UpdateSite 校验 + 白名单过滤 + 公开 JSON 收口 | 后端可用（curl 可配） |
| 2 | SSR 渲染（fields-table + 卡片 meta）+ 预览验证 | 页面可见 |
| 3 | openapi/契约测试 + Go 测试全绿 | 契约锁定 |
| 4 | 前端「字段展示」Tab + mock + 测试 | 管理界面可用 |

阶段 1-3 是一个不可分割的最小单元（先收口暴露面再谈渲染）；阶段 4 独立可并行。

## 9. 后续衔接（本方案不做）

- `filter_fields` + 栏目按模型过滤（P3"栏目×内容类型"）。
- field_schema 增加 label 键后的展示名替换（模型层演进，自动继承）。
- 首页/导航配置编辑器（P2，独立方案）。
- knowledge 模式/文档树页（等 Folder 投影落地）。


---

## 10. 实施记录（2026-09-10，全部完成）

| 项 | 状态 | 落点 |
| --- | --- | --- |
| 0028 迁移 | ✅ | `db/migrations/0028_site_model_views.sql`；已通过 migrate 服务在本地栈正规应用（注意：手工 psql 应用会导致 api 的 schema contract check 拒绝启动，必须走记账迁移） |
| ModelView 类型 + 校验 + 过滤 + 剪枝 | ✅ | `internal/site/modelview.go`（ModelView / ModelViewError / ValidateModelViewShape / ValidateModelViewReferences / WhitelistFields / PruneModelViews / LoadModelSchemasForPrune） |
| Site/siteColumns/scan + UpdateSite 链 | ✅ | `internal/site/service.go`（PATCH 整体替换语义；校验失败返回 `*ModelViewError`） |
| PATCH handler 422 details | ✅ | `internal/httpapi/sites.go`（errors.As → writeErrorDetail：model_id/field/reason） |
| Release 快照 + 剪枝 + 审计 | ✅ | `internal/site/release.go`（ReleaseConfig.ModelViews；剪枝计数进 `site.release_published` 审计 `model_views_pruned`） |
| 读侧 fail-closed | ✅ | `public.go`：详情 `Fields []PublicFieldValue`（白名单序、带类型）、列表 `PublicPost.Fields` 卡片字段（查询补 pv.fields/field_schema/resource_model_id） |
| SSR 渲染 | ✅ | `DetailVM.Fields` / `CardVM.Fields` + `FormatFieldValues` 类型格式化（boolean→是/否、multiselect→顿号连接等）；detail.html 字段表、card.html meta 行、site.css 样式 |
| openapi + 字段级契约 | ✅ | PublicSite/SitePatch 补 model_views；新增 `site_schema_test.go`（Site 结构体 ↔ PublicSite schema 双向比对） |
| 单测 | ✅ | `modelview_test.go`（形状/上限/去重/fail-closed 过滤/剪枝矩阵） |
| 前端 | ✅ | `model/site.ts`（modelViews + normalize）、`fetchModelFieldSchemas`、「字段展示」Tab（chip 勾选、上限、类型徽章、复杂类型不进候选）、mock 与组件测试 |
| 真实冒烟 | ✅ | 本地栈（K7 库）：建站 → 建演示模型（record kind，draft 会被正确拒绝）→ 版本 validate/publish → PATCH 白名单 200 + 回读一致 → 未知模型/未知字段 422+details → Release 快照携带 model_views（pruned=0 入审计）→ 公开面 fail-closed |

**行为收紧提示**：上线后所有站点（含存量快照）的公开详情 JSON `fields` 变为只输出白名单字段——未配置即输出空数组。
