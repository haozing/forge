package delivery

// invalidator.go — the worker-side cache invalidation orchestrator (design
// doc §6.2): the delivery.cache event consumer computes the affected site
// set of one domain fact and appends delivery.cache_invalidations rows. The
// api process polls and applies them; the append is idempotent and River
// retries cover worker failures.

import (
	"context"
	"encoding/json"
	"fmt"
	"log"

	"agentchunzhi/internal/eventing"
	"agentchunzhi/internal/store"
)

// Invalidator is the worker-side delivery.cache consumer.
type Invalidator struct {
	Store *store.Store
	Logf  func(string, ...any)
}

// Process maps one domain fact onto cache invalidation rows.
func (i *Invalidator) Process(ctx context.Context, organizationID, eventType string, payload json.RawMessage) error {
	if i.Store == nil || i.Store.Pool == nil {
		return fmt.Errorf("delivery invalidator store is not initialized")
	}
	switch eventType {
	case eventing.EventAssetPublished, eventing.EventAssetArchived, eventing.EventAssetVisibilityChanged:
		assetID, err := payloadString(payload, "asset_id")
		if err != nil {
			return err
		}
		// 1:1 站点（D1）：站点由资产的工作区唯一定位——下线/排除后资产会从
		// 派生收录视图消失，若按"当前收录"反查会漏失效（缓存滞留旧内容）。
		tag, err := i.Store.Pool.Exec(ctx, `
			INSERT INTO delivery.cache_invalidations (organization_id, site_id)
			SELECT $1::uuid, s.id
			FROM site.public_sites s
			WHERE s.organization_id = $1::uuid
			  AND s.workspace_id = (SELECT workspace_id FROM asset.assets WHERE id = $2::uuid)
			  AND s.status = 'active'
		`, organizationID, assetID)
		if err != nil {
			return logInvalidation(i.Logf, eventType, tag, err)
		}
		// C6 反向传播：引用了该资产的页面随发布态变化一并失效。
		refTag, err := i.Store.Pool.Exec(ctx, `
			INSERT INTO delivery.cache_invalidations (organization_id, site_id)
			SELECT DISTINCT $1::uuid, sl.site_id
			FROM asset.assets target
			JOIN asset.asset_versions tv
			  ON tv.organization_id = target.organization_id AND tv.id = target.current_published_version_id
			JOIN asset.asset_versions v
			  ON v.organization_id = target.organization_id
			 AND v.markdown LIKE '%chunzhi-asset://' || target.id::text || '%'
			JOIN asset.assets src
			  ON src.organization_id = v.organization_id AND src.id = v.asset_id
			 AND src.deleted_at IS NULL AND src.current_published_version_id IS NOT NULL
			JOIN site.site_slugs sl
			  ON sl.organization_id = src.organization_id AND sl.asset_id = src.id
			 AND sl.site_id IN (SELECT id FROM site.public_sites
			                    WHERE organization_id = $1::uuid
			                      AND workspace_id = src.workspace_id AND status = 'active')
			WHERE target.id = $2::uuid
		`, organizationID, assetID)
		if err != nil {
			return logInvalidation(i.Logf, eventType+" (references)", refTag, err)
		}
		return logInvalidation(i.Logf, eventType, tag, nil)
	case eventing.EventSiteChanged, eventing.EventSiteBindingChanged, eventing.EventSiteCommentCreated:
		siteID, err := payloadString(payload, "site_id")
		if err != nil {
			return err
		}
		// site.site_changed normally rotates the release/revision key on its
		// own; the row is the explicit confirmation (design doc §6.2).
		tag, err := i.Store.Pool.Exec(ctx, `
			INSERT INTO delivery.cache_invalidations (organization_id, site_id)
			VALUES ($1::uuid, $2::uuid)
		`, organizationID, siteID)
		return logInvalidation(i.Logf, eventType, tag, err)
	case eventing.EventWorkspaceMembershipChanged:
		workspaceID, err := payloadString(payload, "workspace_id")
		if err != nil {
			return err
		}
		// Membership changes only affect the member band of gated sites —
		// the tier cache key is the leak-prevention hard constraint (§6.1).
		tag, err := i.Store.Pool.Exec(ctx, `
			INSERT INTO delivery.cache_invalidations (organization_id, site_id, tier)
			SELECT $1::uuid, s.id, 'member'
			FROM site.public_sites s
			WHERE s.organization_id = $1::uuid AND s.workspace_id = $2::uuid
			  AND s.default_content_scope <> 'public'
		`, organizationID, workspaceID)
		return logInvalidation(i.Logf, eventType, tag, err)
	case eventing.EventTagUpdated, eventing.EventTagArchived, eventing.EventTagRestored:
		workspaceID, err := payloadString(payload, "workspace_id")
		if err != nil {
			return err
		}
		tag, err := i.Store.Pool.Exec(ctx, `
			INSERT INTO delivery.cache_invalidations (organization_id, site_id)
			SELECT $1::uuid, s.id
			FROM site.public_sites s
			WHERE s.organization_id = $1::uuid AND s.workspace_id = $2::uuid
		`, organizationID, workspaceID)
		return logInvalidation(i.Logf, eventType, tag, err)
	default:
		// Unknown facts are not an error: the consumer contract is closed at
		// the registry level, anything else here is a no-op.
		return nil
	}
}

// payloadString extracts one identifier leaf of an event payload.
func payloadString(payload json.RawMessage, key string) (string, error) {
	var document map[string]any
	if err := json.Unmarshal(payload, &document); err != nil {
		return "", fmt.Errorf("delivery invalidator payload is invalid: %w", err)
	}
	value, _ := document[key].(string)
	if value == "" {
		return "", fmt.Errorf("delivery invalidator payload is missing %s", key)
	}
	return value, nil
}

func logInvalidation(logf func(string, ...any), eventType string, command interface{ RowsAffected() int64 }, err error) error {
	if err != nil {
		return err
	}
	if logf != nil && command.RowsAffected() > 0 {
		logf("delivery cache invalidation event=%s sites=%d", eventType, command.RowsAffected())
	}
	return nil
}

// NewInvalidator wires the default invalidator.
func NewInvalidator(database *store.Store) *Invalidator {
	return &Invalidator{Store: database, Logf: log.Printf}
}
