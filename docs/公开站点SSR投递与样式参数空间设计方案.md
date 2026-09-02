# 公开站点 SSR 投递与样式参数空间设计方案

> 状态：目标设计（待评审；已经代码审计修订，修订记录见 §15）
> 版本：2026-09-01 r2，基于 main `21e974b`
> 范围：公开站点（Delivery）的 HTML 投递层、缓存层、样式参数空间（L1）、Agent 样式工具
> 前提：开发期，不做数据迁移与双写兼容；存量数据仅靠新列默认值与 NULL 回退自然兜底（§7.4 引导路径），不做回填。本方案与 `docs/CMS呈现层前后端设计方案.md`（下称 CMS 方案）是承接关系——CMS 方案定义的 ViewModel/Renderer/固定组件目录/Release 等概念在此具体化为可实施设计，冲突处以本方案为准

## 1. 目标与非目标

### 1.1 目标

1. 公开站点具备服务端渲染的 HTML 投递面（Go SSR + 页面级缓存），SEO 达到静态站同级；
2. 样式系统落地为封闭的声明式参数空间（L1：design tokens + 预设 + 覆写），覆盖绝大部分日常样式诉求；
3. 用户可以通过 Agent 以自然语言修改站点样式：意图 → 参数补丁 → 真实渲染预览 → 人工确认 → 发布；
4. 复用现有事件管道（River outbox）做缓存失效，借鉴帝国/PHPCMS 的增量关联失效链，做到"发布即可见、漏失效有 TTL 兜底"。

### 1.2 非目标（边界）

- 不做纯静态文件生成与 CDN 推送；
- 不做自定义 CSS、自定义组件、任意页面模板、模板在线编辑（CMS 方案 §2.3 红线不变）；
- 不做 CMS 管理端本身（React，另行实施）；本方案只定义管理端需要消费的接口；
- 不改变资产生产流程：Delivery 只读已发布版本（v2 §7.2）。

## 2. 产品依据

| 出处 | 依据 |
| --- | --- |
| v2 §7.3 | 站点能力清单：首页/列表/详情/栏目/标签页/站内搜索/来源展示/安全渲染/范围控制 |
| v2 §7.2 | PublicSite 绑定 Asset 不绑定版本，读 current_published_version_id；站点无版本固定 |
| v2 §2.3/§19.1 | 五出口同源（原文位于 产品文档-v2.md 第 49 行，属 §2.3）；对外定位"沉淀专业经验"，内容站 SEO 是核心分发渠道 |
| CMS 方案 §11.5 | 首屏 SSR、meta 服务端生成、RSS/Sitemap、登录站 noindex、无 JS 可读 |
| CMS 方案 §12 | 固定组件目录（本方案 §5.3 直接映射为模板组件） |
| 治理哲学（§8.3 同构） | Agent 产候选、人在发布闸门确认；样式与内容走同一条治理线 |
| 帝国/PHPCMS 借鉴 | 增量关联失效链、组件级片段缓存、声明式组件参数、公共 partial；明确不借鉴模板内嵌 SQL/PHP/在线编辑 |

## 3. 总体架构

```text
资产系统（已发布版本）
        |
        | 事件: asset.published / asset.archived / asset.visibility_changed
        |       site.site_changed / site.binding_changed（internal/eventing/catalog.go 实名，均已存在）
        v
River outbox ──> worker: 失效编排器（§6.4）
        |                计算 (site, 路由集合) → 写 delivery.cache_invalidations 表
        v
api (Go) ── internal/delivery
   ├─ Router     /sites/{slug}/...  HTML 路由（§4）
   ├─ ViewModel  Resolver→Hydrator→ViewModel（白名单字段，CMS 方案 §9 同构）
   ├─ Renderer   html/template 渲染（布局+partial+组件 §5）
   ├─ StyleEngine style_config 校验 → CSS variables 注入（§7）
   └─ Cache      页面级缓存 + 失效表轮询（§6）
搜索岛：前端少量 vanilla JS 调既有 /api/public/sites/{slug}/search
管理端(React)：站点/绑定/style_config 管理 + 预览（§8）
Agent：site_style_suggest 工具（§8.3，实施更名：OpenAI 兼容端点函数名禁止点号）
```

