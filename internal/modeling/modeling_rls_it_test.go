// modeling_rls_it_test.go — RLS 试点（0045）的强制力冒烟：
// FORGE_MODELING_IT=1 且本地 postgres（25432）可达时执行。
// 断言三件事：
//  1. 受限角色（agentchunzhi_app，非表 owner）不设 GUC 看不见任何行；
//  2. set_config('app.organization_id', <org>) 后只看见本组织的行；
//  3. 服务层 Save→List→Patch 全链在 GUC 事务内工作。
package modeling

import (
	"context"
	"os"
	"testing"

	"agentchunzhi/internal/auth"
	"agentchunzhi/internal/authz"
	"agentchunzhi/internal/store"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const itDSNDefault = "postgresql://agentchunzhi:dev-only-change-me@localhost:25432/agentchunzhi?sslmode=disable"

func TestModelingRLSIsolation(t *testing.T) {
	if os.Getenv("FORGE_MODELING_IT") != "1" {
		t.Skip("FORGE_MODELING_IT=1 required")
	}
	dsn := os.Getenv("MODELING_IT_DSN")
	if dsn == "" {
		dsn = itDSNDefault
	}
	pool, err := pgxpool.New(context.Background(), dsn)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer pool.Close()

	orgA := "11111111-0000-4000-8000-000000000001"
	orgB := "22222222-0000-4000-8000-000000000002"
	wsA := "33333333-0000-4000-8000-000000000001"
	wsB := "33333333-0000-4000-8000-000000000002"
	creator := "aaaa0001-0000-4000-8000-000000000001" // 本地 admin@t.dev

	// 种子：第二个组织/工作区（本地库只有 orgA）。
	if _, err := pool.Exec(context.Background(), `
		INSERT INTO organization.organizations (id, slug, name)
		VALUES ($1::uuid, 'rls-it-b', 'RLS IT B')
		ON CONFLICT (id) DO NOTHING
	`, orgB); err != nil {
		t.Fatalf("seed orgB: %v", err)
	}
	if _, err := pool.Exec(context.Background(), `
		INSERT INTO content.workspaces (id, organization_id, slug, name, created_by)
		VALUES ($1::uuid, $2::uuid, 'rls-it-b', 'RLS IT B', $3::uuid)
		ON CONFLICT (id) DO NOTHING
	`, wsB, orgB, creator); err != nil {
		t.Fatalf("seed wsB: %v", err)
	}

	// 两个组织的行由 owner 直插（绕过 RLS，模拟既有数据）。
	for _, pair := range [][2]string{{orgA, wsA}, {orgB, wsB}} {
		if _, err := pool.Exec(context.Background(), `
			INSERT INTO content.modeling_plans
				(organization_id, workspace_id, intent, plan, status, created_by)
			VALUES ($1::uuid, $2::uuid, 'rls-it', '[{"source_asset_id":"aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa","enabled":true}]'::jsonb, 'draft', $3::uuid)
		`, pair[0], pair[1], creator); err != nil {
			t.Fatalf("seed: %v", err)
		}
	}

	// 受限角色（非 owner，受 RLS 管辖）的单事务探针：
	// 无 GUC → 0 行；设 orgA GUC → 可见行全部属于 orgA。
	err = withRestrictedTx(context.Background(), pool2(t, dsn), func(tx pgx.Tx) error {
		var visible, own int
		if err := tx.QueryRow(context.Background(),
			`SELECT count(*), count(*) FILTER (WHERE organization_id = $1::uuid) FROM content.modeling_plans`, orgA,
		).Scan(&visible, &own); err != nil {
			return err
		}
		if visible != 0 {
			t.Fatalf("no GUC: saw %d rows, want 0", visible)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("restricted tx: %v", err)
	}
	err = withTx(context.Background(), pool2(t, dsn), orgA, func(tx pgx.Tx) error {
		var total, own int
		if err := tx.QueryRow(context.Background(),
			`SELECT count(*), count(*) FILTER (WHERE organization_id = $1::uuid) FROM content.modeling_plans`, orgA,
		).Scan(&total, &own); err != nil {
			return err
		}
		if total == 0 || total != own {
			t.Fatalf("orgA GUC: total=%d own=%d (orgB rows must stay hidden, orgA rows visible)", total, own)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("restricted tx: %v", err)
	}

	// 服务层全链（Save→List→Patch）在 GUC 事务内工作。
	servicePool := pool2(t, dsn)
	svc := Service{Store: &store.Store{Pool: servicePool}, Policy: authz.WorkspacePolicyService{Store: &store.Store{Pool: servicePool}}}
	admin := auth.Principal{OrganizationID: orgA, UserID: creator, UserType: auth.UserTypeMember}
	saved, err := svc.Save(context.Background(), admin, wsA, SaveInput{
		Intent: "rls-it service",
		Plan:   []byte(`[{"source_asset_id":"bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb","enabled":true}]`),
	})
	if err != nil {
		t.Fatalf("save: %v", err)
	}
	listed, err := svc.List(context.Background(), admin, wsA, 10)
	if err != nil || len(listed) == 0 {
		t.Fatalf("list: %v (%d)", err, len(listed))
	}
	approved, err := svc.Patch(context.Background(), admin, wsA, saved.ID, PatchInput{Status: strPtr(StatusApproved)})
	if err != nil || approved.Status != StatusApproved {
		t.Fatalf("patch: %v (%s)", err, approved.Status)
	}
}

func strPtr(s string) *string { return &s }

// withRestrictedTx 以受限角色开事务（owner 身份可 SET ROLE 到任意角色），
// 先清掉 GUC（superuser 连接默认无 GUC，显式置空表达真实语义），再交给 fn。
func withRestrictedTx(ctx context.Context, pool *pgxpool.Pool, fn func(tx pgx.Tx) error) error {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, `SET LOCAL ROLE agentchunzhi_app`); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `SELECT set_config('app.organization_id', '', true)`); err != nil {
		return err
	}
	if err := fn(tx); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// withTx 以受限角色 + 指定组织 GUC 开事务。
func withTx(ctx context.Context, pool *pgxpool.Pool, orgID string, fn func(tx pgx.Tx) error) error {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, `SET LOCAL ROLE agentchunzhi_app`); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `SELECT set_config('app.organization_id', $1, true)`, orgID); err != nil {
		return err
	}
	if err := fn(tx); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func pool2(t *testing.T, dsn string) *pgxpool.Pool {
	t.Helper()
	pool, err := pgxpool.New(context.Background(), dsn)
	if err != nil {
		t.Fatalf("connect2: %v", err)
	}
	return pool
}
