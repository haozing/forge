package asset

// transfer_tag_roundtrip_test.go — phase 6 P6-1 coverage for the import/export
// tag closure. The pure cases pin the wire contract: the export tag_keys cell
// (a JSON array of normalized keys, see formatTagKeysCell/loadExportTagKeys)
// must parse back through the import channel unchanged, and structurally
// invalid keys must be detectable before any row is written. The integration
// case runs the real row worker against a live database
// (AGENTCHUNZHI_TEST_DATABASE_URL, skipped without it): seeded assets are
// exported through the worker's own query and cell assembly, the cells are
// replayed as import rows under unknown_tag_policy=create, and the imported
// versions must expose the exact source tag sets — covering unknown key
// creation, archived key restore, duplicate-key idempotency and the
// tag_key_invalid reject.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"testing"

	"agentchunzhi/internal/eventing"
	"agentchunzhi/internal/store"
	"agentchunzhi/internal/tag"
)

// TestExportTagKeysCellRoundTripsThroughImportParsing proves the two wire
// sides are inverse: export emits a JSON array of normalized keys (the exact
// asset.tags.normalized_key values read by loadExportTagKeys) and the import
// channel reads the same JSON array, so re-normalizing an exported key set is
// a no-op by construction.
func TestExportTagKeysCellRoundTripsThroughImportParsing(t *testing.T) {
	for _, keys := range [][]string{
		{"cms 文档", "go", "release"},
		{"single"},
		{},
	} {
		// Export cells only ever carry stored normalized keys.
		for _, key := range keys {
			normalized, err := tag.NormalizeKey(key)
			if err != nil {
				t.Fatalf("sample key %q must normalize: %v", key, err)
			}
			if normalized != key {
				t.Fatalf("export payloads carry normalized keys; %q is not one", key)
			}
		}
		cell := formatTagKeysCell(keys) // export writer side
		var row map[string]any
		if err := json.Unmarshal([]byte(`{"title":"t","tag_keys":`+cell+`}`), &row); err != nil {
			t.Fatalf("export cell %q must be embeddable JSON: %v", cell, err)
		}
		parsed, ok, err := importRowTagKeys(row) // import reader side
		if err != nil || !ok {
			t.Fatalf("import must accept the export cell %q, ok=%v err=%v", cell, ok, err)
		}
		roundTripped, err := normalizeImportTagKeys(parsed)
		if err != nil {
			t.Fatalf("exported keys must survive import normalization: %v", err)
		}
		if len(roundTripped) != len(keys) {
			t.Fatalf("round trip changed key count: %v -> %v", keys, roundTripped)
		}
		for index, key := range keys {
			if roundTripped[index] != key {
				t.Fatalf("round trip changed key %q -> %q", key, roundTripped[index])
			}
		}
	}
}

// TestImportTagKeysColumnShapes pins the column-level semantics: missing
// column and null cell mean "no tags", an empty array is a valid empty set,
// and broken cells (non-array JSON, non-string entries — how a malformed CSV
// cell surfaces after the row mapping) fail so the worker rejects the row.
func TestImportTagKeysColumnShapes(t *testing.T) {
	if _, ok, err := importRowTagKeys(map[string]any{"title": "t"}); ok || err != nil {
		t.Fatalf("missing column must mean no tags, ok=%v err=%v", ok, err)
	}
	if _, ok, err := importRowTagKeys(map[string]any{"tag_keys": nil}); ok || err != nil {
		t.Fatalf("null cell must behave like missing, ok=%v err=%v", ok, err)
	}
	keys, ok, err := importRowTagKeys(map[string]any{"tag_keys": []any{}})
	if err != nil || !ok || len(keys) != 0 {
		t.Fatalf("empty array must be an empty tag set, ok=%v keys=%v err=%v", ok, keys, err)
	}
	if _, _, err := importRowTagKeys(map[string]any{"tag_keys": "release"}); err == nil {
		t.Fatal("non-array cell (broken JSON) must fail the row")
	}
	if _, _, err := importRowTagKeys(map[string]any{"tag_keys": []any{"release", 42}}); err == nil {
		t.Fatal("non-string entries must fail the row")
	}
}