新增代码位置：`internal/delivery/`（router.go、viewmodel.go、renderer.go、style.go、cache.go、invalidate.go）+ `internal/httpapi/delivery_routes.go`（路由注册）+ worker 侧 `internal/delivery/invalidator.go`。

## 4. HTML 投递面：路由与页面

### 4.1 路由表

同一 Go 服务挂载，与 `/api` 并存（开发期无独立域名；生产由部署层按 Host/域名分流到同一路由树，`site.public_sites.domain` 字段已预留）：

| 路由 | 页面 | 数据源 |
| --- | --- | --- |
| `GET /sites/{slug}/` | 首页 | Release 首页配置 + 精选/最新组件 |
| `GET /sites/{slug}/posts/` | 文章列表 | 统一查询（公开 band） |
| `GET /sites/{slug}/posts/{displayPath...}` | 文章详情 | Binding → current_published_version_id |
| `GET /sites/{slug}/sections/{sectionSlug}/` | 栏目页 | Section 绑定集合 |
| `GET /sites/{slug}/tags/` 与 `/tags/{key}/` | 标签页 | 既有公开标签 API 同源数据 |
| `GET /sites/{slug}/search` | 搜索页壳 | 静态壳 + JS 岛调 `/api/public/sites/{slug}/search` |
| `GET /sites/{slug}/rss.xml` | RSS | 全量已发布绑定 |
| `GET /sites/{slug}/sitemap.xml` | Sitemap | 全量路由枚举 |
| `GET /sites/{slug}/robots.txt` | robots | 按站点范围生成（slug 模式下爬虫不会自动发现，起信息作用；绑定域名路由上线后于宿主根生效） |

统一行为：404/410 页面、`disabled` 站点整站 404、`organization/workspace` 范围站点对匿名访客返回 noindex 登录引导页（§9）；全站复用 JSON 公开面既有的 PublicThrottle IP 限流（internal/site/public.go:55，429 + Retry-After，节流库故障 fail-closed 503）。

### 4.2 与 JSON API 的关系

`/api/public/sites/...`（已实现）继续作为搜索岛与潜在第三方消费的 JSON 面；HTML 面复用 `internal/site/public.go` 的既有取数层（方法挂在 PublicReader 而非 Service：PublicReader.Home/Posts/Post/Section/Tags，internal/site/public.go:61；PublicSiteQuery 经 PublicReader.Query 字段注入，public.go:65），不允许出现两套可见性判断（CMS 方案 §9 同款红线）。现状核验：该层的访客分档、ETag、防探测 404 已实现，HTML 面是在其上加渲染而非重写取数。搜索页壳之外的一切页面服务端出全量 HTML。

## 5. 模板系统

### 5.1 组织

```
internal/delivery/templates/
  layout.html          // 页面外壳：head/meta/CSS variables 注入点/header/footer
  partials/
    header.html  footer.html  post_card.html  tag_chip.html
    pagination.html  breadcrumbs.html  toc.html  empty.html
  pages/
    home.html  list.html  detail.html  section.html
    tag_index.html  tag_page.html  search.html
    rss.xml.tmpl  sitemap.xml.tmpl  robots.txt.tmpl
  errors/
    404.html 410.html 403.html 500.html
```

- 全部 `embed.FS` 打包进二进制，随版本发布；模板编译在进程启动时完成并 `panic on error`；
- 公共 partial 对应帝国 `[!--temp.header--]` 的现代化等价物；
- 模板只接收 ViewModel struct，接收不到原始资产 DTO（编译期保证：ViewModel 是具体 Go 类型）。

### 5.2 Markdown 与安全

- 服务端渲染：goldmark（GFM：表格/删除线/任务列表，go.mod 已有）→ bluemonday 清理（**需新增依赖**；白名单策略与 CMS 方案 §"安全渲染"一致：放行语义标签，剥 script/iframe/事件属性/style）；
- 结构化字段按资源模型 `frontend` outlet 白名单输出（CMS 方案既有定义）；
- html/template 自动转义兜底；附件只出受控下载链接，不内联未清理内容。

