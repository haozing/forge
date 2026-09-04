// Package noteblocks is the read/render side of the live note block tree.
// One conversation owns one note document (content.containers, kind='note',
// asset_id set). Chat messages are NOT part of the tree: they live only in
// message_blocks as an immutable transcript, while the tree holds what the
// member deliberately put into the note — manual blocks and saved message
// excerpts. Markdown is a derived artifact — this package is the only place
// that freezes a tree into the deterministic markdown stored on immutable
// asset versions, together with the reference-style snapshot that lets a
// version be replayed without embedding block bodies.
package noteblocks

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/jackc/pgx/v5"
)

// Querier is the subset of pgx.Tx the tree helpers need; *pgxpool.Pool and
// pgx.Tx both satisfy it, so read-only views can run without a transaction.
type Querier interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
}

// Block is one positioned node of the live tree.
type Block struct {
	Position   float64 `json:"position"`
	RevisionID string  `json:"block_revision_id"`
	Kind       string  `json:"kind"`
	Content    string  `json:"content"`
}

// Tree is the loaded live tree of one note document.
type Tree struct {
	ContainerID string  `json:"container_id"`
	Revision    int64   `json:"revision"`
	Blocks      []Block `json:"blocks"`
}

// LoadTreeTx reads the live tree FOR UPDATE on the container row so concurrent
// appends and freezes serialize.
func LoadTreeTx(ctx context.Context, tx Querier, organizationID, containerID string) (Tree, error) {
	var tree Tree
	var revision int64
	err := tx.QueryRow(ctx, `
		SELECT id::text, revision FROM content.containers
		WHERE organization_id = $1::uuid AND id = $2::uuid AND kind = 'note'
		FOR UPDATE
	`, organizationID, containerID).Scan(&tree.ContainerID, &revision)
	if err != nil {
		return Tree{}, fmt.Errorf("lock note tree: %w", err)
	}
	tree.Revision = revision
	blocks, err := loadBlocks(ctx, tx, organizationID, containerID)
	if err != nil {
		return Tree{}, err
	}
	tree.Blocks = blocks
	return tree, nil
}

// LoadTreeByAssetTx resolves the note document container of an asset and
// loads its live tree. hasContainer=false means the asset is not a
// conversation note (form-authored asset) and the caller should fall back to
// the draft path.
func LoadTreeByAssetTx(ctx context.Context, tx Querier, organizationID, assetID string) (Tree, bool, error) {
	var containerID string
	err := tx.QueryRow(ctx, `
		SELECT id::text FROM content.containers
		WHERE organization_id = $1::uuid AND asset_id = $2::uuid AND kind = 'note'
		FOR UPDATE
	`, organizationID, assetID).Scan(&containerID)
	if err != nil {
		if err == pgx.ErrNoRows {
			return Tree{}, false, nil
		}
		return Tree{}, false, fmt.Errorf("resolve note container: %w", err)
	}
	tree, err := LoadTreeTx(ctx, tx, organizationID, containerID)
	return tree, true, err
}

// LoadTreeByAssetView is the lock-free read for composed views; tree writers
// take the FOR UPDATE path instead.
func LoadTreeByAssetView(ctx context.Context, q Querier, organizationID, assetID string) (Tree, bool, error) {
	var containerID string
	var revision int64
	err := q.QueryRow(ctx, `
		SELECT id::text, revision FROM content.containers
		WHERE organization_id = $1::uuid AND asset_id = $2::uuid AND kind = 'note'
	`, organizationID, assetID).Scan(&containerID, &revision)
	if err != nil {
		if err == pgx.ErrNoRows {
			return Tree{}, false, nil
		}
		return Tree{}, false, fmt.Errorf("resolve note container: %w", err)
	}
	blocks, err := loadBlocks(ctx, q, organizationID, containerID)
	if err != nil {
		return Tree{}, false, err
	}
	return Tree{ContainerID: containerID, Revision: revision, Blocks: blocks}, true, nil
}

func loadBlocks(ctx context.Context, tx Querier, organizationID, containerID string) ([]Block, error) {
	rows, err := tx.Query(ctx, `
		SELECT bp.position, br.id::text, b.block_type, br.content
		FROM content.block_placements bp
		JOIN content.block_revisions br ON br.organization_id = bp.organization_id AND br.id = bp.block_revision_id
		JOIN content.blocks b ON b.organization_id = br.organization_id AND b.id = br.block_id
		WHERE bp.organization_id = $1::uuid AND bp.container_id = $2::uuid
	`, organizationID, containerID)
	if err != nil {
		return nil, fmt.Errorf("load note blocks: %w", err)
	}
	defer rows.Close()
	blocks := []Block{}
	for rows.Next() {
		var block Block
		if err := rows.Scan(&block.Position, &block.RevisionID, &block.Kind, &block.Content); err != nil {
			return nil, fmt.Errorf("scan note block: %w", err)
		}
		blocks = append(blocks, block)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate note blocks: %w", err)
	}
	sort.SliceStable(blocks, func(i, j int) bool { return blocks[i].Position < blocks[j].Position })
	return blocks, nil
}

