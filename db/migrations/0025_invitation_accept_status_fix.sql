-- 0025_invitation_accept_status_fix.sql
-- The organization-invitation accept transaction (internal/organization/
-- invitation_accept.go) wrote accepted_at/accepted_by but never flipped
-- status off 'pending': accepted invitations kept occupying the
-- member_invitations_pending_email_idx slot (blocking re-invites for the
-- same email) and the expiry sweep later mislabeled them 'expired'. Reconcile
-- every row that shows a real acceptance (accepted_at is only ever written by
-- the accept transaction) onto the accepted terminal state.

UPDATE organization.member_invitations
SET status = 'accepted', updated_at = now()
WHERE accepted_at IS NOT NULL AND status <> 'accepted';