### 5.3 组件目录映射（CMS 方案 §12 → 模板组件）

每个组件 = 一个 partial + 一个参数 struct + 一个数据预取器（Go 函数）。**预取器即 PHPCMS `{pc:}` 标签函数的安全版**：模板声明"要什么、多少条"，预取器负责产出 ViewModel 切片，模板侧无任何查询能力。

红线（v2 §7.3）：一切需要内容筛选/标签/搜索的预取器**必须**经 `PublicSiteQuery`（ForPublicSite 范围编译，internal/query/scope_compiler.go:281；PublicSiteQuery 装配于 internal/query/service.go:119）取数，禁止直连资产表 SQL——HTML 面与既有 JSON 公开面（internal/site/public.go）共用同一套可见性判断，不允许出现第二套。

| 组件（CMS 方案 §12） | 预取器 | 参数（ViewModel 字段） |
| --- | --- | --- |
| content_list@1 | `fetchContentList(binding, limit, cursor)` | 每页数量、摘要开关、排序 |
| featured_content@1 | `fetchBound(content_type=featured)` | 条数 |
| latest_articles@1 | `fetchLatest(section?)` | 条数、时间格式 |
| tag_filter@1 / tag_cloud@1 | `fetchTags(site)` | 数量上限、排序 |
| related_content@1 | `fetchRelated(asset, tags)` | 条数、匹配方式 |
| article_pager@1 | `fetchNeighbors(published_at)` | — |
| breadcrumbs@1 / table_of_contents@1 | 纯计算，无 IO | 最大深度/标题深度 |
| site_header@1 / site_footer@1 / search_box@1 / search_results@1 | Release 配置 | 开关与文案 |

## 6. 缓存设计（页面级 + 事件失效 + TTL 兜底）

### 6.1 键与层级

```text
页面级:  page:{site_id}:{release_rev}:{tier}:{route_path}
```

只做页面级缓存，**不做片段级**：页面形态仅首页/列表/详情等少数几类，片段复用收益覆盖不了 param_hash + 内容指纹的键复杂度；待 profiling 证明有必要再议（§14）。

- `tier` = 访客可见档位（`anon` | `member`）。分档由 HTTP 层 publicVisitorPrincipal（internal/httpapi/public_sites.go:37）+ PublicReader.visitor（internal/site/public.go:163）+ ForPublicSite 编译器（internal/query/scope_compiler.go:281）组成（v2 §7.3 范围控制），organization/workspace 范围站点对两类访客渲染结果不同——**键缺档位会把成员可见内容泄漏给匿名访客**，这是硬约束不是优化项。

- `release_rev` = 站点 Release 版本号（§7.4）；样式或配置一变，全站键自然换代，无需逐键失效；
- 存储：进程内朴素 map + TTL，带条目数上限（默认 1 万条、可配，超限按插入序淘汰）；不引入 LRU 库、不预设内存字节配额——当前公开站点数为零，容量参数待有真实流量后按实测调整。单飞（`golang.org/x/sync/singleflight`，x/sync 已是直接依赖）防冷键击穿；
- 响应头：`Cache-Control: public, max-age=30, stale-while-revalidate=300`；同时输出 `ETag`（= release_rev + tier + route_path 哈希），命中条件请求回 304——与 JSON 公开面既有的 ETag 语义（public_sites.go:99 publicCacheHeaders）对齐。

### 6.2 失效链（借鉴帝国关联生成，方向相反为"失效"）

worker 以新增 consumer key `delivery.cache` 注册为既有事件的消费者（注册先例：internal/eventing/eventing.go:128-151），失效编排器计算受影响路由集合后**写 `delivery.cache_invalidations` 表**（site_id + 路由前缀 + created_at）；api 进程内一个轮询 goroutine 每 2s 取未处理行、按键前缀删除内存缓存并标记已处理——幂等、重启安全（未处理行持久在库）。**不新增 api↔worker 之间的 HTTP 内部端点**：现状两进程只经 Postgres 交换状态（outbox、River 队列、worker_heartbeats），worker 不监听 HTTP，保持这一边界。

