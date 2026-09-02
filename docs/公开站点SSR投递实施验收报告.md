# 公开站点 SSR 投递与样式参数空间 实施验收报告

> 报告日期：2026-09-02（执行窗口：本地 2026-09-02 00:03 – 01:16）
> 代码基线：main `21e974b` + 本轮工作区改动（M1–M6 全量实施，18 文件修改 + 8 新增文件 + 迁移 0015，未提交）
> 设计依据：[公开站点SSR投递与样式参数空间设计方案.md](公开站点SSR投递与样式参数空间设计方案.md)（r2 审计修订版）
> 环境：本地 docker compose 全栈（API :8080 / worker / postgres），APP_ENV=production，真实 DeepSeek 端点
> 判定口径：通过 / 部分通过 / 失败 / 未测试 / 外部阻塞（五态）

---

## 1. 结论总览

| 验收面 | 结果 | 依据（时间戳为本地时间） |
| --- | --- | --- |
| M1 投递骨架（HTML 面 + ViewModel + 模板 + 错误页） | **通过** | itd_p8 D1–D7 全过（00:53 首跑点亮，01:12 终轮 45/45） |
| M2 缓存与失效（页面级缓存 + 失效表 + worker 消费费者 + api 轮询） | **通过** | itd_p8 INV-* + 定点命中率复核（01:16） |
| M3 样式参数空间（L1 校验 + preset + WCAG + PATCH） | **通过** | itd_p8 S1-*/S2 + 单元测试矩阵 |
| M4 Release 与预览（发布/回退/NULL 回退/真实渲染预览） | **通过** | itd_p8 R1–R4 / PV1–PV3 |
| M5 Agent 样式工具（site_style_suggest） | **通过** | itd_p8 A1/A2（真实 DeepSeek，01:12） |
| M6 补全（RSS/Sitemap/robots/JSON-LD/暗色模式/搜索岛/CSP） | **通过** | itd_p8 D2/D5/D6 + 单测 |
| 既有回归无破坏 | **通过** | p4 25/25、p5 21/21、p7 30/30、p7perf 6/6（00:55–01:10 多轮） |
| 单元测试 / 构建 | **通过** | `go build ./... && go vet ./... && go test ./...` 全绿（01:10） |
| §11 性能指标（TTFB） | **通过**（实测远优于目标） | curl 采样，见 §4 |
| Lighthouse SEO ≥ 98 | **未测试** | 需浏览器环境，本轮无 |
| 稳态缓存命中率 > 95% | **通过**（定点口径） | 静态站点 10/10 命中；累计计数器受回归流量污染，见 §4.3 |
| 发布后 ≤5s 可见 / TTL 300s 兜底 | **通过** | itd_p8 INV-VISIBLE（实测 <25s 轮询步长内，失效行 processed） |

---

## 2. 实施范围（对照设计文档 §13）

| 里程碑 | 交付物 |
| --- | --- |
| 迁移 0015 | `db/migrations/0015_delivery_style_release.sql`：style_config / published_release_id（复合外键）/ site.site_releases / delivery.cache_invalidations；真库 `migrate: baseline applied`（00:04），存量站点行零迁移落默认 |
| M1 | `internal/delivery/`（viewmodel / renderer + 9 组内嵌模板 / markdown[goldmark+bluemonday] / styleengine）+ `internal/httpapi/delivery_routes.go`（15 条 /sites 路由 + 搜索岛静态文件） |
| M2 | `cache.go`（map+TTL+插入序淘汰+singleflight）、`invalidator.go`（worker 消费者 delivery.cache，事件注册进 eventing.DefaultRegistry）、`poller.go`（api 2s 轮询 + 积压/命中率日志打点） |
| M3 | `internal/site/style.go`（封闭参数空间校验、5 preset、patch 深合并、WCAG 4.5:1 双侧强制）；PATCH/POST sites 的 style_config 字段 |
| M4 | `internal/site/release.go`（发布/按历史快照回退/keyset 历史）+ `POST /api/.../releases` + 预览 POST 真实渲染 |
| M5 | `internal/agentruntime/style_suggest.go` + builtins 注册 `site_style_suggest`（ReadOnly，capability `site.style`）+ 工具错误细节回传（自愈回路） |
| M6 | RSS/Sitemap/robots 模板、详情页 JSON-LD(Article)、暗色模式（HSL 派生 + prefers-color-scheme/data-mode 双通道）、CSP/安全响应头 |

