package site

// slug.go — 标题 → slug 生成与发布时原子切换（站点方案 §5.2/§5.3，
// D3/D4/D7/D8/D18，2026-09-12）。
//
// 生成规则（D3/D4/D7/D8）：
//  1. trim + NFKC + 小写；
//  2. 纯 ASCII（英文/数字）保持原词：空格/标点转连字符，不转写不缩写；
//  3. 中文段转无声调拼音（段内连写、单段截 24），段间连字符；
//  4. 标题段截 48（时间后缀不占预算）；
//  5. 冲突（当前 + 全部历史）：-MMdd → 同日 -MMdd-HHmm → 序号 -2…；
//     本资产自己的历史 slug 直接回收，不加后缀；
//  6. 空结果回退 post-{短 id}。
//
// 切换时机（D18）：只在**发布事务内**执行——草稿阶段改标题不动线上 URL；
// 事务内完成「旧 slug 失效 → 新 slug 生效 → 写 old→new 301」，新 URL 上线
// 与旧 URL 跳转同时生效，零 404 窗口。

import (
	"context"
	"fmt"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/mozillazg/go-pinyin"
)

const (
	slugMaxTitleRunes = 48 // 标题部分上限；时间后缀不占预算
	slugMaxRunRunes   = 24 // 单个连续段（拼音/英文 run）上限
)

