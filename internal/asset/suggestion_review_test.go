package asset

// suggestion_review_test.go — pure-logic coverage for the phase 4 review
// merge semantics (single revision bump per call, deterministic relation
// materialization) plus one DB-gated skeleton for the accept→commit→
// materialization chain. The database cases need
// AGENTCHUNZHI_TEST_DATABASE_URL and skip without it.

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"agentchunzhi/internal/auth"
	"agentchunzhi/internal/authz"
	"agentchunzhi/internal/eventing"
	"agentchunzhi/internal/store"
	"agentchunzhi/internal/tag"
)

func TestDraftMergePlanMergesFieldsAndSummary(t *testing.T) {
	draft := Draft{
		AssetID:  "11111111-1111-4111-8111-111111111111",
		Revision: 7,
		Summary:  "old summary",
		Fields:   map[string]any{"keep": "me", "stale": "value"},
	}
	plan := newDraftMergePlan(draft)
	plan.mergeField("language", "go")
	plan.mergeSummary("AI summary")

	next := plan.apply(draft)
	if next.Fields["keep"] != "me" {
		t.Fatalf("existing field must survive the merge, got %v", next.Fields)
	}
	if next.Fields["stale"] != "value" {
		t.Fatalf("untouched fields must keep their values, got %v", next.Fields)
	}
	if next.Fields["language"] != "go" {
		t.Fatalf("merged field missing: %v", next.Fields)
	}
	if next.Summary != "AI summary" {
		t.Fatalf("summary merge missing: %q", next.Summary)
	}
	if next.Revision != draft.Revision {
		t.Fatalf("the plan never moves the revision itself; caller persists once")
	}
	// The base draft stays untouched so a failed batch can fall back to it.
	if _, ok := draft.Fields["language"]; ok {
		t.Fatalf("merge must not mutate the base draft fields")
	}
	if draft.Summary != "old summary" {
		t.Fatalf("merge must not mutate the base draft summary")
	}
}

func TestDraftMergePlanDuplicateFieldLastWriteWins(t *testing.T) {
	draft := Draft{Fields: map[string]any{}}
	plan := newDraftMergePlan(draft)
	plan.mergeField("score", 1)
	plan.mergeField("score", 2)
	next := plan.apply(draft)
	if next.Fields["score"] != 2 {
		t.Fatalf("later suggestion for the same key must win, got %v", next.Fields["score"])
	}
}

func TestDraftMergePlanRevertRestoresBaseFields(t *testing.T) {
	draft := Draft{Fields: map[string]any{"keep": "me"}}
	plan := newDraftMergePlan(draft)
	if plan.hasFields() {
		t.Fatalf("a fresh plan carries no field merges")
	}
	plan.mergeField("language", "go")
	plan.mergeSummary("AI summary")
	if !plan.hasFields() {
		t.Fatalf("mergeField must arm the field map")
	}
	plan.revertFields()
	next := plan.apply(draft)
	if _, ok := next.Fields["language"]; ok {
		t.Fatalf("revert must drop merged fields, got %v", next.Fields)
	}
	if len(next.Fields) != 1 || next.Fields["keep"] != "me" {
		t.Fatalf("revert must restore the base field set, got %v", next.Fields)
	}
	if next.Summary != "AI summary" {
		t.Fatalf("revert only undoes field merges, not the summary, got %q", next.Summary)
	}
}

func TestSplitFieldOutcomesPartitionsByKind(t *testing.T) {
	outcomes := []ReviewOutcome{
		{SuggestionID: "a", Kind: SuggestionKindField},
		{SuggestionID: "b", Kind: SuggestionKindTag},
		{SuggestionID: "c", Kind: SuggestionKindSummary},
		{SuggestionID: "d", Kind: SuggestionKindRelation},
	}
	failed, kept := splitFieldOutcomes(outcomes)
	if len(failed) != 2 || failed[0].SuggestionID != "a" || failed[1].SuggestionID != "c" {
		t.Fatalf("field/summary outcomes must partition into failed, got %v", failed)
	}
	if len(kept) != 2 || kept[0].SuggestionID != "b" || kept[1].SuggestionID != "d" {
		t.Fatalf("tag/relation outcomes must survive, got %v", kept)
	}
}

