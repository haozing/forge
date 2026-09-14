# 模板上下文参考（主题化站点 · 给设计 Agent 与模板作者）

**版本：1.0（2026-09-14）**

## 版本与变更记录（§4.2：上下文版本化）

| 版本 | 日期 | 变更 | 兼容性 |
|---|---|---|---|
| 1.0 | 2026-09-14 | 首版定稿：槽位/上下文/函数白名单/query 原语/安全红线 | — |

- **兼容规则**：新增 VM 字段或新增白名单函数 = 兼容变更（版本号不变）；
  删除字段或改变字段语义 = 不兼容变更，上下文版本号 +1 并在此表记录。
- agent 侧以本文档为系统提示的技能底座；文档升级即技能升级。

> 本文档是站点主题模板的**唯一事实参考**：可用槽位、每个槽位拿到的数据上下文、
> 可调用的函数白名单、query 原语、以及安全红线。设计 Agent 产出模板文件前必读；
> 校验器返回的每条错误都能在本文找到对应规则。
> 规格出处：《站点主题化与AI设计重构》§4；代码出处 `internal/theme`。

## 0. 渲染模型一分钟

- 一套主题 = **槽位 → 模板源码**的文件集（存数据库，模板是数据不是代码）。
- 每个模板槽位必须定义 `content` 块：`{{define "content"}} … {{end}}`；
  `layout` 槽位负责整页骨架，并在正文中用 `{{template "content" .}}` 挂载页面内容。
- 未提供的槽位回退内置默认主题；`partials` 槽位提供可复用片段
  （`card`、`tag_chips`、`pager`），被引用时必须存在于同一棵模板树
  （你覆盖 `partials` 时要保留默认定义，或自行提供同名 define）。
- 所有插值默认 HTML 转义（html/template 按上下文转义/URL 过滤）。
  **没有也不允许 noescape**；唯一按原样输出的正文 HTML 是服务端净化过的
  `ContentHTML`（template.HTML 类型）。

## 1. 槽位一览

| 槽位 | 路由 | 上下文类型 | 说明 |
|---|---|---|---|
| `layout` | （全部） | 同页面槽位 | 整页骨架，必须含 `{{template "content" .}}` |
| `home` | `/` | HomeVM | 最新内容流 + 标签云 |
| `detail` | `/posts/{path}` | DetailVM | 内容详情（正文/字段/更新记录/上下篇/评论/附件） |
| `about` | `/about/` | DetailVM | 关于页（同 detail 字段） |
| `list` | `/posts/` | ListVM | 内容列表 + 分页 |
| `section` | `/sections/{key}` | ListVM | 栏目列表 |
| `category` | `/c/{path...}` | CategoryVM | 公开分类树节点（面包屑 + 子分类 + 内容） |
| `tags` | `/tags/` | TagsVM | 标签云索引 |
| `tag_page` | `/tags/{key}` | TagPageVM | 单标签归档 |
| `search` | `/search` | SearchVM | 搜索壳（结果经 `{{searchIsland}}` JS 岛） |
| `archive` | `/archive/` | `{Page; Years}` | 年/月归档 |
| `page` | `/p/{slug}` | CustomPageVM | 自定义页面（净化的正文 HTML） |
| `partials` | （无路由） | — | 片段 define 集合 |
| `tokens.css` / `theme.css` | 内联 `<style>` | — | 设计令牌与主题样式（纯 CSS，见 §6） |

单文件 ≤ 256KB，文件集总量 ≤ 1MB；未知槽位直接拒绝。

## 2. 全局上下文：每个页面都有

layout 与所有内容槽位的根对象都内嵌 **Page**：

| 字段 | 类型 | 说明 |
|---|---|---|
| `.Title` | string | 页标题（已含站点名后缀） |
| `.Description` | string | 页描述（meta description） |
| `.Canonical` | string | 规范 URL（绝对地址） |
| `.NoIndex` | bool | true 时 layout 输出 `noindex` meta |
| `.Site` | Chrome | 站点级页眉页脚信息，见下表 |
| `.Queries` | Queries | query 原语环境；用 `{{.Query (dict …)}}` 调用（§5） |
| `.Kind` | string | 页面种类（home/detail/…），可用于挂 class |

**Chrome（`.Site`）**：

| 字段 | 说明 |
|---|---|
| `.Slug` `.Name` `.SiteLang` | 站点标识、名称、语言码 |
| `.Nav` | 派生导航 `[]NavItem{Label, Href}` |
| `.Languages` | 语言切换项 `[]NavItem{Label, Href, Hreflang}`（多语言站点非空） |
| `.HomeHref` `.PostsHref` `.TagsHref` `.SearchHref` `.RSSHref` | 固定路由（可能为空 = 未启用） |
| `.LogoURL` `.FaviconURL` `.SocialImageURL` | 品牌媒体地址（空 = 未配置） |