// GenerateSlug turns a title into a slug candidate. Pure function: no
// uniqueness resolution happens here.
func GenerateSlug(title string) string {
	normalized := normTitle(title)
	var segments []string
	var current strings.Builder
	runLen := 0
	flush := func() {
		if current.Len() > 0 {
			segments = append(segments, current.String())
			current.Reset()
		}
		runLen = 0
	}
	args := pinyin.NewArgs()
	args.Style = pinyin.Normal
	args.Heteronym = false
	for _, r := range normalized {
		switch {
		case (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9'):
			if runLen < slugMaxRunRunes {
				current.WriteRune(r)
				runLen++
			}
		case isHan(r):
			if runLen < slugMaxRunRunes {
				if syllables := pinyin.SinglePinyin(r, args); len(syllables) > 0 {
					current.WriteString(syllables[0])
					runLen += utf8.RuneCountInString(syllables[0])
				}
			}
		default:
			flush()
		}
	}
	flush()
	joined := strings.Join(segments, "-")
	if utf8.RuneCountInString(joined) > slugMaxTitleRunes {
		runes := []rune(joined)
		joined = string(runes[:slugMaxTitleRunes])
		// 优先按连字符边界截断，避免出现半个音节。
		if idx := strings.LastIndex(joined, "-"); idx > 0 {
			joined = joined[:idx]
		}
	}
	joined = strings.Trim(joined, "-")
	return joined
}

func normTitle(title string) string {
	return strings.TrimSpace(strings.ToLower(title))
}

func isHan(r rune) bool {
	return unicode.Is(unicode.Han, r)
}

// shortID 是 uuid 去连字符后的前 8 位，用于回退与哈希类后缀。
func shortID(id string) string {
	clean := strings.ReplaceAll(id, "-", "")
	if len(clean) > 8 {
		clean = clean[:8]
	}
	return clean
}

// EnsurePublishedSlugTx 在发布事务内保证资产持有与标题匹配的当前 slug，
// 并为换出的旧 slug 写入 301。工作区没有活跃站点时是 no-op。
func EnsurePublishedSlugTx(ctx context.Context, tx pgx.Tx, organizationID, workspaceID, assetID, versionID, actorUserID string) error {
	var siteID string
	err := tx.QueryRow(ctx, `
		SELECT id::text FROM site.public_sites
		WHERE organization_id = $1::uuid AND workspace_id = $2::uuid AND status = 'active'
	`, organizationID, workspaceID).Scan(&siteID)
	if err != nil {
		if err == pgx.ErrNoRows {
			return nil // 无站点：收录与 slug 不适用
		}
		return fmt.Errorf("resolve site for slug: %w", err)
	}
	var title string
	err = tx.QueryRow(ctx, `
		SELECT COALESCE(title, '') FROM asset.asset_versions
		WHERE organization_id = $1::uuid AND id = $2::uuid
	`, organizationID, versionID).Scan(&title)
	if err != nil {
		return fmt.Errorf("load published title for slug: %w", err)
	}
	base := GenerateSlug(title)
	if base == "" {
		base = "post-" + shortID(assetID)
	}
	if utf8.RuneCountInString(base) < 2 {
		base = base + "-" + shortID(assetID)
	}

	// 当前 slug 与幂等守卫：同基不动，防止每次发布换 URL。
	var currentSlug *string
	err = tx.QueryRow(ctx, `
		SELECT slug FROM site.site_slugs
		WHERE organization_id = $1::uuid AND site_id = $2::uuid
		  AND asset_id = $3::uuid AND is_current
	`, organizationID, siteID, assetID).Scan(&currentSlug)
	if err != nil && err != pgx.ErrNoRows {
		return fmt.Errorf("load current slug: %w", err)
	}
	if currentSlug != nil && *currentSlug == base {
		return nil
	}

	// 候选解析：fresh 直接取；本资产的历史 slug 回收；他资产占用则加时间后缀。
	now := time.Now()
	suffixDay := now.Format("0102")
	suffixMinute := now.Format("0102-1504")
	type candidate struct {
		slug    string
		recycle bool // 命中本资产历史行
		history bool // 命中历史行（含回收）
	}
	candidates := []candidate{{slug: base}}
	for _, s := range []string{base + "-" + suffixDay, base + "-" + suffixMinute} {
		candidates = append(candidates, candidate{slug: s})
	}
	for n := 2; n <= 50; n++ {
		candidates = append(candidates, candidate{slug: fmt.Sprintf("%s-%d", base, n)})
	}
	var chosen candidate
	var chosenRowID *string
	for _, c := range candidates {
		var rowID string
		var rowAsset string
		err := tx.QueryRow(ctx, `
			SELECT id::text, asset_id::text FROM site.site_slugs
			WHERE organization_id = $1::uuid AND site_id = $2::uuid AND slug = $3
		`, organizationID, siteID, c.slug).Scan(&rowID, &rowAsset)
		if err == pgx.ErrNoRows {
			chosen = c
			chosenRowID = nil
			break
		}
		if err != nil {
			return fmt.Errorf("probe slug candidate: %w", err)
		}
		if rowAsset == assetID {
			chosen = candidate{slug: c.slug, recycle: true, history: true}
			chosenRowID = &rowID
			break
		}
		// 他资产占用（当前或历史）：继续尝试下一候选。
	}
	if chosen.slug == "" {
		return fmt.Errorf("slug 候选耗尽：asset %s", assetID)
	}

	// 旧的当前行失效。
	var oldSlug string
	if currentSlug != nil {
		oldSlug = *currentSlug
		if _, err := tx.Exec(ctx, `
			UPDATE site.site_slugs SET is_current = false
			WHERE organization_id = $1::uuid AND site_id = $2::uuid
			  AND asset_id = $3::uuid AND is_current
		`, organizationID, siteID, assetID); err != nil {
			return fmt.Errorf("retire current slug: %w", err)
		}
	}

	if chosen.recycle && chosenRowID != nil {
		if _, err := tx.Exec(ctx, `
			UPDATE site.site_slugs SET is_current = true
			WHERE id = $1::uuid
		`, *chosenRowID); err != nil {
			return fmt.Errorf("recycle slug: %w", err)
		}
	} else {
		if _, err := tx.Exec(ctx, `
			INSERT INTO site.site_slugs (organization_id, site_id, asset_id, slug, is_current)
			VALUES ($1::uuid, $2::uuid, $3::uuid, $4, true)
		`, organizationID, siteID, assetID, chosen.slug); err != nil {
			return fmt.Errorf("insert slug: %w", err)
		}
	}

	// old→new 301（发布事务内与 URL 切换同时生效；历史冲突按最新指向覆盖）。
	if oldSlug != "" && oldSlug != chosen.slug {
		if _, err := tx.Exec(ctx, `
			INSERT INTO site.path_redirects
				(organization_id, site_id, from_path, to_path, created_by)
			VALUES ($1::uuid, $2::uuid, $3, $4, $5::uuid)
			ON CONFLICT (site_id, from_path) DO UPDATE
			SET to_path = EXCLUDED.to_path, updated_at = now()
		`, organizationID, siteID, oldSlug, chosen.slug, actorUserID); err != nil {
			return fmt.Errorf("record slug redirect: %w", err)
		}
	}
	return nil
}