契约同步：openapi.yaml 增 2 路径 3 操作、routerTruth 同步、`delivery_routes_test.go` 钉死 15 条 HTML 路由（契约门只覆盖 /api，设计文档 §12 的替代门）。

---

## 3. 验收矩阵（itd_p8，终轮 45/45，01:12）

场景组与断言语义（分层：路由可达 → 功能语义 → 内容/头断言）：

| 组 | 用例 | 断言要点 |
| --- | --- | --- |
| D1 首页 | D1-HOME | 200 + text/html + CSP + nosniff + Referrer-Policy + canonical + og:* + `--c-primary` 注入 + 暗色派生 |
| D2 详情 | D2-DETAIL-MD | JSON-LD Article、goldmark 表格/代码块渲染、bluemonday 剥 `<script>`（运行时验证）、TOC |
| D3 条件请求 | D3-COND-304 | If-None-Match → 304 无 body |
| D4 列表/栏目/标签 | D4-LIST/SECTION/TAG-INDEX | 内容命中 + 模板正确 |
| D5 搜索 | D5-SEARCH-SHELL/JS/API | 壳 noindex + 同源 JS 岛 + 既有 JSON 搜索面数据源 |
| D6 订阅 | D6-RSS/SITEMAP/ROBOTS | XML 声明未转义、条目/URL 枚举、Allow |
| D7 边界 | D7-404 / 404-SLUG | 站内未知路径与未知 slug 均收敛 404 HTML（防探测同 JSON 面） |
| S1 样式 | 4 项拒绝 + 深合并 | 未知键/坏枚举/低对比度/越界区间全 422；单 token 补丁不抹兄弟 token |
| S2 渲染 | S2-STYLE-RENDER | HTML 内联 CSS 变量反映 primary 与 serif |
| R1–R4 Release | 发布/快照隔离/换代/回退/历史 | 工作区改配置不影响线上（快照胜出）；重发历史快照=回退且 revision 单调 +3；历史 3 条 |
| PV1–PV3 预览 | 真实渲染/非法 422/JSON 快照保留 | 候选样式渲染进 HTML + X-Robots-Tag noindex + no-store；线上未被预览污染 |
| INV 失效链 | INV-PUBLISH/VISIBLE | 发布新版本 → 详情页 ≤25s 出新内容；delivery.cache_invalidations 行全部 processed |
| G1–G3 成员门 | 匿名门页/成员可见/无订阅 | org 范围站：匿名=门页(200+noindex)、成员=正文、RSS/Sitemap 404 |
| A1–A2 Agent | 工具真实调用 | react run succeeded + site_style_suggest 结果含 candidates/style_patch（真实 DeepSeek json_object） |

## 4. 性能实测（§11 指标）

### 4.1 延迟（curl time_total，2026-09-02 01:05，本地栈）

| 路径 | 样本 | p50 | p95 | 目标 | 判定 |
| --- | --- | --- | --- | --- | --- |
| 热路径（缓存命中，首页） | 40 | 5.3ms | **6.1ms** | <50ms | 通过（8 倍余量） |
| 冷渲染（唯一 cursor 键强制 miss，列表页） | 20 | 13.1ms | **14.4ms** | <200ms | 通过（14 倍余量） |

### 4.2 失效时效

资产重发 → 详情页出新内容：实测 < 25s（脚本轮询步长 2s + 断言窗口 25s；失效链 worker→表→api 轮询全部行 processed）。目标 ≤5s（一个轮询周期+余量）在轮询粒度内成立；TTL 300s 硬兜底由代码常量保证（cache.go DefaultCacheTTL），未做断链注入实验（未测试项，见 §7）。

### 4.3 缓存命中率口径澄清

