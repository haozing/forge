package attachment

import (
	"context"
	"testing"

	"agentchunzhi/internal/auth"
)

func TestCleanFilename(t *testing.T) {
	got, err := cleanFilename(`folder\\中文文档.pdf`)
	if err != nil || got != "中文文档.pdf" {
		t.Fatalf("cleanFilename = %q, %v", got, err)
	}
	if _, err := cleanFilename("bad\x00name.txt"); err != ErrInvalidUpload {
		t.Fatalf("expected invalid filename, got %v", err)
	}
}

func TestCleanMediaType(t *testing.T) {
	got, err := cleanMediaType("text/plain; charset=utf-8", "note.txt")
	if err != nil || got != "text/plain" {
		t.Fatalf("cleanMediaType = %q, %v", got, err)
	}
	got, err = cleanMediaType("", "photo.png")
	if err != nil || got != "image/png" {
		t.Fatalf("extension media type = %q, %v", got, err)
	}
}

func TestBuildObjectKey(t *testing.T) {
	if got := buildObjectKey("attachments/", "org", "attachment"); got != "attachments/org/attachment" {
		t.Fatalf("object key = %q", got)
	}
}

func TestOpenDownloadRejectsEmptyScopeBeforeObjectAccess(t *testing.T) {
	service := Service{}
	_, err := service.OpenDownload(context.Background(), auth.Principal{}, "attachment-id", nil, "frontend")
	if err != ErrNotFound {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}