| 事件（internal/eventing/catalog.go 实名） | 失效范围 |
| --- | --- |
| asset.published / asset.archived / asset.visibility_changed | 该资产绑定到的所有站点：详情页、所属 section 列表、首页、相关标签页、RSS、Sitemap、列表页第 1 页与相邻页 |
| site.binding_changed | 该站点：受影响路由 + 首页 + Sitemap |
| site.site_changed（Release 发布） | 该站点整站（实际由 release_rev 换代自动完成，写失效行仅作显式确认） |
| workspace.membership_changed | 成员加入/离开组织波及的 organization/workspace 范围站点整站 **member 档**——tier 键是防泄漏硬约束（§6.1），成员资格变化必须进失效链，不能只靠 TTL 兜底 |
| tag.updated / tag.archived / tag.restored | 该标签波及的站点标签页与含标签组件的首页/列表页 |

### 6.3 兜底

- 每键 TTL 硬上限 300s：即使失效链断（worker 积压/轮询停摆），陈旧窗口 ≤5 分钟；
- 不做夜间全量预热：内存缓存重启即空属常态，预热救不了白天发布，TTL 已兜底；
- 失效表积压（最老未处理行 > 60s）与缓存命中率走结构化日志定期打点——仓库现状无 metrics 栈（无 prometheus/OTel，仅日志 + healthz/readyz），首期不为本方案引入，见 §11。

### 6.4 为什么是"worker 写表 + api 轮询"，而不是 HTTP PURGE 或 api 内嵌订阅

事件已走 River outbox，worker 是既有消费者，失效编排（事件 → 受影响路由集合）放 worker 可复用其重试语义。api 与 worker 之间**不新增 HTTP 内部端点**（审计修订：原稿的 `POST /internal/cache/purge` 需要"仅 worker 网络可达 + 共享 secret"的全新通道，而现状两进程只通过 Postgres 交换状态、worker 不监听 HTTP）；api 内嵌 LISTEN/NOTIFY 则会让失效逻辑分散在两处。DB 失效表 + 2s 轮询以一次可忽略的查询代价同时规避两者，且未处理行天然持久、幂等、失败重投由 River 重试语义覆盖。

## 7. 样式参数空间（L1）

### 7.1 原则

样式 = 封闭的声明式参数空间，**不是** CSS/模板代码。表达力靠三层叠加：

```text
preset（高维起点） ⊕ override（参数覆写） ⊕ IA 参数（信息架构）
```

### 7.2 style_config 结构（落库 `site.public_sites.style_config jsonb`，schema 校验）

```jsonc
{
  "preset": "magazine",          // 预设：calm | magazine | minimal | warm | archive（模板代码内置）
  "tokens": {                    // design tokens，全部有值域校验
    "color": {
      "primary": "#2E7D32",      // 任意 hex；暗色模式自动派生（明度反转算法，不允许手写两套）
      "surface": "", "text": "", // 空串 = 跟随 preset
      "mode": "light | dark | auto"
    },
    "typography": {
      "heading_font": "serif | sans",   // 字体只允许内置两族（衬线/无衬线），不加载外链字体
      "body_size": 16,           // 15-19 连续
      "reading_width": 720       // 640-860 连续（px）
    },
    "density":  "airy | normal | compact",   // 映射全局间距刻度
    "radius":   "sharp | soft | round",
    "shadow":   "flat | subtle | lifted"
  },
  "layout": {
    "home_style": "hero | plain | grid",     // 首页形态（§11.1 反营销 Hero：hero 为内容型头图，非落地页）
    "list_style": "list | grid",
    "card_ratio": "16:9 | 4:3 | 1:1 | text",
    "sidebar": "none | toc | tags"
  },
  "ia": {                          // 信息架构参数（同属 agent 可改范围）
    "home_components": ["featured","latest","tag_cloud"],  // 组件顺序白名单枚举
    "summary_length": 160,        // 80-320
    "posts_per_page": 12          // 6-24
  }
}
```