// TestNormalizeImportTagKeysFlagsInvalidKeys pins the tag_key_invalid reject
// semantics: valid entries fold/trim exactly like the resolver will do again,
// and a structurally broken key is reported as errImportTagKeyInvalid before
// anything is written.
func TestNormalizeImportTagKeysFlagsInvalidKeys(t *testing.T) {
	normalized, err := normalizeImportTagKeys([]string{"  Go  ", "CMS 文档"})
	if err != nil {
		t.Fatalf("valid keys must normalize: %v", err)
	}
	if len(normalized) != 2 || normalized[0] != "go" || normalized[1] != "cms 文档" {
		t.Fatalf("unexpected normalized keys: %#v", normalized)
	}
	if _, err := normalizeImportTagKeys([]string{"ok", "bad\x00key"}); !errors.Is(err, errImportTagKeyInvalid) {
		t.Fatalf("control-character key must be tagged tag_key_invalid, got %v", err)
	}
}

func equalStringSets(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	seen := make(map[string]int, len(a))
	for _, item := range a {
		seen[item]++
	}
	for _, item := range b {
		seen[item]--
		if seen[item] < 0 {
			return false
		}
	}
	return true
}

// TestTransferTagRoundTripIntegration walks the full closure against a real
// database: export seeded tagged assets through the worker's own query and
// cell assembly, replay the cells as import rows (create policy), and assert
// the imported versions carry the exact source tag sets.
func TestTransferTagRoundTripIntegration(t *testing.T) {
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
	events, err := eventing.NewEventStore(db.Pool)
	if err != nil {
		t.Fatalf("build event store: %v", err)
	}

	// Fixture: org, member, workspace, model+version; two active source tags
	// plus one archived tag; two assets with a sealed, tag-carrying version
	// and a shared draft each; one import batch frozen on
	// unknown_tag_policy=create.
	var organizationID, memberID, workspaceID, modelID, modelVersionID, batchID string
	var archivedTagID string
	err = db.Pool.QueryRow(ctx, `
		WITH org AS (
			INSERT INTO organization.organizations (name, slug, status)
			VALUES ('ITC-TagRoundtrip-' || gen_random_uuid()::text, 'ws-tagrt-' || gen_random_uuid()::text, 'active') RETURNING id
		), member AS (
			INSERT INTO identity.users (organization_id, user_type, email, display_name, password_hash, status)
			SELECT id, 'member', 'tagrt-' || gen_random_uuid()::text || '@itc.invalid', 'ITC TagRoundtrip Editor', 'x', 'active' FROM org RETURNING id, organization_id
		), ws AS (
			INSERT INTO content.workspaces (organization_id, slug, name, created_by)
			SELECT organization_id, 'ws-tagrt-' || gen_random_uuid()::text, 'ITC TagRoundtrip WS', id FROM member RETURNING id, organization_id
		), membership AS (
			INSERT INTO content.workspace_members (organization_id, workspace_id, user_id, role, granted_by)
			SELECT organization_id, id, (SELECT id FROM member), 'editor', (SELECT id FROM member) FROM ws
		), model AS (
			INSERT INTO model.resource_models (organization_id, workspace_id, model_key, name, created_by)
			SELECT organization_id, id, 'tagrt-' || gen_random_uuid()::text, 'ITC TagRoundtrip Model', (SELECT id FROM member)
			FROM ws RETURNING id, organization_id
		), model_version AS (
			INSERT INTO model.resource_model_versions
				(organization_id, resource_model_id, version_no, status, policy, field_schema, created_by)
			SELECT organization_id, id, 1, 'published', '{}'::jsonb, '{}'::jsonb, (SELECT id FROM member)
			FROM model RETURNING id, resource_model_id
		), model_link AS (
			UPDATE model.resource_models m SET current_version_id = v.id
			FROM model_version v WHERE m.id = v.resource_model_id
		), ws_id AS (
			SELECT id FROM ws
		), tag_release AS (
			INSERT INTO asset.tags (organization_id, workspace_id, normalized_key, display_name, slug, created_by)
			SELECT organization_id, (SELECT id FROM ws_id), 'release', 'release', 'release', (SELECT id FROM member) FROM ws_id RETURNING id
		), tag_cms AS (
			INSERT INTO asset.tags (organization_id, workspace_id, normalized_key, display_name, slug, created_by)
			SELECT organization_id, (SELECT id FROM ws_id), 'cms 文档', 'cms 文档', 'cms-文档', (SELECT id FROM member) FROM ws_id RETURNING id
		), tag_archived AS (
			INSERT INTO asset.tags (organization_id, workspace_id, normalized_key, display_name, slug, created_by)
			SELECT organization_id, (SELECT id FROM ws_id), 'archive-me', 'archive-me', 'archive-me', (SELECT id FROM member) FROM ws_id RETURNING id
		), archive_tag AS (
			UPDATE asset.tags SET status = 'archived', archived_at = now(), archived_by = (SELECT id FROM member)
			WHERE workspace_id = (SELECT id FROM ws_id) AND normalized_key = 'archive-me'
		), asset_a AS (
			INSERT INTO asset.assets (organization_id, workspace_id, resource_model_id, created_by)
			SELECT organization_id, (SELECT id FROM ws_id), (SELECT id FROM model), id FROM member RETURNING id, organization_id, workspace_id
		), asset_b AS (
			INSERT INTO asset.assets (organization_id, workspace_id, resource_model_id, created_by)
			SELECT organization_id, (SELECT id FROM ws_id), (SELECT id FROM model), id FROM member RETURNING id, organization_id, workspace_id
		), version_a AS (
			INSERT INTO asset.asset_versions
				(organization_id, workspace_id, asset_id, resource_model_id, resource_model_version_id,
				 version_no, title, markdown, content_checksum, created_by, sealed_at)
			SELECT a.organization_id, a.workspace_id, a.id, (SELECT id FROM model), (SELECT id FROM model_version),
			       1, 'Roundtrip A', 'body a', 'tagrt-checksum-a', (SELECT id FROM member), now()
			FROM asset_a a RETURNING id, asset_id
		), version_b AS (
			INSERT INTO asset.asset_versions
				(organization_id, workspace_id, asset_id, resource_model_id, resource_model_version_id,
				 version_no, title, markdown, content_checksum, created_by, sealed_at)
			SELECT a.organization_id, a.workspace_id, a.id, (SELECT id FROM model), (SELECT id FROM model_version),
			       1, 'Roundtrip B', 'body b', 'tagrt-checksum-b', (SELECT id FROM member), now()
			FROM asset_b a RETURNING id, asset_id
		), draft_a AS (
			INSERT INTO asset.asset_drafts (organization_id, workspace_id, asset_id, base_version_id, title)
			SELECT a.organization_id, a.workspace_id, a.id, (SELECT id FROM version_a), 'Roundtrip A' FROM asset_a a RETURNING id
		), draft_b AS (
			INSERT INTO asset.asset_drafts (organization_id, workspace_id, asset_id, base_version_id, title)
			SELECT a.organization_id, a.workspace_id, a.id, (SELECT id FROM version_b), 'Roundtrip B' FROM asset_b a RETURNING id
		), link_a AS (
			UPDATE asset.assets a SET current_working_version_id = (SELECT id FROM version_a), draft_id = (SELECT id FROM draft_a)
			WHERE a.id = (SELECT asset_id FROM version_a)
		), link_b AS (
			UPDATE asset.assets a SET current_working_version_id = (SELECT id FROM version_b), draft_id = (SELECT id FROM draft_b)
			WHERE a.id = (SELECT asset_id FROM version_b)
		), vt_a AS (
			INSERT INTO asset.asset_version_tags (organization_id, workspace_id, asset_version_id, tag_id, source, created_by)
			SELECT v.organization_id, v.workspace_id, v.id, t.id, 'manual', (SELECT id FROM member)
			FROM version_a v
			JOIN asset.tags t ON t.workspace_id = (SELECT id FROM ws_id) AND t.normalized_key IN ('release', 'cms 文档')
		), vt_b AS (
			INSERT INTO asset.asset_version_tags (organization_id, workspace_id, asset_version_id, tag_id, source, created_by)
			SELECT v.organization_id, v.workspace_id, v.id, t.id, 'manual', (SELECT id FROM member)
			FROM version_b v
			JOIN asset.tags t ON t.workspace_id = (SELECT id FROM ws_id) AND t.normalized_key IN ('release', 'archive-me')
		), batch AS (
			INSERT INTO asset.import_batches
				(organization_id, workspace_id, resource_model_id, resource_model_version_id, submitted_by,
				 source_name, source_checksum, unknown_tag_policy, status)
			SELECT organization_id, (SELECT id FROM ws_id), (SELECT id FROM model), (SELECT id FROM model_version),
			       (SELECT id FROM member), 'roundtrip-test', 'roundtrip-checksum', 'create', 'processing'
			FROM ws_id RETURNING id
		)
		SELECT o.id::text, m.id::text, w.id::text, mo.id::text, mv.id::text, b.id::text,
		       (SELECT id FROM tag_archived)
		FROM org o, member m, ws w, model mo, model_version mv, batch b
	`).Scan(&organizationID, &memberID, &workspaceID, &modelID, &modelVersionID, &batchID, &archivedTagID)
	if err != nil {
		t.Fatalf("seed fixture: %v", err)
	}

	// Export side: the worker's own query + batched tag key loader + cell
	// writer, i.e. exactly what processExport puts into the file.
	processor := TransferProcessor{Store: db, Events: events}
	query, args := exportAssetQuery(organizationID, workspaceID, modelID, map[string]any{}, tag.ResolvedFilter{})
	rows, err := db.Pool.Query(ctx, query, args...)
	if err != nil {
		t.Fatalf("run export query: %v", err)
	}
	type exportedRecord struct {
		Title     string
		VersionID string
		Keys      []string
	}
	exported := make([]exportedRecord, 0, 2)
	versionIDs := make([]string, 0, 2)
	for rows.Next() {
		var id, versionID, visibility, publication, origin string
		var title, markdown *string
		var fields []byte
		if err := rows.Scan(&id, &title, &markdown, &fields, &versionID, &visibility, &publication, &origin); err != nil {
			rows.Close()
			t.Fatalf("scan export row: %v", err)
		}
		exported = append(exported, exportedRecord{Title: derefString(title), VersionID: versionID})
		versionIDs = append(versionIDs, versionID)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate export rows: %v", err)
	}
	if len(exported) != 2 {
		t.Fatalf("expected both seeded assets in the export, got %d", len(exported))
	}
	keysByVersion, err := processor.loadExportTagKeys(ctx, organizationID, versionIDs)
	if err != nil {
		t.Fatalf("load export tag keys: %v", err)
	}
	for index := range exported {
		exported[index].Keys = decodeTagKeysCell(formatTagKeysCell(keysByVersion[exported[index].VersionID]))
	}
	var recordA, recordB exportedRecord
	for _, record := range exported {
		switch record.Title {
		case "Roundtrip A":
			recordA = record
		case "Roundtrip B":
			recordB = record
		}
	}
	if !equalStringSets(recordA.Keys, []string{"release", "cms 文档"}) {
		t.Fatalf("export cell for asset A must be the source tag set, got %v", recordA.Keys)
	}
	if !equalStringSets(recordB.Keys, []string{"release", "archive-me"}) {
		t.Fatalf("export cell for asset B must be the source tag set, got %v", recordB.Keys)
	}

	// Import side: replay the export cells as source rows, the way a CSV/JSONL
	// round trip feeds the worker. Row 1 also carries an unknown key (twice) to
	// prove auto-creation plus duplicate-key idempotency; row 2 proves the
	// archived key is restored; row 3 proves invalid keys reject the row.
	header := importBatchHeader{
		id:               batchID,
		organizationID:   organizationID,
		workspaceID:      workspaceID,
		modelID:          modelID,
		versionID:        modelVersionID,
		sourceName:       "roundtrip-test",
		submittedBy:      memberID,
		fieldSchema:      []byte("{}"),
		unknownTagPolicy: UnknownTagPolicyCreate,
	}
	buildImportRow := func(number int, title string, keys []string) pendingImportRow {
		sourceRow, err := json.Marshal(map[string]any{
			"title": title, "markdown": "replayed body", "tag_keys": keys,
		})
		if err != nil {
			t.Fatalf("marshal source row: %v", err)
		}
		var rowID string
		if err := db.Pool.QueryRow(ctx, `
			INSERT INTO asset.import_rows (import_batch_id, row_number, source_row, row_checksum)
			VALUES ($1::uuid, $2, $3::jsonb, $4) RETURNING id::text
		`, batchID, number, string(sourceRow), fmt.Sprintf("roundtrip-row-%d", number)).Scan(&rowID); err != nil {
			t.Fatalf("seed import row %d: %v", number, err)
		}
		return pendingImportRow{id: rowID, number: number, sourceRow: sourceRow}
	}
	row1 := buildImportRow(1, recordA.Title, append(append([]string{}, recordA.Keys...), "brand-new", "brand-new"))
	row2 := buildImportRow(2, recordB.Title, recordB.Keys)
	row3 := buildImportRow(3, "Broken Row", []string{"bad\x00key"})
	for _, row := range []pendingImportRow{row1, row2, row3} {
		if err := processor.processImportRow(ctx, header, row); err != nil {
			t.Fatalf("process import row %d: %v", row.number, err)
		}
	}

	// Row outcomes: two accepted, the invalid-key row rejected with the
	// tag_key_invalid finding and no asset written.
	type rowOutcome struct {
		Status    string
		Errors    string
		AssetID   *string
		VersionID *string
	}
	outcomes := make(map[int]rowOutcome)
	outcomeRows, err := db.Pool.Query(ctx, `
		SELECT row_number, status, errors::text, asset_id::text, version_id::text
		FROM asset.import_rows WHERE import_batch_id = $1::uuid ORDER BY row_number
	`, batchID)
	if err != nil {
		t.Fatalf("load row outcomes: %v", err)
	}
	for outcomeRows.Next() {
		var number int
		var outcome rowOutcome
		if err := outcomeRows.Scan(&number, &outcome.Status, &outcome.Errors, &outcome.AssetID, &outcome.VersionID); err != nil {
			outcomeRows.Close()
			t.Fatalf("scan row outcome: %v", err)
		}
		outcomes[number] = outcome
	}
	outcomeRows.Close()
	if err := outcomeRows.Err(); err != nil {
		t.Fatalf("iterate row outcomes: %v", err)
	}
	for number := 1; number <= 3; number++ {
		outcome, ok := outcomes[number]
		if !ok {
			t.Fatalf("import row %d missing from outcomes", number)
		}
		switch number {
		case 1, 2:
			if outcome.Status != "accepted" || outcome.AssetID == nil || outcome.VersionID == nil {
				t.Fatalf("import row %d must be accepted with asset+version, got %+v", number, outcome)
			}
		case 3:
			if outcome.Status != "rejected" || outcome.AssetID != nil {
				t.Fatalf("import row 3 must be rejected without an asset, got %+v", outcome)
			}
			var findings []ImportRowError
			if err := json.Unmarshal([]byte(outcome.Errors), &findings); err != nil {
				t.Fatalf("decode row 3 findings: %v", err)
			}
			if len(findings) != 1 || findings[0].Code != "tag_key_invalid" {
				t.Fatalf("row 3 must carry the tag_key_invalid finding, got %+v", findings)
			}
		}
	}

	// Archived key restored to active by the replay; unknown key created
	// exactly once despite the duplicate entry; the broken row created nothing.
	var status string
	var archivedAtIsNull bool
	if err := db.Pool.QueryRow(ctx, `
		SELECT status, archived_at IS NULL FROM asset.tags
		WHERE organization_id = $1::uuid AND workspace_id = $2::uuid AND normalized_key = 'archive-me'
	`, organizationID, workspaceID).Scan(&status, &archivedAtIsNull); err != nil {
		t.Fatalf("load restored tag: %v", err)
	}
	if status != tag.StatusActive || !archivedAtIsNull {
		t.Fatalf("archived key must be restored to active, got status=%q archived_at_null=%v", status, archivedAtIsNull)
	}
	var brandNewCount int
	var brandNewDisplayName string
	if err := db.Pool.QueryRow(ctx, `
		SELECT count(*), COALESCE(max(display_name), '') FROM asset.tags
		WHERE organization_id = $1::uuid AND workspace_id = $2::uuid AND normalized_key = 'brand-new'
	`, organizationID, workspaceID).Scan(&brandNewCount, &brandNewDisplayName); err != nil {
		t.Fatalf("load created tag: %v", err)
	}
	if brandNewCount != 1 || brandNewDisplayName != "brand-new" {
		t.Fatalf("unknown key must be created exactly once with key as display name, got n=%d name=%q", brandNewCount, brandNewDisplayName)
	}
	var badKeyCount int
	if err := db.Pool.QueryRow(ctx, `
		SELECT count(*) FROM asset.tags
		WHERE organization_id = $1::uuid AND workspace_id = $2::uuid AND normalized_key LIKE 'bad%'
	`, organizationID, workspaceID).Scan(&badKeyCount); err != nil {
		t.Fatalf("count broken key tags: %v", err)
	}
	if badKeyCount != 0 {
		t.Fatalf("rejected row must not create tags, found %d", badKeyCount)
	}

	// The batch budget is charged the actual number of created tags (one).
	var createdTagCount int
	if err := db.Pool.QueryRow(ctx, `
		SELECT created_tag_count FROM asset.import_batches WHERE id = $1::uuid
	`, batchID).Scan(&createdTagCount); err != nil {
		t.Fatalf("load batch created_tag_count: %v", err)
	}
	if createdTagCount != 1 {
		t.Fatalf("created tag budget must be charged once, got %d", createdTagCount)
	}

	// Round trip equality: read the imported versions back through the same
	// export reader used for the source assets and compare tag sets.
	var newVersionA, newVersionB string
	if newVersionA = *outcomes[1].VersionID; newVersionA == "" {
		t.Fatal("row 1 version missing")
	}
	if newVersionB = *outcomes[2].VersionID; newVersionB == "" {
		t.Fatal("row 2 version missing")
	}
	importedKeys, err := processor.loadExportTagKeys(ctx, organizationID, []string{newVersionA, newVersionB})
	if err != nil {
		t.Fatalf("load imported tag keys: %v", err)
	}
	if !equalStringSets(importedKeys[newVersionA], []string{"release", "cms 文档", "brand-new"}) {
		t.Fatalf("imported version A tag set must equal the augmented source set, got %v", importedKeys[newVersionA])
	}
	if !equalStringSets(importedKeys[newVersionB], []string{"release", "archive-me"}) {
		t.Fatalf("imported version B tag set must equal the source set, got %v", importedKeys[newVersionB])
	}

	// Imported relations are stamped with the import source and the fresh
	// drafts inherited the version relations.
	var nonImportRelations int
	if err := db.Pool.QueryRow(ctx, `
		SELECT count(*) FROM asset.asset_version_tags
		WHERE asset_version_id = ANY($1::uuid[]) AND source <> $2
	`, []string{newVersionA, newVersionB}, tag.SourceImport).Scan(&nonImportRelations); err != nil {
		t.Fatalf("count relation sources: %v", err)
	}
	if nonImportRelations != 0 {
		t.Fatalf("imported relations must carry source=import, found %d others", nonImportRelations)
	}
	var draftTagCount int
	if err := db.Pool.QueryRow(ctx, `
		SELECT count(*) FROM asset.asset_draft_tags
		WHERE organization_id = $1::uuid
		  AND asset_draft_id IN (
			SELECT draft_id FROM asset.assets WHERE organization_id = $1::uuid AND id = ANY($2::uuid[])
		  )
	`, organizationID, []string{*outcomes[1].AssetID, *outcomes[2].AssetID}).Scan(&draftTagCount); err != nil {
		t.Fatalf("count draft tag relations: %v", err)
	}
	if draftTagCount != 5 {
		t.Fatalf("fresh drafts must inherit the version tag sets (3+2), got %d", draftTagCount)
	}
}
