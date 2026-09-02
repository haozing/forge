package site

// presets.go — org-level custom style presets (二期 §5). A preset is a data
// bundle {style_config, custom_css}; applying it is COPY semantics resolved
// at write time: a style patch whose "preset" is a uuid expands to the
// preset's own document (patch keys still win), and the stored site document
// stays self-contained — rendering never needs preset lookups, and later
// preset edits cannot ripple into applied sites.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"agentchunzhi/internal/auth"

	"github.com/jackc/pgx/v5"
)

// StylePreset is one org-scoped saved look.
type StylePreset struct {
	ID          string          `json:"id"`
	Name        string          `json:"name"`
	StyleConfig json.RawMessage `json:"style_config"`
	CustomCss   string          `json:"custom_css"`
	CreatedBy   string          `json:"created_by"`
	CreatedAt   time.Time       `json:"created_at"`
	UpdatedAt   time.Time       `json:"updated_at"`
	// AppliedCount is advisory metadata for the management console.
	AppliedCount int `json:"applied_count"`
}

const stylePresetColumns = `p.id::text, p.name, p.style_config, p.custom_css, p.created_by::text, p.created_at, p.updated_at`

func scanStylePresetRow(row interface{ Scan(...any) error }) (StylePreset, error) {
	var item StylePreset
	if err := row.Scan(&item.ID, &item.Name, &item.StyleConfig, &item.CustomCss,
		&item.CreatedBy, &item.CreatedAt, &item.UpdatedAt); err != nil {
		return StylePreset{}, err
	}
	return item, nil
}

