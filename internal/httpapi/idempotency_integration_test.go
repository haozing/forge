package httpapi

// idempotency_integration_test.go — middleware-level replay regression tests.
// They run only when AGENTCHUNZHI_TEST_DATABASE_URL points at a migrated
// database (same gate as internal/agentruntime/checkpoint/postgres_test.go).

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"

	"agentchunzhi/internal/auth"
	"agentchunzhi/internal/store"

	"github.com/google/uuid"
)

const idempotencyTestPath = "/api/workspaces/00000000-0000-4000-8000-00000000000f/assets"

// idempotencyTestScope provisions a live member session and removes every
// idempotency_keys row the test reserved.
type idempotencyTestScope struct {
	database  *store.Store
	deps      Dependencies
	token     string
	tokenHash string
	keys      []string
}

func newIdempotencyTestScope(t *testing.T) *idempotencyTestScope {
	t.Helper()
	databaseURL := os.Getenv("AGENTCHUNZHI_TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("AGENTCHUNZHI_TEST_DATABASE_URL is not set")
	}
	ctx := context.Background()
	database, err := store.Open(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(database.Close)
	var organizationID, userID string
	if err := database.Pool.QueryRow(ctx, `
		SELECT u.organization_id::text, u.id::text
		FROM identity.users u
		JOIN organization.organizations o ON o.id = u.organization_id
		WHERE u.user_type = 'member' AND u.status = 'active' AND o.status = 'active'
		ORDER BY u.created_at
		LIMIT 1
	`).Scan(&organizationID, &userID); err != nil {
		t.Fatalf("load idempotency test scope: %v", err)
	}
	token := uuid.NewString() + uuid.NewString()
	sum := sha256.Sum256([]byte(token))
	tokenHash := hex.EncodeToString(sum[:])
	if _, err := database.Pool.Exec(ctx, `
		INSERT INTO identity.sessions (user_id, token_hash, idle_expires_at, absolute_expires_at)
		VALUES ($1::uuid, $2, now() + interval '1 hour', now() + interval '1 day')
	`, userID, tokenHash); err != nil {
		t.Fatalf("seed idempotency test session: %v", err)
	}
	scope := &idempotencyTestScope{
		database: database, token: token, tokenHash: tokenHash,
		deps: Dependencies{Store: database, SessionService: auth.SessionService{Store: database}},
	}
	t.Cleanup(func() {
		for _, key := range scope.keys {
			_, _ = database.Pool.Exec(ctx, `DELETE FROM system.idempotency_keys WHERE idempotency_key=$1`, key)
		}
		_, _ = database.Pool.Exec(ctx, `DELETE FROM identity.sessions WHERE token_hash=$1`, tokenHash)
	})
	return scope
}

func (s *idempotencyTestScope) trackKey(key string) {
	s.keys = append(s.keys, key)
}

// handler mirrors the production middleware order: withRequestID is the
// outermost wrapper, so the replayed response keeps the current request id.
func (s *idempotencyTestScope) handler(next http.Handler) http.Handler {
	return withRequestID(httpIdempotency(s.deps, next))
}

func (s *idempotencyTestScope) request(key, body, requestID string) *http.Request {
	request := httptest.NewRequest(http.MethodPost, idempotencyTestPath, strings.NewReader(body))
	request.AddCookie(&http.Cookie{Name: auth.SessionCookieConfig.Name, Value: s.token})
	if key != "" {
		request.Header.Set("Idempotency-Key", key)
	}
	if requestID != "" {
		request.Header.Set("X-Request-Id", requestID)
	}
	return request
}

// TestIdempotencyReplayReturnsCachedResponseIntegration: a retry with the
// same key and body replays the stored response with the replay marker and
// executes the business handler exactly once.
func TestIdempotencyReplayReturnsCachedResponseIntegration(t *testing.T) {
	scope := newIdempotencyTestScope(t)
	key := "itest-replay-" + uuid.NewString()
	scope.trackKey(key)
	var executions atomic.Int32
	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(fmt.Sprintf(`{"execution":%d}`, executions.Add(1))))
	})
	handler := scope.handler(next)

	first := httptest.NewRecorder()
	handler.ServeHTTP(first, scope.request(key, `{"title":"a"}`, ""))
	if first.Code != http.StatusCreated || first.Body.String() != `{"execution":1}` {
		t.Fatalf("first response = %d %s", first.Code, first.Body.String())
	}
	if first.Header().Get("Idempotency-Replayed") != "" {
		t.Fatal("first execution must not carry the replay marker")
	}

	second := httptest.NewRecorder()
	handler.ServeHTTP(second, scope.request(key, `{"title":"a"}`, ""))
	if second.Code != http.StatusCreated {
		t.Fatalf("replay status = %d, body %s", second.Code, second.Body.String())
	}
	if second.Header().Get("Idempotency-Replayed") != "true" {
		t.Fatal("replay must carry Idempotency-Replayed: true")
	}
	if second.Body.String() != `{"execution":1}` {
		t.Fatalf("replay must return the cached body, got %s", second.Body.String())
	}
	if executions.Load() != 1 {
		t.Fatalf("business handler ran %d times, want 1", executions.Load())
	}
}