- 校验：Go struct + 自定义校验器（枚举/区间/hex 正则），非法值在写入与渲染两侧都拒绝。**渲染侧行为修订（2026-09-02 实施决策回写）**：已入库的损坏样式文档（写入侧全部拦截，正常不可达）在渲染侧**降级到预设默认而非拒绝整页**——单行坏数据不应让整站 500，且降级只影响观感不影响安全（校验器保证的值域约束在降级后依然成立）；
- 未设置字段全部回退 preset 默认——**patch-merge 语义天然支持多轮迭代**（agent/人工都可以增量改）。

### 7.3 渲染消费（StyleEngine）

- `style.Config → 校验 → 归并 preset → 生成 CSS custom properties 块`，注入 `<style>:root{--primary:...;--density-unit:...}</style>`（内联、无外部请求、暗色模式经 `prefers-color-scheme` 与 `data-mode` 属性双通道）；
- 布局参数 → 根元素条件 class（`layout--grid` 等），模板内不出现任何颜色/间距字面量；
- WCAG：preset 出厂配色过 AA 对比度校验；用户自定义 primary 时运行时计算对比度，不足 4.5:1 时拒绝并提示（agent 场景把提示回给模型自动修正）。

### 7.4 站点 Release（配置不可变版本）

新增 `site.site_releases`。**边界澄清（对照 v2 §7.2"v2 不提供站点级版本固定能力"）**：该禁令针对的是内容版本固定（站点不得固定 AssetVersion）；本表的 Release 只固定**配置快照**（homepage/navigation/style/template），内容仍每次解析 current_published_version_id——配置版本化是 CMS 方案"发布不可变的站点配置版本"的既有要求，不与 v2 冲突。

```sql
CREATE TABLE site.site_releases (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    organization_id uuid NOT NULL REFERENCES organization.organizations(id),
    workspace_id uuid NOT NULL,
    site_id uuid NOT NULL,
    revision bigint NOT NULL CHECK (revision > 0),
    config jsonb NOT NULL,        -- {homepage_config, navigation_config, style_config, template}
    published_by uuid NOT NULL REFERENCES identity.users(id),
    created_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (organization_id, id),
    UNIQUE (site_id, revision),
    FOREIGN KEY (organization_id, workspace_id)
        REFERENCES content.workspaces (organization_id, id),
    FOREIGN KEY (organization_id, site_id)
        REFERENCES site.public_sites (organization_id, id)
);
ALTER TABLE site.public_sites
    ADD COLUMN published_release_id uuid;
ALTER TABLE site.public_sites
    ADD CONSTRAINT public_sites_published_release_fk
    FOREIGN KEY (organization_id, published_release_id)
    REFERENCES site.site_releases (organization_id, id);
```

- DDL 对齐仓库惯例：org 内引用一律复合外键（0005/0009/0010 大量先例；原稿两个裸 UUID FK 已修正），workspace_id 对齐同域的 site_content_bindings（0010_site.sql:44）；
- **引导路径（存量行兜底）**：`published_release_id` 为 NULL（从未发布过 Release，含开发库既有站点行）时，公开渲染回退读行上的工作配置列（homepage_config/navigation_config/style_config/template）——站点不会因没发过 Release 而整站消失；首次发布 Release 后切换为快照模式。`style_config` 新列带 `DEFAULT '{}'`，存量行零迁移自然落默认；
- 公开渲染优先读 `published_release_id` 指向的快照（工作区配置修改不影响线上，发布即换代——缓存 `release_rev` 键随之整体换代）；
- 回退 = 把指针拨回历史 revision（新 Release 记录或指针更新，均留审计）；M4 只实现拨指针最小语义，独立 rollback API 延后到有真实使用诉求（§13）；
- Agent 样式建议与人工修改都只产生 Release 候选，**确认发布永远是人**。

## 8. Agent 样式工具（自然语言 → 参数补丁）

### 8.1 交互形态

```text
用户(管理端或会话): "首页太素了，想要杂志感，主色用品牌绿，紧凑一点"
   ↓ Agent（react 工具调用）
site_style_suggest(instruction, site_id?)  // 工具服务端自查站点配置与内容摘要，模型只传意图
   ↓ structured output（json_object 模式；端点为 integration.model_endpoints 运行时配置的 openai_compatible 项，能力探测与调用先例：internal/agentruntime/asset_candidate.go、openai_factory.go:106）
返回 2-3 个候选补丁（每个 = style_config patch + 一句设计理由）
   ↓ 管理端逐个真实渲染预览（§8.2）
用户选中其一（或继续多轮："这个方向对，再亮一点"）
   ↓ 人工点击发布
写入 style_config → 创建 Release → 缓存换代 → 线上生效
```

