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

func TestOpenDownloadRejectsInvalidAttachmentIDBeforeObjectAccess(t *testing.T) {
	service := Service{}
	if _, err := service.OpenDownload(context.Background(), auth.Principal{}, "not-a-uuid"); err != ErrNotFound {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
	if _, err := service.Upload(context.Background(), auth.Principal{}, "not-a-uuid", "file.txt", "text/plain", 1, nil); err != ErrInvalidUpload {
		t.Fatalf("expected ErrInvalidUpload for invalid workspace, got %v", err)
	}
}
