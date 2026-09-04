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

// Block is one positioned node of the live tree. Props is the raw jsonb of
// the revision; the resolved fields (Level, AttachmentID/Alt/Caption) are
// derived from it by loadBlocks and ResolveImageAlts so rendering and
// snapshotting never re-parse props.
type Block struct {
	Position     float64 `json:"position"`
	RevisionID   string  `json:"block_revision_id"`
	Kind         string  `json:"kind"`
	Content      string  `json:"content"`
	Props        string  `json:"-"`                       // raw props jsonb of the revision
	Level        int     `json:"level,omitempty"`         // heading level (props.level, default 3)
	AttachmentID string  `json:"attachment_id,omitempty"` // image blocks, resolved
	Alt          string  `json:"alt,omitempty"`           // image blocks, resolved final alt
	Caption      string  `json:"caption,omitempty"`       // image blocks, resolved
}

// ImageProps is the closed props vocabulary of an image block. The block
// stores a reference, never the file: the image lives in the attachment row
// plus its object-storage object, while alt/caption travel with the block so
// the same image can wear different words in different notes.
type ImageProps struct {
	AttachmentID string `json:"attachment_id"`
	Alt          string `json:"alt"`
	Caption      string `json:"caption"`
}

// ParseImageProps decodes the props of one image block.
func ParseImageProps(props string) ImageProps {
	var parsed ImageProps
	if props != "" {
		_ = json.Unmarshal([]byte(props), &parsed)
	}
	return parsed
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
		SELECT bp.position, br.id::text, b.block_type, br.content, br.props::text
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
		if err := rows.Scan(&block.Position, &block.RevisionID, &block.Kind, &block.Content, &block.Props); err != nil {
			return nil, fmt.Errorf("scan note block: %w", err)
		}
		if block.Kind == "heading" {
			block.Level = headingLevel(block.Props)
		}
		blocks = append(blocks, block)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate note blocks: %w", err)
	}
	sort.SliceStable(blocks, func(i, j int) bool { return blocks[i].Position < blocks[j].Position })
	return blocks, nil
}

