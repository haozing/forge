package site

// pages_config.go — 页面配置 v2（站点方案 C1/D6/D10，2026-09-12）。
// 封闭目录 + 参数化模块：穷举的是模块类型，AI 与用户做"选类型 + 填参数"。
// 结构：{ version, home{content_width, blocks[]}, pages[](自定义页),
// collections{model_key: 参数}, nav{order/hidden/extra} }。
// 校验失败一律 422 并附可用类型（AI 自纠正闭环）；渲染侧未知类型跳过。

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/jackc/pgx/v5"
)

// PagesConfigVersion is the block-catalog/document version.
const PagesConfigVersion = 2

const (
	maxHomeBlocks     = 20
	maxCustomPages    = 20 // K4：含内置 about
	maxBlocksPerPages = 20
	maxTextBlockRunes = 5000
)

// 模块目录 v1（D6）：七型。类型即契约——新增类型 = 版本号递增。
const (
	BlockHero       = "hero"
	BlockLatest     = "latest"
	BlockRanked     = "ranked"
	BlockFeatured   = "featured"
	BlockCategories = "categories"
	BlockText       = "text"
	BlockLinks      = "links"
)

var blockTypes = map[string]bool{
	BlockHero: true, BlockLatest: true, BlockRanked: true, BlockFeatured: true,
	BlockCategories: true, BlockText: true, BlockLinks: true,
}

// 模块级样式旋钮（D10 第③层，封闭枚举映射类名，不携带自由 CSS）。
var (
	blockWidths      = map[string]bool{"contained": true, "full": true}
	blockVariants    = map[string]bool{"plain": true, "card": true, "emphasis": true}
	blockBackgrounds = map[string]bool{"default": true, "muted": true}
	contentWidths    = map[string]bool{"narrow": true, "normal": true, "full": true}
)

// Block is one parameterized module instance.
type Block struct {
	ID           string         `json:"id,omitempty"`
	Type         string         `json:"type"`
	Title        string         `json:"title,omitempty"`
	Subtitle     string         `json:"subtitle,omitempty"`
	ModelKey     string         `json:"model_key,omitempty"`
	SortField    string         `json:"sort_field,omitempty"`
	Order        string         `json:"order,omitempty"` // asc|desc
	Limit        int            `json:"limit,omitempty"`
	Layout       string         `json:"layout,omitempty"` // list|grid
	ImageAssetID string         `json:"image_asset_id,omitempty"`
	Href         string         `json:"href,omitempty"`
	Body         string         `json:"body,omitempty"`
	Links        []NavExtraLink `json:"links,omitempty"`
	Depth        int            `json:"depth,omitempty"`
	Columns      int            `json:"columns,omitempty"`
	Style        *BlockStyle    `json:"style,omitempty"`
}

// BlockStyle is the closed style vocabulary (D10 第③层)。
type BlockStyle struct {
	Width      string `json:"width,omitempty"`
	Variant    string `json:"variant,omitempty"`
	Columns    int    `json:"columns,omitempty"`
	Background string `json:"background,omitempty"`
}

// PageDef is one custom page (about 为内置保留页).
type PageDef struct {
	Slug    string  `json:"slug"`
	Title   string  `json:"title"`
	Builtin bool    `json:"builtin,omitempty"`
	Locale  string  `json:"locale,omitempty"`
	Blocks  []Block `json:"blocks"`
}

// CollectionConfig tunes one model's public listing page.
type CollectionConfig struct {
	SortField string `json:"sort_field,omitempty"`
	Order     string `json:"order,omitempty"`
	PageSize  int    `json:"page_size,omitempty"`
	ListStyle string `json:"list_style,omitempty"`
}

// NavExtraLink is one manually added external nav link.
type NavExtraLink struct {
	Label string `json:"label"`
	Href  string `json:"href"`
}

// NavConfig is the auto-enum + hide/order/extra nav model.
type NavConfig struct {
	Order  []string       `json:"order,omitempty"`
	Hidden []string       `json:"hidden,omitempty"`
	Extra  []NavExtraLink `json:"extra,omitempty"`
}

// PagesConfig is the v2 document.
type PagesConfig struct {
	Version           int                         `json:"version"`
	Home              *HomeConfig                 `json:"home,omitempty"`
	Pages             []PageDef                   `json:"pages,omitempty"`
	Collections       map[string]CollectionConfig `json:"collections,omitempty"`
	Nav               *NavConfig                  `json:"nav,omitempty"`
	FallbackToDefault bool                        `json:"fallback_to_default,omitempty"`
}

