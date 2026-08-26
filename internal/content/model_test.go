package content

import (
	"errors"
	"testing"
)

func TestVersionChecksumIsDeterministic(t *testing.T) {
	first := Version{
		OrganizationID: "org",
		ContainerID:    "container",
		VersionID:      "version",
		Blocks: []Block{
			{ID: "child", Type: "paragraph", ParentID: "root", Position: 0, Content: "child"},
			{ID: "root", Type: "heading", Position: 0, Content: "title"},
		},
	}
	second := first
	second.Blocks = []Block{first.Blocks[1], first.Blocks[0]}
	a, err := first.Checksum()
	if err != nil {
		t.Fatal(err)
	}
	b, err := second.Checksum()
	if err != nil {
		t.Fatal(err)
	}
	if a != b {
		t.Fatalf("expected stable checksum, got %s and %s", a, b)
	}
}

func TestVersionRejectsInvalidParentAndPosition(t *testing.T) {
	version := Version{OrganizationID: "org", ContainerID: "container", VersionID: "version", Blocks: []Block{
		{ID: "a", Type: "paragraph", ParentID: "missing", Position: -1},
	}}
	if err := version.Validate(); !errors.Is(err, ErrInvalidTree) {
		t.Fatalf("expected invalid tree, got %v", err)
	}
}

func TestVersionRejectsDuplicateSiblingPositions(t *testing.T) {
	version := Version{OrganizationID: "org", ContainerID: "container", VersionID: "version", Blocks: []Block{
		{ID: "a", Type: "paragraph", Position: 0},
		{ID: "b", Type: "paragraph", Position: 0},
	}}
	if err := version.Validate(); !errors.Is(err, ErrInvalidTree) {
		t.Fatalf("expected duplicate position error, got %v", err)
	}
}