// ListStylePresets returns the org catalog (name order).
func (s Service) ListStylePresets(ctx context.Context, principal auth.Principal) ([]StylePreset, error) {
	if s.Store == nil || s.Store.Pool == nil {
		return nil, errors.New("database store is not initialized")
	}
	if principal.UserType != auth.UserTypeMember {
		return nil, ErrForbidden
	}
	rows, err := s.Store.Pool.Query(ctx, `
		SELECT `+stylePresetColumns+`
		FROM site.style_presets p
		WHERE p.organization_id = $1::uuid
		ORDER BY p.name
	`, principal.OrganizationID)
	if err != nil {
		return nil, fmt.Errorf("list style presets: %w", err)
	}
	defer rows.Close()
	items := []StylePreset{}
	for rows.Next() {
		item, err := scanStylePresetRow(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

// CreateStylePreset saves a look. The stored style_config must be a fully
// valid document anchored on a BUILTIN preset (uuid references inside a
// preset are rejected — no chaining); custom_css is sanitized here.
func (s Service) CreateStylePreset(ctx context.Context, principal auth.Principal, name string, styleConfig json.RawMessage, customCss string) (StylePreset, error) {
	if s.Store == nil || s.Store.Pool == nil {
		return StylePreset{}, errors.New("database store is not initialized")
	}
	if principal.UserType != auth.UserTypeMember {
		return StylePreset{}, ErrForbidden
	}
	name = strings.TrimSpace(name)
	if len(name) < 2 || len(name) > 32 {
		return StylePreset{}, ErrInvalidInput
	}
	if len(styleConfig) == 0 {
		styleConfig = json.RawMessage("{}")
	}
	config, err := ParseStyleConfig(styleConfig)
	if err != nil {
		return StylePreset{}, err
	}
	if !stylePresets[config.Preset] {
		// A preset anchored on a uuid reference cannot be resolved at render.
		return StylePreset{}, fmt.Errorf("%w: preset style_config must anchor on a builtin preset", ErrInvalidInput)
	}
	cleanCss, stripped := SanitizeCSS(customCss)
	if len(stripped) > 0 && strings.TrimSpace(cleanCss) == "" && strings.TrimSpace(customCss) != "" {
		return StylePreset{}, fmt.Errorf("%w: custom_css was entirely removed by the sanitizer", ErrInvalidInput)
	}
	item, err := scanStylePresetRow(s.Store.Pool.QueryRow(ctx, `
		INSERT INTO site.style_presets AS p (organization_id, name, style_config, custom_css, created_by)
		VALUES ($1::uuid, $2, $3::jsonb, $4, $5::uuid)
		ON CONFLICT (organization_id, name) DO UPDATE
			SET style_config = EXCLUDED.style_config, custom_css = EXCLUDED.custom_css, updated_at = now()
		RETURNING `+stylePresetColumns+`
	`, principal.OrganizationID, name, []byte(styleConfig), cleanCss, principal.UserID))
	if err != nil {
		return StylePreset{}, fmt.Errorf("save style preset: %w", err)
	}
	return item, nil
}

// DeleteStylePreset removes a saved look. Render-side falls back to the
// builtin default when a site still references it, so deletion is safe.
// The creator or an organization admin may delete (org admins are members
// whose role string the workspace policy already trusts elsewhere; here the
// membership table is the source of truth).
func (s Service) DeleteStylePreset(ctx context.Context, principal auth.Principal, presetID string) error {
	if s.Store == nil || s.Store.Pool == nil {
		return errors.New("database store is not initialized")
	}
	if principal.UserType != auth.UserTypeMember {
		return ErrForbidden
	}
	if !validID(presetID) {
		return ErrInvalidInput
	}
	tag, err := s.Store.Pool.Exec(ctx, `
		DELETE FROM site.style_presets p
		WHERE p.organization_id = $1::uuid AND p.id = $2::uuid
		  AND (
			p.created_by = $3::uuid
			OR EXISTS (
				SELECT 1 FROM identity.users u
				WHERE u.id = $3::uuid AND u.organization_id = $1::uuid
				  AND u.organization_role = 'admin' AND u.status = 'active'
			)
		  )
	`, principal.OrganizationID, presetID, principal.UserID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrSiteNotFound
	}
	return nil
}

// expandStylePreset resolves a patch whose "preset" leaf is a uuid into the
// preset's own document (patch keys win over the preset bundle — copy plus
// local tweaks). Builtin names and uuid misses pass through untouched; the
// regular validator rejects unknown names, and a dangling uuid degrades at
// render time by design.
// ExpandStylePreset resolves a patch whose "preset" leaf is a uuid into
// the preset document plus its paired custom_css (copy semantics applies
// both; public: the agent suggest tool shares the path). A patch without a
// uuid preset passes through untouched with an empty css.
func (s Service) ExpandStylePreset(ctx context.Context, organizationID string, patch json.RawMessage) (json.RawMessage, string, error) {
	var document map[string]any
	if err := json.Unmarshal(patch, &document); err != nil || document == nil {
		return patch, "", nil
	}
	reference, ok := document["preset"].(string)
	if !ok || !validID(strings.TrimSpace(reference)) {
		return patch, "", nil
	}
	var bundle []byte
	var presetCSS string
	err := s.Store.Pool.QueryRow(ctx, `
		SELECT style_config, custom_css FROM site.style_presets
		WHERE organization_id = $1::uuid AND id = $2::uuid
	`, organizationID, strings.TrimSpace(reference)).Scan(&bundle, &presetCSS)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, "", fmt.Errorf("%w: preset was not found in this organization", ErrInvalidInput)
	}
	if err != nil {
		return nil, "", fmt.Errorf("load style preset: %w", err)
	}
	// The bundle must anchor on a builtin preset (no chained references).
	var base map[string]any
	if err := json.Unmarshal(bundle, &base); err != nil {
		return nil, "", fmt.Errorf("%w: stored preset document is invalid", ErrInvalidInput)
	}
	if anchor, _ := base["preset"].(string); !stylePresets[anchor] {
		return nil, "", fmt.Errorf("%w: stored preset must anchor on a builtin preset", ErrInvalidInput)
	}
	// Patch keys win over the bundle — except "preset" itself: the resolved
	// document must carry the bundle's BUILTIN anchor, not the uuid (which
	// would fail the regular validator at merge time).
	delete(document, "preset")
	expanded := deepMergeStyle(base, document)
	overlay, err := json.Marshal(expanded)
	if err != nil {
		return nil, "", err
	}
	return overlay, presetCSS, nil
}