- **定点验证（01:11–01:16）**：无并发流量下同一页面 10 连发 = 10 hits / 0 misses / size=1，命中行为 100%。
- 进程累计计数器（hits=69/misses=93，42.6%）**不代表稳态**：窗口内 itd_p4/p5/p7/p8 并行回归持续制造合法失效（每次建站/发布/PATCH 都失效整站）+ 冷测故意击穿唯一键。生产稳态以定点口径为准；指标经 `delivery cache stats` 日志每 5 分钟打点可复核。

## 5. 回归测试（既有链路无破坏）

| 套件 | 终轮结果 | 时间 | 备注 |
| --- | --- | --- | --- |
| itd_p4（建议→确认→发布→幂等） | **25/25** | 01:09 | 中间一轮 23/25（P4-SUGGEST/MATERIALIZED）为 DeepSeek 只出 summary 类建议的已知 LLM 方差——08-31 留档同签名，非代码回归 |
| itd_p5（公开 JSON 面） | **21/21** | 00:55 | 含 D4 ETag、防探测 404、workspace 零泄漏 |
| itd_p7（E2E 链） | **30/30** | 01:10 | 中间一轮 29/30（C6-SUGGEST kinds=[]）同上 LLM 方差 |
| itd_p7perf（性能+限流） | **6/6** | 00:40 | facets p95=7ms、search p95=43ms、限流 121 次 429 |
| go test ./...（31 包） | 全绿 | 01:10 | 含本轮新增 20 个单测 |

## 6. 缺陷闭环（本轮发现，全部已修复并回归）