// TestIdempotencyDifferentBodyConflictsIntegration: the same key with a
// different body is a client conflict, not a replay and not a re-execution.
func TestIdempotencyDifferentBodyConflictsIntegration(t *testing.T) {
	scope := newIdempotencyTestScope(t)
	key := "itest-conflict-" + uuid.NewString()
	scope.trackKey(key)
	var executions atomic.Int32
	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(fmt.Sprintf(`{"execution":%d}`, executions.Add(1))))
	})
	handler := scope.handler(next)

	first := httptest.NewRecorder()
	handler.ServeHTTP(first, scope.request(key, `{"title":"a"}`, ""))
	if first.Code != http.StatusCreated {
		t.Fatalf("first response = %d", first.Code)
	}

	second := httptest.NewRecorder()
	handler.ServeHTTP(second, scope.request(key, `{"title":"different"}`, ""))
	if second.Code != http.StatusConflict {
		t.Fatalf("different body with same key = %d, want 409", second.Code)
	}
	if !strings.Contains(second.Body.String(), "idempotency_conflict") {
		t.Fatalf("conflict body = %s", second.Body.String())
	}
	if executions.Load() != 1 {
		t.Fatalf("business handler ran %d times, want 1", executions.Load())
	}
}

// TestIdempotencyNon2xxReleasesReservationIntegration: a non-2xx outcome
// releases the reservation, so a retry with the same key re-executes the
// business operation.
func TestIdempotencyNon2xxReleasesReservationIntegration(t *testing.T) {
	scope := newIdempotencyTestScope(t)
	key := "itest-retry-" + uuid.NewString()
	scope.trackKey(key)
	var executions atomic.Int32
	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if executions.Add(1) == 1 {
			writeError(w, http.StatusUnprocessableEntity, "invalid_input")
			return
		}
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"execution":2}`))
	})
	handler := scope.handler(next)

	first := httptest.NewRecorder()
	handler.ServeHTTP(first, scope.request(key, `{"title":"a"}`, ""))
	if first.Code != http.StatusUnprocessableEntity || executions.Load() != 1 {
		t.Fatalf("first response = %d, executions = %d", first.Code, executions.Load())
	}

	second := httptest.NewRecorder()
	handler.ServeHTTP(second, scope.request(key, `{"title":"a"}`, ""))
	if second.Code != http.StatusCreated {
		t.Fatalf("retry after non-2xx = %d, body %s", second.Code, second.Body.String())
	}
	if second.Header().Get("Idempotency-Replayed") != "" {
		t.Fatal("re-executed retry must not carry the replay marker")
	}
	if executions.Load() != 2 {
		t.Fatalf("business handler ran %d times, want 2", executions.Load())
	}
}

// TestIdempotencyReplayKeepsCurrentRequestIDIntegration: the stored headers
// must not resurrect the original exchange's X-Request-Id; a replay echoes
// the id of the request being answered right now.
func TestIdempotencyReplayKeepsCurrentRequestIDIntegration(t *testing.T) {
	scope := newIdempotencyTestScope(t)
	key := "itest-requestid-" + uuid.NewString()
	scope.trackKey(key)
	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{}`))
	})
	handler := scope.handler(next)

	first := httptest.NewRecorder()
	handler.ServeHTTP(first, scope.request(key, `{"title":"a"}`, "original-request-id-0001"))
	if first.Header().Get("X-Request-Id") != "original-request-id-0001" {
		t.Fatalf("first response request id = %q", first.Header().Get("X-Request-Id"))
	}

	second := httptest.NewRecorder()
	handler.ServeHTTP(second, scope.request(key, `{"title":"a"}`, "replay-request-id-0002"))
	if got := second.Header().Get("X-Request-Id"); got != "replay-request-id-0002" {
		t.Fatalf("replay response request id = %q, want the current request's id", got)
	}
}