// HomeConfig is the homepage block list.
type HomeConfig struct {
	ContentWidth string  `json:"content_width,omitempty"`
	Blocks       []Block `json:"blocks"`
}

// DefaultPagesConfig builds the zero-config home (建站即完整站点)：
// hero → 最新内容 → categories。分类导航块待分类公开化数据后追加（K10）。
func DefaultPagesConfig() PagesConfig {
	return PagesConfig{
		Version: PagesConfigVersion,
		Home: &HomeConfig{
			ContentWidth: "normal",
			Blocks: []Block{
				{ID: "hero", Type: BlockHero, Title: "欢迎", Subtitle: ""},
				{ID: "latest", Type: BlockLatest, Limit: 6, Layout: "list"},
			},
		},
		Nav: &NavConfig{},
	}
}

// defaultPagesConfigOr returns the provided v2 document, or the generated
// zero-config default when absent/blank (K10/C1: 建站即完整站点).
func defaultPagesConfigOr(raw json.RawMessage) (PagesConfig, error) {
	if len(strings.TrimSpace(string(raw))) == 0 {
		return DefaultPagesConfig(), nil
	}
	var config PagesConfig
	if err := json.Unmarshal(raw, &config); err != nil {
		return PagesConfig{}, fmt.Errorf("pages_config 不是合法 JSON")
	}
	return config, nil
}

// ParsePagesConfig decodes the v2 document. Absent or malformed documents
// yield nil — callers fall back to the legacy homepage_config path.
func ParsePagesConfig(raw json.RawMessage) *PagesConfig {
	if len(strings.TrimSpace(string(raw))) == 0 {
		return nil
	}
	var config PagesConfig
	if err := json.Unmarshal(raw, &config); err != nil {
		return nil
	}
	if config.Version != PagesConfigVersion {
		return nil
	}
	return &config
}

