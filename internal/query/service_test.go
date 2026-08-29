package query

import (
	"context"
	"errors"
	"testing"

	"agentchunzhi/internal/auth"
)

func TestValidUUID(t *testing.T) {
	if !ValidUUID("00000000-0000-4000-8000-000000000001") {
		t.Fatalf("expected valid UUID")
	}
	if ValidUUID("not-a-uuid") {
		t.Fatalf("expected invalid UUID")
	}
}

func TestQueryRejectsEmptyQuery(t *testing.T) {
	_, err := (Service{}).Query(context.Background(), auth.Principal{}, QueryRequest{Mode: "fulltext"}, []string{"model"})
	if err == nil || !errors.Is(err, ErrInvalidQuery) {
		t.Fatalf("expected invalid query, got %v", err)
	}
}

func TestQueryRequiresModelScope(t *testing.T) {
	_, err := (Service{}).Query(context.Background(), auth.Principal{}, QueryRequest{Mode: "fulltext", Query: "hello"}, nil)
	if err != ErrModelAccessDenied {
		t.Fatalf("expected model access denied, got %v", err)
	}
}

func TestReferenceRejectsInvalidAssetOrEmptyScope(t *testing.T) {
	if _, err := (Service{}).Reference(context.Background(), auth.Principal{}, "not-an-asset", []string{"model"}); !errors.Is(err, ErrReferenceNotFound) {
		t.Fatalf("expected invalid asset to be hidden, got %v", err)
	}
	if _, err := (Service{}).Reference(context.Background(), auth.Principal{}, "00000000-0000-4000-8000-000000000001", nil); !errors.Is(err, ErrReferenceNotFound) {
		t.Fatalf("expected empty scope to be hidden, got %v", err)
	}
}

func TestCursorIsSigned(t *testing.T) {
	service := Service{CursorSecret: "test-secret"}
	cursor := service.encodeCursor(cursorPayload{SessionID: "00000000-0000-4000-8000-000000000001", Ordinal: 4})
	decoded, err := service.decodeCursor(cursor)
	if err != nil || decoded.Ordinal != 4 {
		t.Fatalf("decode cursor = %#v, %v", decoded, err)
	}
	tampered := "A" + cursor[1:]
	if _, err := service.decodeCursor(tampered); err == nil {
		t.Fatal("tampered cursor should be rejected")
	}
}

func TestMemberQueryEnums(t *testing.T) {
	if err := validateMemberQueryEnums([]string{"workspace", "organization", "public"}, []string{"draft", "published"}); err != nil {
		t.Fatalf("valid enum filters rejected: %v", err)
	}
	if err := validateMemberQueryEnums([]string{"private"}, nil); !errors.Is(err, ErrInvalidQuery) {
		t.Fatalf("legacy private visibility should be rejected, got %v", err)
	}
	if err := validateMemberQueryEnums([]string{"login"}, nil); !errors.Is(err, ErrInvalidQuery) {
		t.Fatalf("legacy login visibility should be rejected, got %v", err)
	}
	if err := validateMemberQueryEnums([]string{"unknown"}, nil); !errors.Is(err, ErrInvalidQuery) {
		t.Fatalf("invalid visibility error = %v", err)
	}
	if err := validateMemberQueryEnums(nil, []string{"deleted"}); !errors.Is(err, ErrInvalidQuery) {
		t.Fatalf("invalid publication status error = %v", err)
	}
}

func TestDeduplicateMemberAssetsKeepsHighestRankedChunk(t *testing.T) {
	items := deduplicateMemberAssets([]candidate{
		{AssetID: "asset-1", ChunkID: "best"},
		{AssetID: "asset-2", ChunkID: "only"},
		{AssetID: "asset-1", ChunkID: "worse"},
	})
	if len(items) != 2 || items[0].ChunkID != "best" || items[1].ChunkID != "only" {
		t.Fatalf("deduplicated items = %#v", items)
	}
}