// TestIdempotencyPanicReleasesReservationIntegration: a handler panic answers
// 500 internal_panic without killing the process, logs the stack, and
// releases the reservation so the same key can be retried.
func TestIdempotencyPanicReleasesReservationIntegration(t *testing.T) {
	scope := newIdempotencyTestScope(t)
	key := "itest-panic-" + uuid.NewString()
	scope.trackKey(key)
	saved := idempotencyLogf
	var logged atomic.Bool
	idempotencyLogf = func(string, ...any) { logged.Store(true) }
	t.Cleanup(func() { idempotencyLogf = saved })

	var executions atomic.Int32
	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if executions.Add(1) == 1 {
			panic("boom")
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"recovered":true}`))
	})
	handler := scope.handler(next)

	first := httptest.NewRecorder()
	handler.ServeHTTP(first, scope.request(key, `{"title":"a"}`, ""))
	if first.Code != http.StatusInternalServerError {
		t.Fatalf("panicking handler answered %d, want 500", first.Code)
	}
	if !strings.Contains(first.Body.String(), "internal_panic") {
		t.Fatalf("panic body = %s", first.Body.String())
	}
	if !logged.Load() {
		t.Fatal("panic must be logged with its stack")
	}

	second := httptest.NewRecorder()
	handler.ServeHTTP(second, scope.request(key, `{"title":"a"}`, ""))
	if second.Code != http.StatusOK || second.Body.String() != `{"recovered":true}` {
		t.Fatalf("retry after panic = %d %s; reservation was not released", second.Code, second.Body.String())
	}
	if executions.Load() != 2 {
		t.Fatalf("business handler ran %d times, want 2", executions.Load())
	}
}

// TestIdempotencyCompleteFailureKeepsSuccessResponseIntegration: when storing
// the captured 2xx response fails (here: a concurrent writer completed the
// reservation first), the caller still receives the successful business
// response instead of a 500, and the degradation is logged.
func TestIdempotencyCompleteFailureKeepsSuccessResponseIntegration(t *testing.T) {
	scope := newIdempotencyTestScope(t)
	key := "itest-completefail-" + uuid.NewString()
	scope.trackKey(key)
	saved := idempotencyLogf
	var logs strings.Builder
	idempotencyLogf = func(format string, args ...any) {
		logs.WriteString(fmt.Sprintf(format, args...))
	}
	t.Cleanup(func() { idempotencyLogf = saved })

	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		// Simulate a concurrent completion landing between the business
		// transaction and the middleware's persist step.
		if _, err := scope.database.Pool.Exec(context.Background(), `
			UPDATE system.idempotency_keys
			SET response_status=200, response_headers='{}'::jsonb, response_bytes=''
			WHERE idempotency_key=$1 AND response_status IS NULL
		`, key); err != nil {
			t.Errorf("seed concurrent completion: %v", err)
		}
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"business":"committed"}`))
	})
	handler := scope.handler(next)

	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, scope.request(key, `{"title":"a"}`, ""))
	if recorder.Code != http.StatusCreated {
		t.Fatalf("persist failure must not mask the business result: %d %s", recorder.Code, recorder.Body.String())
	}
	if recorder.Body.String() != `{"business":"committed"}` {
		t.Fatalf("body = %s", recorder.Body.String())
	}
	if !strings.Contains(logs.String(), "persist failed") {
		t.Fatalf("persist failure must be logged, got %q", logs.String())
	}
}
