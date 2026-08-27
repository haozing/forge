package modelendpoint

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestProbeOutcomeNeverDecidesStatus(t *testing.T) {
	ok, detail, code := probeOutcome(nil)
	if !ok || detail != "" || code != "" {
		t.Fatalf("healthy probe should carry no telemetry error: ok=%v detail=%q code=%q", ok, detail, code)
	}
	failure := errors.New("dial tcp: connect refused")
	ok, detail, code = probeOutcome(failure)
	if ok {
		t.Fatal("failed probe must report ok=false")
	}
	if detail != failure.Error() {
		t.Fatalf("failure detail should surface the check error, got %q", detail)
	}
	if code != HealthCheckFailedCode {
		t.Fatalf("failed probe should record %q telemetry, got %q", HealthCheckFailedCode, code)
	}
}

func TestEnableWarnsOnUnverifiedOrUnhealthyOnly(t *testing.T) {
	cases := []struct {
		name            string
		verified        *time.Time
		healthErrorCode string
		wantWarning     string
	}{
		{"never verified", nil, "", WarningEnableUnverified},
		{"verified but last check failed", timeOf(time.Now().UTC()), HealthCheckFailedCode, WarningEnableUnverified},
		{"verified and healthy", timeOf(time.Now().UTC()), "", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := enableWarning(tc.verified, tc.healthErrorCode); got != tc.wantWarning {
				t.Fatalf("enableWarning = %q, want %q", got, tc.wantWarning)
			}
		})
	}
}

func timeOf(value time.Time) *time.Time { return &value }

func TestStatusResultJSONFlattensEndpointAndKeepsWarningOptional(t *testing.T) {
	warned := StatusResult{Endpoint: Endpoint{ID: "ep-1", Name: "ITC-Endpoint", Status: "active"}, Warning: WarningEnableUnverified}
	payload, err := json.Marshal(warned)
	if err != nil {
		t.Fatal(err)
	}
	text := string(payload)
	for _, fragment := range []string{`"id":"ep-1"`, `"name":"ITC-Endpoint"`, `"status":"active"`, `"warning":"` + WarningEnableUnverified + `"`} {
		if !strings.Contains(text, fragment) {
			t.Fatalf("status result payload missing %s: %s", fragment, text)
		}
	}

	silent := StatusResult{Endpoint: Endpoint{ID: "ep-2", Name: "ITC-Endpoint-2", Status: "disabled"}}
	payload, err = json.Marshal(silent)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(payload), "warning") {
		t.Fatalf("disable without warning must omit the field: %s", payload)
	}
}

func TestProbeResultJSONCarriesOutcomeWithoutStatusMutationContract(t *testing.T) {
	item := Endpoint{ID: "ep-3", Name: "ITC-Endpoint-3", Status: "enabled-by-admin"}
	failed := ProbeResult{OK: false, Detail: "external LLM unreachable", Endpoint: item}
	payload, err := json.Marshal(failed)
	if err != nil {
		t.Fatal(err)
	}
	text := string(payload)
	for _, fragment := range []string{`"ok":false`, `"detail":"external LLM unreachable"`, `"id":"ep-3"`, `"status":"enabled-by-admin"`} {
		if !strings.Contains(text, fragment) {
			t.Fatalf("probe result payload missing %s: %s", fragment, text)
		}
	}

	succeeded := ProbeResult{OK: true, Endpoint: item}
	payload, err = json.Marshal(succeeded)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(payload), `"detail"`) {
		t.Fatalf("successful probe must omit detail: %s", payload)
	}
	if strings.Contains(string(payload), `"ok":false`) || !strings.Contains(string(payload), `"ok":true`) {
		t.Fatalf("successful probe must set ok=true: %s", payload)
	}
}