默认 layout 已示范典型用法：导航 range、搜索表单（`method="get"` + 站内
`action`，这是表单的唯一合法形态）、`<style>{{themeCSS}}</style>`。

## 3. 各槽位上下文

### home（HomeVM）
- `.Items []CardVM`：最新内容卡片流。
- `.TagCloud []TagChip` / `.ShowTagCloud bool`。

### 内容卡片 CardVM（home/list/section/category/tag_page/related 通用）
`.Title` `.Href` `.Summary` `.PublishedOn`（YYYY-MM-DD）`.UpdatedOn`
`.CoverURL`（同源媒体地址，可空）`.Tags []TagChip`（Key/DisplayName/Href）
`.Fields []FieldValueVM{Key,Label,Type,Value}`（public_view 白名单字段的展示行）。

### detail / about（DetailVM）
`.Title` `.ContentHTML`（净化后的正文 HTML，template.HTML，直接输出）
`.TOC []Heading{ID,Text}`（≥2 项时目录）
`.Fields []FieldValueVM` `.Tags []TagChip` `.CoverURL` `.CoverAlt`
`.PublishedOn` `.UpdatedISO`（RFC3339，给 `<time datetime>`）
`.VersionNo`（版本号，0 = 无版本）`.Publications []PublicationVM{VersionNo,PublishedOn,ChangeNote,AILabel}`
（更新记录；模板惯例 `len ≥ 2` 才渲染折叠块）
`.Prev` / `.Next *NeighborLink{Title,Href}`（同站上下篇）
`.Attachments []AttachmentVM{Name,URL,MediaType,ByteSize}`
`.CommentsEnabled` `.Comments []CommentVM{Author,Body,Created}` `.CanComment`
`.Moderation` `.PostPath`（评论表单提交地址）
`.Related []CardVM`（相关内容，可空）
`.Section` `.SectionHref`（所属栏目）。

### list / section（ListVM）
`.Heading` `.Items []CardVM` `.Pagination.NextHref`（下一页链接，空 = 无）。

### category（CategoryVM）
`.Heading` `.Crumbs []CrumbVM{Name,Href}`（面包屑，含上级不含当前）
`.Subcategories []SubcategoryVM{Name,Href,Count}` `.Items []CardVM`。

### tags（TagsVM）`.Tags []TagChip`（含 `.Count`）
### tag_page（TagPageVM）`.TagName`（展示名）`.TagKey` `.Items []CardVM`
### search（SearchVM）`.Query`（回填当前词）；结果区放 `{{searchIsland}}`
### archive
`.Years []{Year string; Months []{Month,Label string; Items []CardVM}}`
### page（CustomPageVM）`.Heading` `.ContentHTML`
### gate / error（GateVM/ErrorVM）
门禁页与错误页也走同一 layout：`.Status int`（error）。

## 4. 函数白名单

模板内可调用的**全部**函数（其余一律拒绝，Parse 阶段报 unknown function）：