// AppendPosition returns the next append position of the tree.
func (t Tree) AppendPosition() float64 {
	if len(t.Blocks) == 0 {
		return 0
	}
	return t.Blocks[len(t.Blocks)-1].Position + 1
}

// RenderMarkdown freezes the tree into deterministic markdown. Only note
// content (manual blocks and saved excerpts) ever reaches a frozen artifact;
// conversation messages never do.
func RenderMarkdown(tree Tree) string {
	var builder strings.Builder
	for _, block := range tree.Blocks {
		switch block.Kind {
		case "heading":
			builder.WriteString("### ")
			builder.WriteString(strings.TrimSpace(block.Content))
			builder.WriteString("\n\n")
		case "quote":
			for _, line := range strings.Split(strings.TrimRight(block.Content, "\n"), "\n") {
				builder.WriteString("> ")
				builder.WriteString(line)
				builder.WriteString("\n")
			}
			builder.WriteString("\n")
		case "code":
			builder.WriteString("```\n")
			builder.WriteString(strings.TrimRight(block.Content, "\n"))
			builder.WriteString("\n```\n\n")
		case "list":
			for _, line := range strings.Split(strings.TrimSpace(block.Content), "\n") {
				if strings.TrimSpace(line) == "" {
					continue
				}
				builder.WriteString("- ")
				builder.WriteString(strings.TrimSpace(line))
				builder.WriteString("\n")
			}
			builder.WriteString("\n")
		case "callout":
			builder.WriteString("> [!NOTE]\n")
			builder.WriteString("> ")
			builder.WriteString(strings.TrimSpace(block.Content))
			builder.WriteString("\n\n")
		default: // paragraph, text, link, attachment, qa pairs render verbatim
			builder.WriteString(strings.TrimRight(block.Content, "\n"))
			builder.WriteString("\n\n")
		}
	}
	return builder.String()
}

// Snapshot is the reference-style frozen tree: positions and pointers only,
// block bodies stay in the immutable block revisions.
type Snapshot struct {
	ContainerID string          `json:"container_id"`
	Revision    int64           `json:"revision"`
	Blocks      []SnapshotEntry `json:"blocks"`
}

// SnapshotEntry locates one frozen block.
type SnapshotEntry struct {
	Position   float64 `json:"position"`
	RevisionID string  `json:"block_revision_id"`
	Kind       string  `json:"kind"`
}

// SnapshotJSON renders the tree into the asset_versions.blocks payload.
func SnapshotJSON(tree Tree) ([]byte, error) {
	snapshot := Snapshot{ContainerID: tree.ContainerID, Revision: tree.Revision, Blocks: make([]SnapshotEntry, 0, len(tree.Blocks))}
	for _, block := range tree.Blocks {
		snapshot.Blocks = append(snapshot.Blocks, SnapshotEntry{
			Position: block.Position, RevisionID: block.RevisionID, Kind: block.Kind,
		})
	}
	return json.Marshal(snapshot)
}

// ReplayMarkdown renders a frozen snapshot back into markdown by resolving
// the recorded block revisions. Used to prove version determinism and to
// serve traceability views; it never consults the live tree.
func ReplayMarkdown(ctx context.Context, tx Querier, organizationID string, snapshot Snapshot) (string, error) {
	blocks := make([]Block, 0, len(snapshot.Blocks))
	for _, entry := range snapshot.Blocks {
		var content string
		err := tx.QueryRow(ctx, `
			SELECT br.content
			FROM content.block_revisions br
			WHERE br.organization_id = $1::uuid AND br.id = $2::uuid
		`, organizationID, entry.RevisionID).Scan(&content)
		if err != nil {
			return "", fmt.Errorf("replay block revision: %w", err)
		}
		blocks = append(blocks, Block{
			Position: entry.Position, RevisionID: entry.RevisionID, Kind: entry.Kind, Content: content,
		})
	}
	sort.SliceStable(blocks, func(i, j int) bool { return blocks[i].Position < blocks[j].Position })
	return RenderMarkdown(Tree{Blocks: blocks}), nil
}