func TestNormalizeRelationMaterials(t *testing.T) {
	const (
		low  = "11111111-1111-4111-8111-111111111111"
		high = "22222222-2222-4222-8222-222222222222"
	)
	normalized, err := normalizeRelationMaterials([]RelationMaterial{
		{TargetAssetID: high, RelationType: RelationCites, Confidence: 0.5, SuggestionID: "s-cites"},
		{TargetAssetID: low, RelationType: RelationReferences, Confidence: 0.9, SuggestionID: "s-refs"},
		{TargetAssetID: low, RelationType: RelationReferences, Confidence: 0.1, SuggestionID: "s-dup"},
	})
	if err != nil {
		t.Fatalf("normalize must accept valid materials: %v", err)
	}
	if len(normalized) != 2 {
		t.Fatalf("duplicates must collapse, got %d: %+v", len(normalized), normalized)
	}
	// Deterministic (target, type) order regardless of input order; the first
	// entry for a duplicate wins.
	if normalized[0].TargetAssetID != low || normalized[0].SuggestionID != "s-refs" {
		t.Fatalf("unexpected order/first-wins: %+v", normalized)
	}
	if normalized[1].TargetAssetID != high || normalized[1].RelationType != RelationCites {
		t.Fatalf("unexpected order: %+v", normalized)
	}

	if _, err := normalizeRelationMaterials([]RelationMaterial{{TargetAssetID: low, RelationType: "likes"}}); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("unknown relation type must fail with ErrInvalidInput, got %v", err)
	}
	if _, err := normalizeRelationMaterials([]RelationMaterial{{TargetAssetID: "not-a-uuid", RelationType: RelationCites}}); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("invalid target id must fail with ErrInvalidInput, got %v", err)
	}
	if materials, err := normalizeRelationMaterials(nil); err != nil || len(materials) != 0 {
		t.Fatalf("nil input must normalize to an empty set without error, got %v, %v", materials, err)
	}
}

func TestValidRelationTypeAndSuggestionKind(t *testing.T) {
	for _, relationType := range []string{RelationRelatedTo, RelationReferences, RelationDerivedFrom, RelationCites, RelationContinuesFrom} {
		if !ValidRelationType(relationType) {
			t.Fatalf("contract relation type %q must validate", relationType)
		}
	}
	for _, relationType := range []string{"", "likes", "RELATED_TO"} {
		if ValidRelationType(relationType) {
			t.Fatalf("relation type %q must not validate", relationType)
		}
	}
	for _, kind := range []string{SuggestionKindField, SuggestionKindSummary, SuggestionKindTag, SuggestionKindRelation} {
		if !ValidSuggestionKind(kind) {
			t.Fatalf("suggestion kind %q must validate", kind)
		}
	}
	for _, kind := range []string{"", "attachment", "Field"} {
		if ValidSuggestionKind(kind) {
			t.Fatalf("suggestion kind %q must not validate", kind)
		}
	}
}

func TestSuggestionCursorRoundTrip(t *testing.T) {
	item := SuggestionItem{
		ID:        "11111111-1111-4111-8111-111111111111",
		CreatedAt: mustParseTime(t, "2026-08-30T12:00:00.123456789Z"),
	}
	decoded, err := decodeSuggestionCursor(encodeSuggestionCursor(item))
	if err != nil {
		t.Fatalf("cursor must round-trip: %v", err)
	}
	if decoded.ID != item.ID {
		t.Fatalf("cursor id mismatch: %q", decoded.ID)
	}
	encodedAgain := encodeSuggestionCursor(SuggestionItem{ID: decoded.ID, CreatedAt: mustParseTime(t, decoded.CreatedAt)})
	if encodedAgain != encodeSuggestionCursor(item) {
		t.Fatalf("cursor encoding must be stable across a round-trip")
	}
	for _, invalid := range []string{"", "!!!", "e30"} {
		if _, err := decodeSuggestionCursor(invalid); !errors.Is(err, ErrInvalidInput) {
			t.Fatalf("cursor %q must fail with ErrInvalidInput, got %v", invalid, err)
		}
	}
}

