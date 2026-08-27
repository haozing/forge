package store

import "context"

// RecordAttachmentDownload writes the attachment.download audit entry.
// The metadata carries the owning asset workspace_id so the record stays
// visible in per-workspace audit views (they filter on metadata->>'workspace_id').
func (s *Store) RecordAttachmentDownload(ctx context.Context, organizationID, actorUserID, attachmentID, result string) error {
	if s == nil || s.Pool == nil {
		return nil
	}
	_, err := s.Pool.Exec(ctx, `
		INSERT INTO audit.audit_log
			(organization_id, actor_user_id, action, resource_type, resource_id, result, metadata)
		VALUES ($1::uuid, $2::uuid, 'attachment.download', 'attachment', $3::uuid, $4,
			jsonb_build_object('channel', 'open_api',
				'workspace_id', COALESCE((
					SELECT a.workspace_id::text
					FROM asset.attachments at
					JOIN asset.asset_versions av ON av.id = at.asset_version_id
					JOIN asset.assets a ON a.id = av.asset_id
					WHERE at.organization_id = $1::uuid AND at.id = $3::uuid
				), '')))
	`, organizationID, actorUserID, attachmentID, result)
	return err
}
