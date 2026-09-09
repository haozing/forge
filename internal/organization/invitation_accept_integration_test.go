package organization

import (
	"context"
	"errors"
	"os"
	"testing"

	"agentchunzhi/internal/store"

	"github.com/google/uuid"
)

// TestAcceptMarksInvitationAcceptedIntegration pins the invitation terminal
// state: acceptance must flip status to 'accepted' (not just stamp
// accepted_at), or the pending-email unique index keeps blocking re-invites
// and the expiry sweep mislabels accepted rows as 'expired'.
func TestAcceptMarksInvitationAcceptedIntegration(t *testing.T) {
	databaseURL := os.Getenv("AGENTCHUNZHI_TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("AGENTCHUNZHI_TEST_DATABASE_URL is not set")
	}
	ctx := context.Background()
	db, err := store.Open(ctx, databaseURL)
	if err != nil {
		t.Fatalf("open integration database: %v", err)
	}
	t.Cleanup(db.Close)

	var organizationID, inviterID string
	if err := db.Pool.QueryRow(ctx, `
		INSERT INTO organization.organizations (name, slug)
		VALUES ('ITC-Accept-' || gen_random_uuid()::text, 'itc-accept-' || gen_random_uuid()::text)
		RETURNING id::text
	`).Scan(&organizationID); err != nil {
		t.Fatalf("seed organization: %v", err)
	}
	if err := db.Pool.QueryRow(ctx, `
		INSERT INTO identity.users (organization_id, user_type, email, password_hash, display_name, status)
		VALUES ($1::uuid, 'member', 'itc-inviter-' || gen_random_uuid()::text || '@itc.invalid', 'x', 'ITC Accept Inviter', 'active')
		RETURNING id::text
	`, organizationID).Scan(&inviterID); err != nil {
		t.Fatalf("seed inviter: %v", err)
	}
	token := "itc-accept-token-" + uuid.NewString()
	var invitationID string
	if err := db.Pool.QueryRow(ctx, `
		INSERT INTO organization.member_invitations
			(organization_id, email, display_name, organization_role, authority_scope, token_hash, status, expires_at, invited_by)
		VALUES ($1::uuid, $2, 'ITC Acceptee', 'member', 'organization', $3, 'pending', now() + interval '1 day', $4::uuid)
		RETURNING id::text
	`, organizationID, "itc-acceptee-"+uuid.NewString()+"@itc.invalid", hashToken(token), inviterID).Scan(&invitationID); err != nil {
		t.Fatalf("seed invitation: %v", err)
	}

	t.Cleanup(func() {
		cleanupAcceptRow(t, db, `DELETE FROM audit.audit_log WHERE organization_id = $1::uuid`, organizationID)
		cleanupAcceptRow(t, db, `DELETE FROM organization.member_invitations WHERE organization_id = $1::uuid`, organizationID)
		cleanupAcceptRow(t, db, `DELETE FROM identity.users WHERE organization_id = $1::uuid`, organizationID)
		cleanupAcceptRow(t, db, `DELETE FROM organization.organizations WHERE id = $1::uuid`, organizationID)
	})

	service := InvitationService{Store: db}
	result, err := service.Accept(ctx, AcceptInput{Token: token, DisplayName: "ITC Acceptee", Password: "accept-1234-5678"})
	if err != nil {
		t.Fatalf("accept invitation: %v", err)
	}
	if result.UserID == "" || result.OrganizationID != organizationID {
		t.Fatalf("unexpected accept result: %+v", result)
	}

	var status string
	var accepted bool
	if err := db.Pool.QueryRow(ctx, `
		SELECT status, accepted_at IS NOT NULL FROM organization.member_invitations WHERE id = $1::uuid
	`, invitationID).Scan(&status, &accepted); err != nil {
		t.Fatalf("reload invitation: %v", err)
	}
	if status != "accepted" || !accepted {
		t.Fatalf("accepted invitation must be status='accepted' with accepted_at, got status=%q accepted=%v", status, accepted)
	}

	var memberCount int
	if err := db.Pool.QueryRow(ctx, `
		SELECT count(*) FROM identity.users WHERE organization_id = $1::uuid AND user_type = 'member' AND status = 'active'
	`, organizationID).Scan(&memberCount); err != nil || memberCount != 2 {
		t.Fatalf("expected inviter + accepted member, got %d members (err=%v)", memberCount, err)
	}

	if _, err := service.Accept(ctx, AcceptInput{Token: token, DisplayName: "ITC Acceptee Again", Password: "accept-1234-5678"}); !errors.Is(err, ErrInvitationInvalid) {
		t.Fatalf("re-accepting a consumed token must fail invalid, got %v", err)
	}
}

func cleanupAcceptRow(t *testing.T, db *store.Store, sql string, arguments ...any) {
	t.Helper()
	if _, err := db.Pool.Exec(context.Background(), sql, arguments...); err != nil {
		t.Errorf("clean accept integration data: %v", err)
	}
}