### 8.2 预览

- 扩展现有预览端点为**真实 Delivery 渲染**：`POST /api/workspaces/{ws}/sites/{siteId}/preview`，body `{style_config?: object, page?: string}`（候选样式 + 想预览的页面路由，缺省首页），调用与线上一致的 Renderer + StyleEngine，返回带 `X-Robots-Tag: noindex` 的完整 HTML（CMS 方案"预览必须使用真实 Delivery 页面"的落地；现有 GET JSON 快照保留给管理端列表视图）。候选 style_config 复用 §7.2 校验器，非法即 422；
- 预览 token 短时效、绑定成员身份，禁止未授权访问（§10）。

### 8.3 工具定义（接入现有 agentruntime）

- 新增 builtin 工具 `site_style_suggest`（实施名将原文的 site.style.suggest 更名：OpenAI 兼容端点的函数名模式 ^[a-zA-Z0-9_-]+$ 拒绝点号，实测 400；capability 串仍为 `site.style`）（`internal/agentruntime/tools/builtins.go` 注册，RegisterBuiltins 先例 builtins.go:50）。**权限决策（审计修订，不用 site.manage）**：authz 动作 `site.manage` 是 admin-only 红线（internal/authz/roles.go:103），且 `agentAllowedActions` 明确排除 site.\*（internal/authz/actions.go:64-74，"Agents never manage sites"）；本工具无副作用——只读站点配置 + LLM 产候选补丁，落库与发布都由人完成——故定为 ReadOnly 风险档 + 新增工具级 capability 串（与 `asset.read` 等既有 capability 平级，不经 authz 动作目录）；真正的写路径（发布 Release）仍走管理端 API，由人持 `site.manage` 完成；
- 运行面约束：工具调用发生在 react/workflow 应用的 runs 通道（`POST /api/agent-sessions/{id}/runs`，SQL 强制 runtime_mode='react'，internal/httpapi/agent_runs.go:105），rag 直连 chat 不可见——站点管理 Agent 应注册为 react 应用；
- 输出 schema 即 style_config 的 patch 子集（复用 §7.2 校验器，越界参数在工具层拒绝并回传修正提示给模型——一次自愈循环）；
- 失败语义：模型输出不合法 → 422 `style_patch_invalid`；无 site.manage 权限 → 403（与现有工具权限模型一致）；
- 多候选 = 一次工具调用返回数组，管理端渲染对比视图。

### 8.4 灵活性边界（明确写入产品文档供对齐）

- 覆盖：颜色/字体/密度/布局/首页形态/组件顺序/摘要分页等 token 与 IA 级诉求（日常样式的绝大部分）；
- 不覆盖：参数空间外的新视觉范式与新组件——通过产品迭代加 preset/参数解决；二期可上 L2 受控 CSS 子集（sanitizer 白名单属性，仍走预览+Release），三期 L3 主题包。本方案只实施 L1。

## 9. SEO 实施细则

| 项 | 规则 |
| --- | --- |
| title/description | 服务端生成：详情 = 资产标题 + 站点名；列表 = 栏目/标签名 + 站点名 |
| canonical | 每页唯一规范 URL（基于 slug + display_path），分页页 canonical 指向自身并带 rel="prev/next" |
| Open Graph / article | title/summary/updated_at（`article:modified_time` 取 current_published_version 时间） |
| 结构化数据 | 文章详情输出 JSON-LD（Article/Person/BreadcrumbList） |
| noindex | organization/workspace 范围站点整站 noindex 且不出 RSS/Sitemap；预览页永远 noindex |
| RSS | `/sites/{slug}/rss.xml`，仅 public 范围站点，最近 50 条 |
| Sitemap | 全路由 + `lastmod`（取资产发布时间），分页超过 5 万自动分片（首期单文件足够） |
| 无 JS 可读 | 除搜索结果外所有页面无 JS 完整可读（CMS 方案 §11.5） |