// headingLevel parses props.level of a heading block; absent or out-of-range
// levels fall back to 3, matching the pre-level renderer output.
func headingLevel(props string) int {
	var parsed struct {
		Level int `json:"level"`
	}
	if props != "" {
		_ = json.Unmarshal([]byte(props), &parsed)
	}
	if parsed.Level >= 1 && parsed.Level <= 6 {
		return parsed.Level
	}
	return 3
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
// conversation messages never do. Image blocks render the neutral
// chunzhi-media:// reference — the public renderer rewrites it to the
// same-origin media route; the stored markdown never carries a URL.
func RenderMarkdown(tree Tree) string {
	var builder strings.Builder
	for _, block := range tree.Blocks {
		switch block.Kind {
		case "heading":
			level := block.Level
			if level < 1 || level > 6 {
				level = 3
			}
			builder.WriteString(strings.Repeat("#", level) + " ")
			builder.WriteString(strings.TrimSpace(block.Content))
			builder.WriteString("\n\n")
		case "image":
			// Unresolved references (props without attachment_id) render
			// nothing: the freeze never carries a half-specified image.
			if block.AttachmentID == "" {
				continue
			}
			builder.WriteString("![" + mdImageText(block.Alt) + "](chunzhi-media://" + block.AttachmentID)
			if caption := mdImageText(block.Caption); caption != "" {
				builder.WriteString(" \"" + caption + "\"")
			}
			builder.WriteString(")\n\n")
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

// mdImageText sanitizes alt/caption text for the frozen markdown: newlines
// collapse and bracket/quote characters that would break the image syntax
// are dropped.
func mdImageText(value string) string {
	replacer := strings.NewReplacer("\r", "", "\n", " ", "[", "", "]", "", "\"", "'")
	return strings.TrimSpace(replacer.Replace(value))
}

// ResolveImageAlts resolves the rendering fields of every image block in the
// tree: the attachment reference comes from props and the final alt is the
// block-level alt when set, otherwise the attachment's vision-generated
// default. Resolution happens once before render/snapshot so the frozen
// markdown carries the final alt and never re-derives it. Blocks referencing
// missing attachments resolve to no alt; strict commit-time validation is
// the freeze path's job.
func ResolveImageAlts(ctx context.Context, q Querier, organizationID string, tree Tree) error {
	ids := ImageAttachmentIDs(tree)
	if len(ids) == 0 {
		return nil
	}
	defaultAlt := map[string]string{}
	rows, err := q.Query(ctx, `
		SELECT id::text, default_alt_text FROM asset.attachments
		WHERE organization_id = $1::uuid AND id = ANY($2::uuid[])
	`, organizationID, ids)
	if err != nil {
		return fmt.Errorf("load image default alts: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var attachmentID, alt string
		if err := rows.Scan(&attachmentID, &alt); err != nil {
			return fmt.Errorf("scan image default alt: %w", err)
		}
		defaultAlt[attachmentID] = alt
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterate image default alts: %w", err)
	}
	for index := range tree.Blocks {
		block := &tree.Blocks[index]
		if block.Kind != "image" {
			continue
		}
		props := ParseImageProps(block.Props)
		if props.AttachmentID == "" {
			continue
		}
		block.AttachmentID = props.AttachmentID
		block.Alt = props.Alt
		if block.Alt == "" {
			block.Alt = defaultAlt[props.AttachmentID]
		}
		block.Caption = props.Caption
	}
	return nil
}

// ImageAttachmentIDs returns the distinct attachment references of the
// tree's image blocks in stable order.
func ImageAttachmentIDs(tree Tree) []string {
	seen := map[string]bool{}
	ids := []string{}
	for _, block := range tree.Blocks {
		if block.Kind != "image" {
			continue
		}
		props := ParseImageProps(block.Props)
		if props.AttachmentID == "" || seen[props.AttachmentID] {
			continue
		}
		seen[props.AttachmentID] = true
		ids = append(ids, props.AttachmentID)
	}
	return ids
}

// Snapshot is the reference-style frozen tree: positions and pointers only,
// block bodies stay in the immutable block revisions. Image blocks carry
// their resolved reference fields so replay reproduces the frozen markdown
// byte for byte even after the attachment's default alt changes.
type Snapshot struct {
	ContainerID string          `json:"container_id"`
	Revision    int64           `json:"revision"`
	Blocks      []SnapshotEntry `json:"blocks"`
}

// SnapshotEntry locates one frozen block.
type SnapshotEntry struct {
	Position     float64 `json:"position"`
	RevisionID   string  `json:"block_revision_id"`
	Kind         string  `json:"kind"`
	AttachmentID string  `json:"attachment_id,omitempty"`
	Alt          string  `json:"alt,omitempty"`
	Caption      string  `json:"caption,omitempty"`
}

// SnapshotJSON renders the tree into the asset_versions.blocks payload.
func SnapshotJSON(tree Tree) ([]byte, error) {
	snapshot := Snapshot{ContainerID: tree.ContainerID, Revision: tree.Revision, Blocks: make([]SnapshotEntry, 0, len(tree.Blocks))}
	for _, block := range tree.Blocks {
		entry := SnapshotEntry{Position: block.Position, RevisionID: block.RevisionID, Kind: block.Kind}
		if block.Kind == "image" {
			entry.AttachmentID, entry.Alt, entry.Caption = block.AttachmentID, block.Alt, block.Caption
		}
		snapshot.Blocks = append(snapshot.Blocks, entry)
	}
	return json.Marshal(snapshot)
}

// ReplayMarkdown renders a frozen snapshot back into markdown by resolving
// the recorded block revisions. Image blocks render from the resolved
// reference fields sealed into the snapshot; heading levels re-read the
// immutable revision props. Used to prove version determinism and to serve
// traceability views; it never consults the live tree.
func ReplayMarkdown(ctx context.Context, tx Querier, organizationID string, snapshot Snapshot) (string, error) {
	blocks := make([]Block, 0, len(snapshot.Blocks))
	for _, entry := range snapshot.Blocks {
		block := Block{
			Position: entry.Position, RevisionID: entry.RevisionID, Kind: entry.Kind,
			AttachmentID: entry.AttachmentID, Alt: entry.Alt, Caption: entry.Caption,
		}
		if entry.Kind != "image" || entry.AttachmentID == "" {
			var content, props string
			err := tx.QueryRow(ctx, `
				SELECT br.content, br.props::text
				FROM content.block_revisions br
				WHERE br.organization_id = $1::uuid AND br.id = $2::uuid
			`, organizationID, entry.RevisionID).Scan(&content, &props)
			if err != nil {
				return "", fmt.Errorf("replay block revision: %w", err)
			}
			block.Content, block.Props = content, props
			if entry.Kind == "heading" {
				block.Level = headingLevel(props)
			}
		}
		blocks = append(blocks, block)
	}
	sort.SliceStable(blocks, func(i, j int) bool { return blocks[i].Position < blocks[j].Position })
	return RenderMarkdown(Tree{Blocks: blocks}), nil
}
