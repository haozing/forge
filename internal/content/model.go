package content

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
)

const maxDepth = 32

var (
	ErrInvalidTree   = errors.New("invalid content tree")
	ErrVersionConflict = errors.New("version conflict")
)

type Block struct {
	ID         string
	Type       string
	Content    string
	Format     string
	Props      map[string]any
	ParentID   string
	Position   int
	Role       string
}

type Version struct {
	OrganizationID string
	ContainerID    string
	VersionID      string
	Blocks         []Block
}

func (v Version) Validate() error {
	if v.OrganizationID == "" || v.ContainerID == "" || v.VersionID == "" {
		return fmt.Errorf("%w: organization, container and version IDs are required", ErrInvalidTree)
	}
	seen := make(map[string]struct{}, len(v.Blocks))
	children := make(map[string]map[int]struct{})
	for _, block := range v.Blocks {
		if block.ID == "" || block.Type == "" || block.Position < 0 {
			return fmt.Errorf("%w: block ID, type and non-negative position are required", ErrInvalidTree)
		}
		if _, ok := seen[block.ID]; ok {
			return fmt.Errorf("%w: duplicate block %q", ErrInvalidTree, block.ID)
		}
		seen[block.ID] = struct{}{}
		key := block.ParentID
		if children[key] == nil {
			children[key] = make(map[int]struct{})
		}
		if _, ok := children[key][block.Position]; ok {
			return fmt.Errorf("%w: duplicate position %d under %q", ErrInvalidTree, block.Position, key)
		}
		children[key][block.Position] = struct{}{}
	}
	for _, block := range v.Blocks {
		if block.ParentID == "" {
			continue
		}
		if _, ok := seen[block.ParentID]; !ok {
			return fmt.Errorf("%w: parent %q is missing", ErrInvalidTree, block.ParentID)
		}
		if depth(v.Blocks, block.ID, seen) > maxDepth {
			return fmt.Errorf("%w: maximum depth is %d", ErrInvalidTree, maxDepth)
		}
	}
	return nil
}

func depth(blocks []Block, id string, seen map[string]struct{}) int {
	parents := make(map[string]string, len(blocks))
	for _, block := range blocks {
		parents[block.ID] = block.ParentID
	}
	depth := 1
	visited := map[string]struct{}{id: {}}
	for parent := parents[id]; parent != ""; parent = parents[parent] {
		if _, ok := visited[parent]; ok {
			return maxDepth + 1
		}
		visited[parent] = struct{}{}
		depth++
		if depth > maxDepth {
			return depth
		}
	}
	return depth
}

func (v Version) Checksum() (string, error) {
	if err := v.Validate(); err != nil {
		return "", err
	}
	blocks := append([]Block(nil), v.Blocks...)
	sort.Slice(blocks, func(i, j int) bool {
		if blocks[i].ParentID != blocks[j].ParentID {
			return blocks[i].ParentID < blocks[j].ParentID
		}
		if blocks[i].Position != blocks[j].Position {
			return blocks[i].Position < blocks[j].Position
		}
		return blocks[i].ID < blocks[j].ID
	})
	payload := struct {
		OrganizationID string  `json:"organization_id"`
		ContainerID    string  `json:"container_id"`
		Blocks         []Block `json:"blocks"`
	}{v.OrganizationID, v.ContainerID, blocks}
	data, err := json.Marshal(payload)
	if err != nil {
		return "", fmt.Errorf("encode content checksum: %w", err)
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]), nil
}