## 10. 安全红线（汇总）

1. 模板只接 ViewModel（编译期类型约束）；无模板内查询、无模板内代码；
2. Markdown 服务端渲染 + bluemonday 白名单清理；html/template 自动转义；
3. 样式只有 CSS variables 注入，无用户 CSS、无外链字体；
4. 预览走成员鉴权 + 短时效 token + noindex；
5. 失效通道只经 Postgres（worker 写 `delivery.cache_invalidations` + api 轮询消费），api↔worker 之间不新增任何 HTTP 内部端点；
6. 未发布版本、草稿、working version 永不出现在 HTML 面（Resolver 只解析 current_published_version_id）；
7. HTML 面统一下发 CSP 与安全响应头：`Content-Security-Policy: default-src 'none'; style-src 'unsafe-inline'; script-src 'self'; img-src 'self'; base-uri 'none'; frame-ancestors 'none'`（CSS variables 经内联 `<style>` 注入故仅放行内联样式；搜索岛 JS 打包为同源静态文件下发，不放行 inline script），另加 `X-Content-Type-Options: nosniff`、`Referrer-Policy: strict-origin-when-cross-origin`；
8. HTML 面复用 JSON 公开面的 PublicThrottle IP 限流（429 + Retry-After、节流库故障 fail-closed 503）与公共面免 Idempotency-Key 约定——爬虫流量与恶意刷取同规则治理。

## 11. 可观测性与验收指标

- 可观测性：缓存命中率、失效表积压、渲染耗时分布（冷/热）、RSS/Sitemap 生成计数——首期全部走结构化日志定期打点（仓库无 metrics 栈，不为本方案单独引入 prometheus/OTel，列为后续独立事项）；
- 验收标准：
  - 热路径 TTFB p95 < 50ms（进程内缓存命中）、冷渲染 p95 < 200ms；
  - 稳态缓存命中率 > 95%（日志打点口径）；
  - 发布事件后 ≤5s 受影响页面可见新内容（失效链生效：一个轮询周期 + 处理余量）且 5 分钟内必然可见（TTL 兜底）；
  - Lighthouse SEO 分 ≥ 98（public 站点抽样）；
  - style_config 非法值（越界枚举/hex/区间）写入与渲染双侧 422；
  - Agent 建议链路：意图 → 候选 → 预览 → 发布 → 线上生效全流程 < 1 分钟（不含人思考时间）。

## 12. 数据模型与 API 变更清单

迁移 `0015_delivery_style_release.sql`（0015 恰为下一空闲编号，目录现状最新为 0014_agent_answer_posture.sql）：
- `site.public_sites` 加 `style_config jsonb NOT NULL DEFAULT '{}'`（存量行靠默认值兜底）、`published_release_id uuid` + 复合外键（§7.4）；
- 新建 `site.site_releases`（§7.4，复合键惯例）；
- 新建 `delivery.cache_invalidations`（§6.2）；
- 应用层注意：`internal/site/service.go` 的 INSERT 与读取均为显式列清单（siteColumns），新列必须两侧同步扩展——SELECT 侧漏列是静默缺失，比报错危险。

API 新增/扩展：
- HTML 面：§4.1 路由表。注意现有双向契约门（openapi_contract_test.go 的 routerTruth）正则只匹配 `/api` 前缀，`/sites` 路由不进该门——delivery 包自带一张路由注册表测试（表驱动断言每个路由+方法已注册），防止路由漂移无人知晓；
- `POST /api/workspaces/{ws}/sites/{siteId}/releases`（发布 Release）、`GET .../releases`（历史）、`POST .../releases/{id}/rollback`（回退）；
- `PATCH /api/workspaces/{ws}/sites/{siteId}` 扩展 `style_config` 字段（管理端直接调参入口，与 agent 候选共用校验器）；
- preview 端点扩展：`?style_config=<候选>` 或 POST body，返回真实 HTML；
- worker：consumer key `delivery.cache` + 失效编排器（写 delivery.cache_invalidations）；api 侧轮询 goroutine。不新增任何 HTTP 内部端点。

## 13. 实施顺序