func TestMapTagSuggestionError(t *testing.T) {
	cases := map[error]error{
		tag.ErrSuggestionNotFound: ErrSuggestionNotFound,
		tag.ErrSuggestionState:    ErrSuggestionStateInvalid,
		tag.ErrSuggestionNoTag:    ErrInvalidInput,
	}
	for input, want := range cases {
		if got := mapTagSuggestionError(input); !errors.Is(got, want) {
			t.Fatalf("mapTagSuggestionError(%v) = %v, want %v", input, got, want)
		}
	}
	unwrapped := errors.New("boom")
	if got := mapTagSuggestionError(unwrapped); !errors.Is(got, unwrapped) {
		t.Fatalf("unknown errors must pass through, got %v", got)
	}
}

// mustParseTime parses one RFC3339 timestamp or fails the test.
func mustParseTime(t *testing.T, value string) time.Time {
	t.Helper()
	parsed, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		t.Fatalf("parse time %q: %v", value, err)
	}
	return parsed
}

// TestSuggestionReviewFlowIntegration walks the phase 4 confirmation chain
// against a real database: accept a field, a summary and a relation
// suggestion in one batch (the draft revision moves exactly once), then
// commit the draft and assert every suggestion backfills its materialized
// version/relation and the processing result records the output version.
func TestSuggestionReviewFlowIntegration(t *testing.T) {
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

	// Organization, member, workspace, model + version, two assets with a
	// sealed version and a shared draft each, one automation run and one
	// processing result reading the source asset's version 1.
	var organizationID, memberID, workspaceID, modelID, modelVersionID string
	var sourceAssetID, targetAssetID, sourceVersionID, sourceDraftID string
	var runID, fieldSuggestionID, summarySuggestionID, relationSuggestionID, resultID string
	err = db.Pool.QueryRow(ctx, `
		WITH org AS (
			INSERT INTO organization.organizations (name, status)
			VALUES ('ITC-SuggReview-' || gen_random_uuid()::text, 'active') RETURNING id
		), member AS (
			INSERT INTO identity.users (organization_id, user_type, display_name, status)
			SELECT id, 'member', 'ITC SuggReview Editor', 'active' FROM org RETURNING id, organization_id
		), ws AS (
			INSERT INTO content.workspaces (organization_id, slug, name, created_by)
			SELECT organization_id, 'ws-sugg-' || gen_random_uuid()::text, 'ITC SuggReview WS', id FROM member RETURNING id, organization_id
		), membership AS (
			INSERT INTO content.workspace_members (organization_id, workspace_id, user_id, role, granted_by)
			SELECT organization_id, id, (SELECT id FROM member), 'editor', (SELECT id FROM member) FROM ws
		), model AS (
			INSERT INTO model.resource_models (organization_id, workspace_id, model_key, name, created_by)
			SELECT organization_id, id, 'sugg-' || gen_random_uuid()::text, 'ITC SuggReview Model', (SELECT id FROM member)
			FROM ws RETURNING id, organization_id
		), model_version AS (
			INSERT INTO model.resource_model_versions
				(organization_id, resource_model_id, version_no, status, policy, field_schema, created_by)
			SELECT organization_id, id, 1, 'published', '{}'::jsonb, '{}'::jsonb, (SELECT id FROM member)
			FROM model RETURNING id, resource_model_id
		), model_link AS (
			UPDATE model.resource_models m SET current_version_id = v.id
			FROM model_version v WHERE m.id = v.resource_model_id
		), asset_src AS (
			INSERT INTO asset.assets (organization_id, workspace_id, resource_model_id, created_by)
			SELECT organization_id, id, (SELECT id FROM model), id FROM member RETURNING id
		), asset_tgt AS (
			INSERT INTO asset.assets (organization_id, workspace_id, resource_model_id, created_by)
			SELECT organization_id, (SELECT id FROM ws), (SELECT id FROM model), id FROM member RETURNING id
		), version_src AS (
			INSERT INTO asset.asset_versions
				(organization_id, workspace_id, asset_id, resource_model_id, resource_model_version_id,
				 version_no, title, content_checksum, created_by, sealed_at)
			SELECT a.organization_id, a.workspace_id, a.id, a.resource_model_id, (SELECT id FROM model_version),
			       1, 'ITC source', 'itc-sugg-src', (SELECT id FROM member), now()
			FROM asset_src a RETURNING id, asset_id
		), version_tgt AS (
			INSERT INTO asset.asset_versions
				(organization_id, workspace_id, asset_id, resource_model_id, resource_model_version_id,
				 version_no, title, content_checksum, created_by, sealed_at)
			SELECT a.organization_id, a.workspace_id, a.id, a.resource_model_id, (SELECT id FROM model_version),
			       1, 'ITC target', 'itc-sugg-tgt', (SELECT id FROM member), now()
			FROM asset_tgt a RETURNING id, asset_id
		), draft_src AS (
			INSERT INTO asset.asset_drafts (organization_id, workspace_id, asset_id, base_version_id, title)
			SELECT a.organization_id, a.workspace_id, a.id, (SELECT id FROM version_src), 'ITC source'
			FROM asset_src a RETURNING id
		), draft_tgt AS (
			INSERT INTO asset.asset_drafts (organization_id, workspace_id, asset_id, base_version_id, title)
			SELECT a.organization_id, a.workspace_id, a.id, (SELECT id FROM version_tgt), 'ITC target'
			FROM asset_tgt a RETURNING id
		), link_src AS (
			UPDATE asset.assets a SET current_working_version_id = (SELECT id FROM version_src),
			       draft_id = (SELECT id FROM draft_src)
			WHERE a.id = (SELECT asset_id FROM version_src)
		), link_tgt AS (
			UPDATE asset.assets a SET current_working_version_id = (SELECT id FROM version_tgt),
			       draft_id = (SELECT id FROM draft_tgt)
			WHERE a.id = (SELECT asset_id FROM version_tgt)
		), run AS (
			INSERT INTO automation.runs (organization_id, workspace_id, source, operation, status, created_by)
			SELECT organization_id, id, 'manual', 'asset_prepare', 'succeeded', (SELECT id FROM member)
			FROM ws RETURNING id
		), result AS (
			INSERT INTO integration.agent_processing_results
				(organization_id, workspace_id, run_id, asset_id, input_version_id, agent_user_id, agent_application_id)
			SELECT a.organization_id, a.workspace_id, (SELECT id FROM run), a.id,
			       (SELECT id FROM version_src), (SELECT id FROM member), '00000000-0000-4000-8000-000000000000'
			FROM asset_src a RETURNING id
		), field_sugg AS (
			INSERT INTO asset.asset_field_suggestions
				(organization_id, workspace_id, source_version_id, run_id, kind, field_key, value, confidence)
			SELECT v.organization_id, v.workspace_id, v.id, (SELECT id FROM run), 'field', 'language',
			       '"go"'::jsonb, 0.9
			FROM version_src v RETURNING id
		), summary_sugg AS (
			INSERT INTO asset.asset_field_suggestions
				(organization_id, workspace_id, source_version_id, run_id, kind, field_key, value, confidence)
			SELECT v.organization_id, v.workspace_id, v.id, (SELECT id FROM run), 'summary', '',
			       '"AI summary"'::jsonb, 0.8
			FROM version_src v RETURNING id
		), relation_sugg AS (
			INSERT INTO asset.asset_relation_suggestions
				(organization_id, workspace_id, source_version_id, run_id, target_asset_id, relation_type, confidence)
			SELECT v.organization_id, v.workspace_id, v.id, (SELECT id FROM run),
			       (SELECT id FROM asset_tgt), 'cites', 0.7
			FROM version_src v RETURNING id
		)
		SELECT o.id::text, m.id::text, w.id::text, mo.id::text, mv.id::text,
		       va.id::text, (SELECT id FROM asset_tgt), vs.id::text, ds.id::text,
		       r.id::text, f.id::text, s.id::text, rel.id::text, pr.id::text
		FROM org o, member m, ws w, model mo, model_version mv, asset_src va,
		     version_src vs, draft_src ds, run r, field_sugg f, summary_sugg s,
		     relation_sugg rel, result pr
	`).Scan(&organizationID, &memberID, &workspaceID, &modelID, &modelVersionID,
		&sourceAssetID, &targetAssetID, &sourceVersionID, &sourceDraftID,
		&runID, &fieldSuggestionID, &summarySuggestionID, &relationSuggestionID, &resultID)
	if err != nil {
		t.Fatalf("seed integration fixture: %v", err)
	}
	t.Cleanup(func() {
		_, _ = db.Pool.Exec(context.Background(),
			`DELETE FROM organization.organizations WHERE id = $1::uuid`, organizationID)
	})

	principal := auth.Principal{OrganizationID: organizationID, UserID: memberID, UserType: auth.UserTypeMember}
	policy := authz.WorkspacePolicyService{Store: db}
	registry, err := eventing.DefaultRegistry()
	if err != nil {
		t.Fatalf("build event registry: %v", err)
	}
	review := SuggestionReviewService{Store: db, Policy: policy}
	member := MemberService{Store: db, Policy: policy, Events: &eventing.EventStore{Registry: registry}}

	var baseRevision int64
	if err := db.Pool.QueryRow(ctx, `
		SELECT revision FROM asset.asset_drafts WHERE id = $1::uuid
	`, sourceDraftID).Scan(&baseRevision); err != nil {
		t.Fatalf("load base revision: %v", err)
	}

	// One batch accepts a field, a summary and a relation: the draft revision
	// advances exactly once.
	batch, err := review.AcceptBatch(ctx, principal, workspaceID, sourceAssetID, []AcceptRef{
		{Kind: SuggestionKindField, SuggestionID: fieldSuggestionID},
		{Kind: SuggestionKindSummary, SuggestionID: summarySuggestionID},
		{Kind: SuggestionKindRelation, SuggestionID: relationSuggestionID},
	})
	if err != nil {
		t.Fatalf("accept batch: %v", err)
	}
	if len(batch.Accepted) != 3 || len(batch.Failed) != 0 {
		t.Fatalf("batch must accept all three suggestions, got accepted=%d failed=%v", len(batch.Accepted), batch.Failed)
	}
	if batch.DraftRevision != baseRevision+1 {
		t.Fatalf("batch must bump the draft revision exactly once: base=%d got=%d", baseRevision, batch.DraftRevision)
	}
	var mergedFields map[string]any
	var mergedSummary string
	var actualRevision int64
	if err := db.Pool.QueryRow(ctx, `
		SELECT fields, summary, revision FROM asset.asset_drafts WHERE id = $1::uuid
	`, sourceDraftID).Scan(&mergedFields, &mergedSummary, &actualRevision); err != nil {
		t.Fatalf("load merged draft: %v", err)
	}
	if mergedFields["language"] != "go" {
		t.Fatalf("field suggestion must merge into the draft, got %v", mergedFields)
	}
	if mergedSummary != "AI summary" {
		t.Fatalf("summary suggestion must merge into the draft, got %q", mergedSummary)
	}
	if actualRevision != baseRevision+1 {
		t.Fatalf("draft revision on disk must be base+1, got %d", actualRevision)
	}
	var draftRelations int
	if err := db.Pool.QueryRow(ctx, `
		SELECT count(*) FROM asset.asset_draft_relations WHERE asset_draft_id = $1::uuid
	`, sourceDraftID).Scan(&draftRelations); err != nil {
		t.Fatalf("count draft relations: %v", err)
	}
	if draftRelations != 1 {
		t.Fatalf("accepted relation must park on the draft, got %d rows", draftRelations)
	}

	// Committing the draft materializes everything and backfills the
	// suggestion provenance and the processing result.
	commit, err := member.CommitDraft(ctx, principal, workspaceID, sourceAssetID, "")
	if err != nil {
		t.Fatalf("commit draft: %v", err)
	}
	if !commit.Created {
		t.Fatalf("commit must create a new version")
	}
	var materializedField, materializedSummary, materializedRelation, outputVersion string
	if err := db.Pool.QueryRow(ctx, `
		SELECT
		  (SELECT materialized_version_id::text FROM asset.asset_field_suggestions WHERE id = $2::uuid),
		  (SELECT materialized_version_id::text FROM asset.asset_field_suggestions WHERE id = $3::uuid),
		  (SELECT COALESCE(materialized_relation_id::text, '') FROM asset.asset_relation_suggestions WHERE id = $4::uuid),
		  (SELECT COALESCE(output_version_id::text, '') FROM integration.agent_processing_results WHERE id = $5::uuid)
	`, organizationID, fieldSuggestionID, summarySuggestionID, relationSuggestionID, resultID).Scan(
		&materializedField, &materializedSummary, &materializedRelation, &outputVersion); err != nil {
		t.Fatalf("load backfilled provenance: %v", err)
	}
	if materializedField != commit.VersionID || materializedSummary != commit.VersionID {
		t.Fatalf("field/summary suggestions must backfill the commit version %s, got %s/%s",
			commit.VersionID, materializedField, materializedSummary)
	}
	if materializedRelation == "" {
		t.Fatalf("relation suggestion must backfill materialized_relation_id")
	}
	if outputVersion != commit.VersionID {
		t.Fatalf("processing result must record the output version %s, got %s", commit.VersionID, outputVersion)
	}
	var edgeCount int
	var edgeSource string
	if err := db.Pool.QueryRow(ctx, `
		SELECT count(*), COALESCE((SELECT source FROM asset.asset_relations WHERE id = $2::uuid), '')
		FROM asset.asset_relations WHERE id = $2::uuid
	`, organizationID, materializedRelation).Scan(&edgeCount, &edgeSource); err != nil {
		t.Fatalf("load materialized edge: %v", err)
	}
	if edgeCount != 1 || edgeSource != tag.SourceAI {
		t.Fatalf("materialized edge must exist with source ai, got count=%d source=%q", edgeCount, edgeSource)
	}

	// The review queue reflects the terminal states.
	page, err := review.List(ctx, principal, workspaceID, sourceAssetID, "all", "", "", 50)
	if err != nil {
		t.Fatalf("list suggestions: %v", err)
	}
	statuses := map[string]string{}
	kinds := map[string]string{}
	for _, item := range page.Items {
		statuses[item.ID] = item.Status
		kinds[item.ID] = item.Kind
	}
	for id, wantKind := range map[string]string{
		fieldSuggestionID:    SuggestionKindField,
		summarySuggestionID:  SuggestionKindSummary,
		relationSuggestionID: SuggestionKindRelation,
	} {
		if statuses[id] != SuggestionStatusAccepted {
			t.Fatalf("suggestion %s must read accepted in the queue, got %q", id, statuses[id])
		}
		if kinds[id] != wantKind {
			t.Fatalf("suggestion %s must surface as kind %s, got %q", id, wantKind, kinds[id])
		}
	}
	if len(page.ProcessingResults) != 1 || page.ProcessingResults[0].ID != resultID {
		t.Fatalf("queue must carry the processing result summary, got %+v", page.ProcessingResults)
	}
	if page.ProcessingResults[0].OutputVersionID != commit.VersionID {
		t.Fatalf("processing result summary must expose the output version, got %q", page.ProcessingResults[0].OutputVersionID)
	}
}
