package review

import (
	"errors"
	"testing"
)

func TestRequestStatusAndCancelReasonContract(t *testing.T) {
	for _, status := range []string{RequestPending, RequestApproved, RequestRejected, RequestCancelled} {
		if !ValidRequestStatus(status) {
			t.Fatalf("status %s must be valid", status)
		}
	}
	for _, legacy := range []string{"superseded", "closed", ""} {
		if ValidRequestStatus(legacy) {
			t.Fatalf("status %q must be rejected", legacy)
		}
	}
	for _, reason := range []string{CancelUserCancelled, CancelNewVersion, CancelAssetArchived, CancelAdminCancelled} {
		if !ValidCancelReason(reason) {
			t.Fatalf("cancel reason %s must be valid", reason)
		}
	}
	if ValidCancelReason("superseded") {
		t.Fatal("legacy cancel reason must be rejected")
	}
}

func TestBatchResultIsolatesFailures(t *testing.T) {
	result := BatchResult{Items: []BatchItemResult{
		{RequestID: "r1", OK: true, Request: &Request{ID: "r1", Status: RequestApproved}},
		{RequestID: "r2", OK: false, ErrorCode: "publication_request_not_pending"},
	}}
	if len(result.Items) != 2 || result.Items[0].OK == result.Items[1].OK {
		t.Fatal("batch results must be per-item")
	}
	if result.Items[1].ErrorCode == "" {
		t.Fatal("failed items must carry a stable error code")
	}
}

func TestSentinelErrorsAreDistinct(t *testing.T) {
	if errors.Is(ErrSelfApproval, ErrConflict) {
		t.Fatal("self approval is a distinct invariant, not a generic conflict")
	}
	if errors.Is(ErrVersionSuperseded, ErrConflict) {
		t.Fatal("superseded request is a distinct invariant")
	}
}
