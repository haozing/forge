package automation

import (
	"testing"
	"time"
)

func TestBackoffDurationUsesExponentialCap(t *testing.T) {
	config := map[string]any{"base_seconds": float64(2), "max_seconds": float64(7)}
	checks := []time.Duration{2 * time.Second, 4 * time.Second, 7 * time.Second, 7 * time.Second}
	for attempt, want := range checks {
		if got := backoffDuration(config, attempt+1); got != want {
			t.Fatalf("attempt %d: got %s, want %s", attempt+1, got, want)
		}
	}
}

func TestIntervalLiteralHasPositiveSeconds(t *testing.T) {
	if got := intervalLiteral(0); got != "1 seconds" {
		t.Fatalf("got %q", got)
	}
	if got := intervalLiteral(3 * time.Second); got != "3 seconds" {
		t.Fatalf("got %q", got)
	}
}

func TestSupportedAutomationOperationsAreExplicit(t *testing.T) {
	for _, operation := range []string{"prepare_asset", "publish", "archive", "reindex", "import", "export", "transcribe", "sync_note"} {
		if _, ok := supportedOperations[operation]; !ok {
			t.Fatalf("operation %q is not registered", operation)
		}
	}
	if _, ok := supportedOperations["arbitrary_code"]; ok {
		t.Fatal("arbitrary operation must not be accepted")
	}
}