1. **M1 投递骨架**：`internal/delivery` 包 + 路由表 + ViewModel 管线 + 模板骨架（calm 预设）+ 404/错误页；验收 = 详情/列表/首页服务端 HTML 可访问、SEO meta 齐；
2. **M2 缓存与失效**：页面级缓存（map+TTL+singleflight）+ 失效链（delivery.cache 消费者 → 失效表 → api 轮询）+ TTL 兜底 + 日志打点；验收 = §11 全部指标；
3. **M3 样式参数空间**：迁移 0015 + StyleEngine + preset 三套（calm/magazine/minimal）+ 对比度校验 + 管理 PATCH；
4. **M4 Release 与预览**：site_releases + 发布（回退仅拨指针，独立 rollback API 延后）+ 存量站点 NULL 回退路径 + 真实渲染预览；
5. **M5 Agent 工具**：site_style_suggest + 多候选 + 预览闭环；
6. **M6 补全**：RSS/Sitemap/robots/JSON-LD/暗色模式/知识库预设组件（document_tree 等）；
7. 每步过 itd 回归 + openapi/routerTruth 同步；M1-M5 各自独立可发布。

## 14. 风险与开放问题

| 风险 | 对策 |
| --- | --- |
| 失效链断导致陈旧 | TTL 300s 硬兜底 + 失效表积压日志告警（最老未处理行 > 60s） |
| 模板迭代节奏慢于样式诉求 | preset/参数按迭代加；agent 工具的 patch schema 随参数空间同步扩展 |
| LLM 输出越界参数 | 双侧校验 + 工具层自愈重试（失败详情回传模型，含候选站点清单）+ 422 语义 |
| 多站点缓存内存压力 | 条目数上限 + 插入序淘汰；站点配额与分级缓存待有真实流量后按实测引入 |
| display_path 变更产生死链 | binding 变更事件中检测 path 变化，Sitemap 即时反映；二期可加 301 映射表（不在本期） |
| 开放问题 | 预览对比视图的交互稿（管理端 React 范围）；`pro` 知识库预设的组件清单细化——均不阻塞 M1-M4 |

## 15. 审计修订记录（2026-09-01 r2）

对照代码逐条核验后修订（核验基线：main `21e974b`）：

1. **事件名更正**：`site.changed` → `site.site_changed`（catalog.go:52 实名）；失效链补入 `workspace.membership_changed`（tier 防泄漏硬约束的必然推论）与 `tag.*` 事件；
2. **缓存瘦身**：删片段级缓存与 content_fingerprint 键、删夜间全量预热、删 LRU 容量/配额/内存字节预设，收敛为页面级 map+TTL+singleflight；
3. **失效通道重设计**：删 `POST /internal/cache/purge`（api↔worker 间无任何 HTTP 先例、worker 不监听 HTTP），改为 worker 以 consumer key `delivery.cache` 写失效表 + api 2s 轮询；可见性 SLO 由 2s 调整为 ≤5s；
4. **权限修正**：`site_style_suggest`（实施名，见 §8.3）不用 admin-only 的 `site.manage`（agentAllowedActions 明确排除 site.\*），改为 ReadOnly + 新工具级 capability 串；
5. **DDL 修正**：site_releases 裸 UUID FK 改复合键、补 workspace_id；补 `published_release_id` 为 NULL 的存量站点引导路径（回退读行上工作配置，站点不因未发 Release 而消失）；
6. **引用更正**：`v2 §49` 为行号误标（实为 产品文档-v2.md 第 49 行，属 §2.3）；`Service.Home/...` 实为 `PublicReader`（public.go:61）；`publicVisitorPrincipal` 在 HTTP 层（public_sites.go:37）；`ForPublicSite` 在 scope_compiler.go:281；DeepSeek 端点为运行时配置非代码事实；
7. **新增红线**：HTML 面 CSP/安全响应头（§10.7）、复用 PublicThrottle 限流（§4.1/§10.8）；可观测性降级为结构化日志打点（仓库无 metrics 栈）；
8. **实施顺序相应更新**（M2/M4），rollback 独立 API 延后；应用层 siteColumns 列清单双侧同步写入 §12 注意事项。
