package checkpoint

import (
	"context"
	"encoding/base64"
	"errors"
	"os"
	"testing"

	"agentchunzhi/internal/modelendpoint"
	"agentchunzhi/internal/store"

	"github.com/cloudwego/eino/compose"
	"github.com/google/uuid"
)

func TestPostgresStoreRoundTripIntegration(t *testing.T) {
	databaseURL := os.Getenv("AGENTCHUNZHI_TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("AGENTCHUNZHI_TEST_DATABASE_URL is not set")
	}
	ctx := context.Background()
	db, err := store.Open(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(db.Close)
	var organizationID, memberID, workspaceID string
	if err := db.Pool.QueryRow(ctx, `
		SELECT o.id::text, u.id::text, w.id::text
		FROM organization.organizations o
		JOIN identity.users u ON u.organization_id = o.id AND u.user_type = 'member' AND u.status = 'active'
		JOIN content.workspaces w ON w.organization_id = o.id AND w.status = 'active'
		ORDER BY o.created_at, u.created_at, w.created_at
		LIMIT 1
	`).Scan(&organizationID, &memberID, &workspaceID); err != nil {
		t.Fatalf("load checkpoint integration scope: %v", err)
	}
	runID := uuid.NewString()
	if _, err := db.Pool.Exec(ctx, `
		INSERT INTO automation.runs (id, organization_id, workspace_id, source, operation, status, created_by)
		VALUES ($1::uuid, $2::uuid, $3::uuid, 'agent', 'react', 'queued', $4::uuid)
	`, runID, organizationID, workspaceID, memberID); err != nil {
		t.Fatalf("create checkpoint run: %v", err)
	}
	t.Cleanup(func() { _, _ = db.Pool.Exec(ctx, `DELETE FROM automation.runs WHERE id = $1::uuid`, runID) })
	key, err := modelendpoint.NewCredentialCipher(base64.StdEncoding.EncodeToString([]byte("0123456789abcdef0123456789abcdef")))
	if err != nil {
		t.Fatal(err)
	}
	checkpointStore := PostgresStore{Store: db, Cipher: key, OrganizationID: organizationID, RunID: runID}
	payload := []byte(`{"node":"asset_prepare","state":{"safe":true}}`)
	if err := checkpointStore.Set(ctx, "root/checkpoint", payload); err != nil {
		t.Fatalf("save checkpoint: %v", err)
	}
	updatedPayload := []byte(`{"node":"asset_prepare","state":{"safe":true,"step":2}}`)
	if err := checkpointStore.Set(ctx, "root/checkpoint", updatedPayload); err != nil {
		t.Fatalf("append checkpoint revision: %v", err)
	}
	loaded, ok, err := checkpointStore.Get(ctx, "root/checkpoint")
	if err != nil || !ok || string(loaded) != string(updatedPayload) {
		t.Fatalf("load checkpoint: ok=%v value=%q err=%v", ok, loaded, err)
	}
	var sequenceCount int
	if err := db.Pool.QueryRow(ctx, `SELECT count(*) FROM automation.checkpoints WHERE run_id = $1::uuid AND checkpoint_key = 'root/checkpoint'`, runID).Scan(&sequenceCount); err != nil {
		t.Fatal(err)
	}
	if sequenceCount != 2 {
		t.Fatalf("checkpoint writes must be append-only, got %d rows", sequenceCount)
	}
	var ciphertext []byte
	if err := db.Pool.QueryRow(ctx, `SELECT payload_ciphertext FROM automation.checkpoints WHERE run_id = $1::uuid`, runID).Scan(&ciphertext); err != nil {
		t.Fatal(err)
	}
	if string(ciphertext) == string(payload) || string(ciphertext) == "" {
		t.Fatal("checkpoint payload must not be stored in plaintext")
	}
	if _, ok, err := checkpointStore.Get(ctx, "missing"); err != nil || ok {
		t.Fatalf("missing checkpoint should return (nil,false,nil): ok=%v err=%v", ok, err)
	}
	if _, _, err := (PostgresStore{Store: db, Cipher: key, OrganizationID: organizationID, RunID: ""}).Get(ctx, "root/checkpoint"); !errors.Is(err, ErrInvalidCheckpointKey) {
		t.Fatalf("invalid scoped checkpoint should be rejected, got %v", err)
	}
}

func TestPostgresStoreCrossProcessGraphResumeIntegration(t *testing.T) {
	databaseURL := os.Getenv("AGENTCHUNZHI_TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("AGENTCHUNZHI_TEST_DATABASE_URL is not set")
	}
	ctx := context.Background()
	db, err := store.Open(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(db.Close)
	var organizationID, memberID, workspaceID string
	if err := db.Pool.QueryRow(ctx, `
		SELECT o.id::text, u.id::text, w.id::text
		FROM organization.organizations o
		JOIN identity.users u ON u.organization_id = o.id AND u.user_type = 'member' AND u.status = 'active'
		JOIN content.workspaces w ON w.organization_id = o.id AND w.status = 'active'
		ORDER BY o.created_at, u.created_at, w.created_at LIMIT 1
	`).Scan(&organizationID, &memberID, &workspaceID); err != nil {
		t.Fatalf("load graph resume scope: %v", err)
	}
	runID := uuid.NewString()
	if _, err := db.Pool.Exec(ctx, `
		INSERT INTO automation.runs (id, organization_id, workspace_id, source, operation, status, created_by)
		VALUES ($1::uuid, $2::uuid, $3::uuid, 'agent', 'react', 'queued', $4::uuid)
	`, runID, organizationID, workspaceID, memberID); err != nil {
		t.Fatalf("create graph resume run: %v", err)
	}
	t.Cleanup(func() {
		_, _ = db.Pool.Exec(context.Background(), `DELETE FROM automation.runs WHERE id = $1::uuid`, runID)
	})
	cipher, err := modelendpoint.NewCredentialCipher(base64.StdEncoding.EncodeToString([]byte("0123456789abcdef0123456789abcdef")))
	if err != nil {
		t.Fatal(err)
	}
	checkpointID := "cross-process-" + uuid.NewString()
	buildGraph := func(store PostgresStore) compose.Runnable[string, string] {
		graph := compose.NewGraph[string, string]()
		if err := graph.AddLambdaNode("approval", compose.InvokableLambda(func(ctx context.Context, input string) (string, error) {
			isResume, _, value := compose.GetResumeContext[string](ctx)
			if !isResume {
				return "", compose.Interrupt(ctx, map[string]any{"reason": "approval required"})
			}
			return value, nil
		})); err != nil {
			t.Fatalf("add resume node: %v", err)
		}
		if err := graph.AddEdge(compose.START, "approval"); err != nil {
			t.Fatalf("add resume start edge: %v", err)
		}
		if err := graph.AddEdge("approval", compose.END); err != nil {
			t.Fatalf("add resume end edge: %v", err)
		}
		runnable, err := graph.Compile(ctx, compose.WithGraphName("cross_process_resume"), compose.WithCheckPointStore(store))
		if err != nil {
			t.Fatalf("compile resume graph: %v", err)
		}
		return runnable
	}
	first := buildGraph(PostgresStore{Store: db, Cipher: cipher, OrganizationID: organizationID, RunID: runID})
	_, err = first.Invoke(ctx, "initial", compose.WithCheckPointID(checkpointID))
	if err == nil {
		t.Fatal("first graph invocation should interrupt")
	}
	interruptInfo, ok := compose.ExtractInterruptInfo(err)
	if !ok || len(interruptInfo.InterruptContexts) != 1 {
		t.Fatalf("unexpected interrupt info: ok=%v info=%+v err=%v", ok, interruptInfo, err)
	}
	second := buildGraph(PostgresStore{Store: db, Cipher: cipher, OrganizationID: organizationID, RunID: runID})
	output, err := second.Invoke(compose.ResumeWithData(ctx, interruptInfo.InterruptContexts[0].ID, "resumed"), "", compose.WithCheckPointID(checkpointID))
	if err != nil || output != "resumed" {
		t.Fatalf("cross-process resume failed: output=%q err=%v", output, err)
	}
}
