package store

import "context"

func (s *Store) RecordAttachmentDownload(ctx context.Context, organizationID, actorUserID, attachmentID, result string) error {
	if s == nil || s.Pool == nil {
		return nil
	}
	_, err := s.Pool.Exec(ctx, `
		INSERT INTO audit.audit_log
			(organization_id, actor_user_id, action, resource_type, resource_id, result, metadata)
		VALUES ($1::uuid, $2::uuid, 'attachment.download', 'attachment', $3::uuid, $4, '{"channel":"open_api"}'::jsonb)
	`, organizationID, actorUserID, attachmentID, result)
	return err
}
