package objectstore

import (
	"testing"
)

func TestNewOSSRequiresRegionAndBucket(t *testing.T) {
	if _, err := NewOSS(OSSConfig{Bucket: "bucket"}); err == nil {
		t.Fatal("expected missing region error")
	}
	if _, err := NewOSS(OSSConfig{Region: "cn-hangzhou"}); err == nil {
		t.Fatal("expected missing bucket error")
	}
	store, err := NewOSS(OSSConfig{Region: "cn-hangzhou", Bucket: "bucket", Prefix: "attachments/"})
	if err != nil || store.prefix != "attachments/" {
		t.Fatalf("expected valid OSS config, got store=%v err=%v", store, err)
	}
}

func TestOSSValidatesConfiguredPrefix(t *testing.T) {
	store := &OSS{bucket: "bucket", prefix: "attachments/"}
	for _, key := range []string{"attachments/asset-1.bin", "attachments/nested/file.pdf"} {
		if got, err := store.validateKey(key); err != nil || got != key {
			t.Fatalf("validateKey(%q) = %q, %v", key, got, err)
		}
	}
	for _, key := range []string{"asset-1.bin", "attachments/../secret", "/attachments/file", `attachments\\file`} {
		if _, err := store.validateKey(key); err == nil {
			t.Fatalf("validateKey(%q) should fail", key)
		}
	}
}

func TestNormalizePrefix(t *testing.T) {
	for _, input := range []string{"attachments", "attachments/"} {
		got, err := normalizePrefix(input)
		if err != nil || got != "attachments/" {
			t.Fatalf("normalizePrefix(%q) = %q, %v", input, got, err)
		}
	}
	if _, err := normalizePrefix("../attachments"); err == nil {
		t.Fatal("expected invalid prefix")
	}
}

var _ ObjectStore = (*OSS)(nil)