// ValidatePagesConfig enforces the closed catalog: known block types, param
// ranges, style enums, custom-page slug reserved/pattern rules and ranked
// sort-field admission (public integer/number fields only). Runs inside the
// caller's transaction; unknown types answer 422 with the catalog.
func ValidatePagesConfig(ctx context.Context, tx pgx.Tx, organizationID, workspaceID string, config PagesConfig) error {
	// 归一：空文档/无 home 键的文档按空 home 处理，禁止后续 nil 解引用。
	if config.Home == nil {
		config.Home = &HomeConfig{}
	}
	if os.Getenv("DESIGN_DEBUG") == "1" {
		out, _ := json.MarshalIndent(config, "", " ")
		fmt.Println("DESIGN_DEBUG config:", string(out))
	}
	validateBlock := func(where string, block Block) error {
		if !blockTypes[block.Type] {
			available := make([]string, 0, len(blockTypes))
			for t := range blockTypes {
				available = append(available, t)
			}
			return fmt.Errorf("%w: %s: 未知模块类型 %q（可用：%s）", ErrInvalidInput, where, block.Type, strings.Join(available, ", "))
		}
		if block.Type == BlockRanked {
			if strings.TrimSpace(block.ModelKey) == "" || strings.TrimSpace(block.SortField) == "" {
				return fmt.Errorf("%w: %s: ranked 需要 model_key 与 sort_field", ErrInvalidInput, where)
			}
			if order := strings.ToLower(block.Order); order != "" && order != "asc" && order != "desc" {
				return fmt.Errorf("%w: %s: ranked order 仅支持 asc/desc", ErrInvalidInput, where)
			}
		}
		if block.Limit < 0 || block.Limit > 20 {
			return fmt.Errorf("%w: %s: limit 需在 0~20", ErrInvalidInput, where)
		}
		if block.Columns < 0 || block.Columns > 4 {
			return fmt.Errorf("%w: %s: columns 需在 0~4", ErrInvalidInput, where)
		}
		if block.Type == BlockText && len([]rune(block.Body)) > maxTextBlockRunes {
			return fmt.Errorf("%w: %s: text 正文需 ≤ %d 字", ErrInvalidInput, where, maxTextBlockRunes)
		}
		if block.Type == BlockLinks {
			if len(block.Links) > 20 {
				return fmt.Errorf("%w: %s: links 最多 20 条", ErrInvalidInput, where)
			}
			for _, link := range block.Links {
				if strings.TrimSpace(link.Label) == "" || strings.TrimSpace(link.Href) == "" {
					return fmt.Errorf("%w: %s: links 每条都需要 label 与 href", ErrInvalidInput, where)
				}
			}
		}
		if block.Style != nil {
			if block.Style.Width != "" && !blockWidths[block.Style.Width] {
				return fmt.Errorf("%w: %s: style.width 非法", ErrInvalidInput, where)
			}
			if block.Style.Variant != "" && !blockVariants[block.Style.Variant] {
				return fmt.Errorf("%w: %s: style.variant 非法", ErrInvalidInput, where)
			}
			if block.Style.Background != "" && !blockBackgrounds[block.Style.Background] {
				return fmt.Errorf("%w: %s: style.background 非法", ErrInvalidInput, where)
			}
		}
		return nil
	}

	if config.Home != nil {
		if config.Home.ContentWidth != "" && !contentWidths[config.Home.ContentWidth] {
			return fmt.Errorf("%w: home.content_width 非法", ErrInvalidInput)
		}
		if len(config.Home.Blocks) > maxHomeBlocks {
			return fmt.Errorf("%w: home blocks 最多 %d 个", ErrInvalidInput, maxHomeBlocks)
		}
		for i, block := range config.Home.Blocks {
			if err := validateBlock(fmt.Sprintf("home.blocks[%d]", i), block); err != nil {
				return err
			}
		}
	}
	if len(config.Pages) > maxCustomPages {
		return fmt.Errorf("自定义页最多 %d 个", maxCustomPages)
	}
	seenPage := map[string]bool{}
	for i, page := range config.Pages {
		slug := strings.ToLower(strings.TrimSpace(page.Slug))
		if !ValidDisplayPath(slug) || strings.Contains(slug, "/") {
			return fmt.Errorf("%w: pages[%d] slug 非法", ErrInvalidInput, i)
		}
		if ReservedPublicSlug(slug) {
			return fmt.Errorf("%w: pages[%d] slug %q 与保留路由或语言码冲突", ErrInvalidInput, i, slug)
		}
		if seenPage[slug] {
			return fmt.Errorf("%w: pages[%d] slug 重复", ErrInvalidInput, i)
		}
		seenPage[slug] = true
		if strings.TrimSpace(page.Title) == "" {
			return fmt.Errorf("%w: pages[%d] 缺少标题", ErrInvalidInput, i)
		}
		for j, block := range page.Blocks {
			if err := validateBlock(fmt.Sprintf("pages[%d].blocks[%d]", i, j), block); err != nil {
				return err
			}
		}
	}
	// ranked 排序字段准入：限该模型已公开的 integer/number 字段（防侧信道）。
	for _, block := range config.Home.Blocks {
		if block.Type == BlockRanked {
			if err := validateRankedSortField(ctx, tx, organizationID, workspaceID, block.ModelKey, block.SortField); err != nil {
				return err
			}
		}
	}
	return nil
}

func validateRankedSortField(ctx context.Context, tx pgx.Tx, organizationID, workspaceID, modelKey, sortField string) error {
	var count int
	// nil tx（纯解析路径/测试）无法做模型准入查询，按校验失败处理而非 panic。
	if tx == nil {
		return fmt.Errorf("%w: ranked.sort_field 需要数据库校验", ErrInvalidInput)
	}
	err := tx.QueryRow(ctx, `
		SELECT count(*)
		FROM model.resource_models rm
		JOIN model.resource_model_versions mv ON mv.id = rm.current_version_id
		CROSS JOIN LATERAL jsonb_array_elements(mv.field_schema->'fields') AS f(def)
		WHERE rm.organization_id = $1::uuid
		  AND (rm.workspace_id = NULLIF($2, '')::uuid OR rm.workspace_id IS NULL)
		  AND rm.status = 'active' AND rm.model_key = $3
		  AND f.def->>'key' = $4
		  AND f.def->>'type' IN ('integer', 'number')
	`, organizationID, workspaceID, modelKey, sortField).Scan(&count)
	if err != nil {
		return fmt.Errorf("%w: 校验排序字段失败: %w", ErrInvalidInput, err)
	}
	if count == 0 {
		return fmt.Errorf("%w: ranked.sort_field %q 不是该模型已公开的数值字段", ErrInvalidInput, sortField)
	}
	return nil
}