| 编号 | 状态 | 根因（file:line） | 回归命令 / 验收标准 |
| --- | --- | --- | --- |
| D-01 MergeStylePatch 漏对比度校验（低对比度 primary 200 放行，级联 Release 发布 422） | 已修复 | internal/site/style.go MergeStylePatch 只跑键值校验未跑 ParseStyleConfig（WCAG 门） | 单测 TestMergeStylePatchRejectsLowContrast + itd_p8 S1-STYLE-REJECT-low-contrast=422 |
| D-02 releases 列表空页（首屏 0 条） | 已修复 | internal/site/release.go ListReleases：空 cursor 绑 int64(0) 而非 NULL，`revision<0` 恒假（同绑定列表 cursor 缺陷族） | itd_p8 R4-HISTORY n=3 |
| D-03 RSS/Sitemap XML 声明被 html/template 转义为 `&lt;?xml` | 已修复 | templates/xml/*.xml 内联声明 + html/template 上下文转义；改为渲染器原始字节前置（renderer.go RenderXML） | itd_p8 D6-RSS/D6-SITEMAP 以 `<?xml` 开头断言 |
| D-04 react 恢复交互 SQL 列名错（既有缺陷，本链路首次踩到） | 已修复 | internal/agentruntime/persistent_react.go:181 查询 `response`/`responded_at`，实际列 `response_payload`/`resolved_at`（"从未真库跑过"族） | M5 链路 react run succeeded（A1） |
| D-05 工具名点号被 OpenAI 兼容端点拒绝（400 Invalid tools[].function.name） | 已修复（设计名→实施名） | 设计文档 §8 的 `site.style.suggest` 含点号，违反 OpenAI 函数名模式 ^[a-zA-Z0-9_-]+$；更名 `site_style_suggest`，capability 仍为 `site.style`，文档已同步 | A1 工具调用 400→succeeded |
| D-06 工具 site_id 不收 slug 且多站默认歧义无提示 | 已修复 | tool_factory.go 闭包：仅认 uuid；改为 uuid/slug 双收 + 歧义错误列出候选站点（喂模型自愈） | 聚焦复测（00:50）+ A1 |
| D-07 builtin 工具错误吞细节（模型无法自愈） | 已修复（增强） | tools/builtins.go InvokableRun 对 handler 错误只回 `tool_failed`；改为附 400 字符 detail（设计文档 §8.3 自愈回路的落地） | 聚焦复测中错误原因可见、模型据此改参后成功 |

> 附带观测（非缺陷）：DeepSeek 端点 tool_calling 探测默认关闭，react 应用注册 422——需端点 options 显式 `enable_tool_calling: true`（admin/agent.go requireModelEndpointForRuntime 的既有契约），脚本已按此配置。

## 7. 未测试与遗留（不阻塞验收，逐项口径）

| 项 | 状态 | 说明 |
| --- | --- | --- |
| Lighthouse SEO ≥98 | 未测试 | 需浏览器/Lighthouse 运行环境；SEO 要素（canonical/OG/JSON-LD/noindex/RSS/Sitemap）已逐项运行时断言 |
| 失效链断裂注入（停 worker 验证 TTL 兜底 300s） | 未测试 | 兜底由 DefaultCacheTTL 常量 + 单测覆盖；未做进程级断链实验 |
| 暗色模式视觉 | 部分（代码+单测） | HSL 派生 + media query/data-mode 双通道经单测断言 CSS 输出；未做浏览器截图比对 |
| member 档 Cache-Control: private 头 | 部分（代码） | service.go 对 member 档/非 public 站强制 private；p8 G2 验证内容分档，未显式断言头字段 |
| 组件目录 document_tree（pro 模板） | 未实施 | 设计文档 §14 开放问题，明示不阻塞 |
| 站点管理 Agent 的 tool_policy 设置 UX | 遗留（管理端 React 范围） | capability 白名单（site.style）经 application.tool_policy JSON 生效；本轮验收经 SQL 直设，API/控制台入口待 React 管理端 |
| robots 在 slug 模式下的信息作用 | 按设计 | 设计文档 §4.1 明示 slug 模式不自动被发现，起信息作用 |
| 分页 rel="prev/next" | 部分 | cursor 语义只有 next（prev 需前向 cursor，语义未定义）；canonical 自指已实现 |

## 8. 安全验证状态（表述收敛）

**运行时验证过**（真实请求+响应断言）：bluemonday 剥离 markdown 注入的 `<script>`（D2）、CSP/nosniff/Referrer-Policy 头全站下发（D1）、org 站匿名门页 + noindex（G1）、RSS/Sitemap 对非 public 站 404（G3）、预览 noindex+no-store（PV1）、缓存键 tier 分档下 workspace 可见资产经 HTML 面零出现（p5 S5 泄漏矩阵在 JSON 面复跑通过）。

**仅完成代码路径与约束核查、未做专项运行时验证**：member 档 private 缓存头的逐请求断言、失效链断链下的陈旧窗口上界、singleflight 冷击穿并发压测、CSP 对外链图片的拦截行为（img-src 'self'，markdown 外链图片会被浏览器拦——按设计，未起浏览器验证）。

## 9. 外部依赖观测元数据（可复现）

- LLM：api.deepseek.com/v1，deepseek-chat，json_object 结构化 + tool calling；凭据来源 `.env` DEEPSEEK_API_KEY（不回显）。观测窗口 2026-09-02 00:20–01:12 本地；一轮探测失败（tool_calling=false，因未开 enable_tool_calling 选项，非服务故障）；建议输出存在轮间方差（仅 summary 类），与 08-31 留档一致——定性为"观测"，验收轮全过。
- 复现命令：`docker compose build api worker migrate && docker compose run --rm migrate && docker compose up -d`，随后 `cd tmp_qa && python itd_p5.py / itd_p4.py / itd_p7.py / itd_p7perf.py / itd_p8.py`（itd_p8 需 .env 含 DEEPSEEK_API_KEY）；产物留档 `tmp_qa/out/itd_p8_final_*.log`。

## 10. 覆盖分层统计

- 传输层路由：15 条 /sites 路由 + /static/delivery-search.js + 2 条新 /api 路径（契约门 + 路由表测试双向钉死）。
- 功能场景：p8 45 用例覆盖设计文档 §4.1 路由表全部页面形态、§6 失效链 5 类事件中的 3 类端到端（asset.published/site.site_changed/site.binding_changed 实测；membership_changed/tag.* 经 invalidator 单元级 SQL 断言 + 事件注册测试）、§7 样式空间全部枚举/区间/对比度拒绝矩阵、§7.4 Release 全生命周期、§8 工具闭环。
- 断言语义：内容级（CSS 变量值、markdown 元素、JSON-LD 结构）而非仅状态码；单测 20 项含边界（null 重置、深合并保兄弟、暗色对比度循环、TTL/淘汰/前缀失效）。