| 函数 | 签名/用法 | 说明 |
|---|---|---|
| `query` | `{{.Query (dict "model" "x" "limit" 5)}}` | 公开内容查询原语，见 §5 |
| `dict` | `(dict "k1" v1 "k2" v2)` | 构造 map，专配 query |
| `assetURL` | `{{assetURL "附件id"}}` | → `/sites/{slug}/media/{id}` |
| `absURL` | `{{absURL .Canonical}}` | 补站点绝对前缀（无配置时原样） |
| `dateFmt` | `{{dateFmt "2006-01-02" .UpdatedAt}}` | Go 参考时间布局；支持 time/*time/ISO 字符串 |
| `truncate` | `{{truncate 60 .Summary}}` | 按 rune 截断加 … |
| `json` | `{{json .}}` | 序列化为 JSON（自动转义 `< > &`） |
| `i18n` | `{{i18n "search"}}` | 站点文案表（home/posts/tags/search/about/archive/published/updated/next/prev/categories/latest/empty_list/back_home/comment/attachments/read_more） |
| `now` | `{{dateFmt "2006" now}}` | 当前时间 |
| `themeCSS` | `{{themeCSS}}` | 输出 tokens.css + theme.css（仅 layout 的 `<style>` 内用） |
| `searchIsland` | `{{searchIsland}}` | 搜索 JS 岛 script 标签（唯一合法的 script 来源） |
| Go 内建 | `if else end range with template define block and or not eq ne lt le gt ge len index slice print printf println urlquery html js` | 与标准库一致 |

**永久禁止**：`noescape`、`call`、`exec` 及任何白名单外函数。

## 5. query 原语（附加数据拉取）

页面主数据由系统推送（§3 各槽位）；页面需要的**附加数据**用 query 拉：

```
{{$latest := .Query (dict "limit" 5)}}                        ← 默认：全部收录内容
{{$docs  := .Query (dict "model" "builtin_document" "limit" 3)}}
{{$cams  := .Query (dict "model" "camera" "limit" 10 "sort" "newest"
                         "filter" (dict "brand" "Canon"))}}
{{range $latest.Items}}
  <article class="card">
    <h3><a href="{{.Href}}">{{.Title}}</a></h3>
    {{with .Summary}}<p>{{.}}</p>{{end}}
    {{range .Fields}}{{if .Label}}<div>{{.Label}}: {{.Value}}</div>{{end}}{{end}}
  </article>
{{end}}
```

参数（`dict` 键值对）：

| 键 | 类型 | 说明 |
|---|---|---|
| `model` | string | 内容模型 key；空 = 全部收录内容 |
| `limit` | int | 1–20（超限钳到 20，非法回退 10） |
| `sort` | string | `newest`（默认，按发布时间降序）/ `oldest`（升序）。updated/数值字段排序在路线图中，当前传入会被按发布时间处理（不报错） |
| `filter` | dict | 公开字段 key → 精确值（仅 public_view 白名单字段生效） |

返回 `QueryResult`：`.Items []QueryItem` 与 `.Total int`；`QueryItem` 含
`.Title` `.Href` `.Summary` `.PublishedOn` `.CoverURL` `.Fields`（展示名→格式化值 map）
`.Tags`（展示名数组）。

**预算**：单次页面渲染最多 **8 次** query；第 9 次整个渲染报错（校验期就会
发现拖库写法）。数据源是派生收录视图——三道闸（已发布/站点绑定/公开范围）
在视图层生效，模板不存在查出非公开数据的语法。

## 6. CSS 槽位（tokens.css / theme.css）

- `tokens.css`：设计令牌（`--color-*`、`--font-*` 等自定义属性），agent 换肤改这里。
- `theme.css`：主题样式本体。
- 两者合并后经 `{{themeCSS}}` 内联进 layout 的 `<style>`。
- 限制：禁 `@import`；`url()` 只允许 `data:image/…`、`/sites/…`（站内媒体）与 `none`。

## 7. 安全红线（扫描器逐条拒绝的写法）

模板/CSS 源码在保存与 apply 时全量扫描，命中即拒并按「文件:行:规则」返回。
按**浏览器解码后**的视角匹配（HTML 字符引用冒号、URL 内剥除的空白等变体同样拦截）：

| 规则 | 拒绝的写法 |
|---|---|
| `script_tag` | 任何 `<script`（含大小写/空白变体）；脚本只能经 `{{searchIsland}}` |
| `event_attr` | `on*=` 事件属性（onclick/onerror/…） |
| `js_url` | `javascript:`（含 `&#58;`、`&#x3a;` 实体冒号、字母间空白变体） |
| `data_html` | `data:text/html`（含实体冒号变体） |
| `danger_tag` | `<iframe` `<object` `<embed` |
| `form_rule` | `<form>` 仅允许 `method="get"` 且 `action` 为站内相对路径 |
| `noescape_forbidden` | 源码任何位置出现 `noescape`（含注释） |
| `unknown_function` | 白名单外函数 |
| `css_import` | `@import` |
| `css_url` | `url()` 白名单外地址 |
| `unknown_slot` / `file_too_large` / `set_too_large` / `missing_content_mount` | 结构性约束 |

第二道闸是 html/template 的上下文转义与 URL 过滤：即使恶意字符串经数据进入
VM，插值处也会转义，`javascript:` 形态的 href 会被替换为 `#ZgotmplZ`。
但**不要依赖第二道闸写模板**——扫描被拒的文件根本进不了发布流。

## 8. 校验错误格式

保存/apply 返回的 `issues` 是结构化清单，agent 应逐条自修复：

```json
{"issues": [{"file": "home", "line": 3, "rule": "script_tag",
             "detail": "模板禁止 <script>（脚本经 {{searchIsland}} 白名单输出）"}]}
```

`rule` 与 §7 表格一一对应；`file` 是槽位名，`line` 从 1 计。
