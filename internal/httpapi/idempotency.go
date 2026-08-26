package httpapi

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"

	"agentchunzhi/internal/auth"
	"agentchunzhi/internal/store"

	"github.com/jackc/pgx/v5"
)

const maxIdempotentRequestBytes = 64 << 20

type idempotentHTTPResponse struct {
	Status  int
	Headers http.Header
	Body    []byte
}

type bufferedResponseWriter struct {
	header http.Header
	status int
	body   bytes.Buffer
}

func newBufferedResponseWriter(initial http.Header) *bufferedResponseWriter {
	return &bufferedResponseWriter{header: initial.Clone()}
}

func (writer *bufferedResponseWriter) Header() http.Header { return writer.header }

func (writer *bufferedResponseWriter) WriteHeader(status int) {
	if writer.status != 0 {
		return
	}
	writer.status = status
}

func (writer *bufferedResponseWriter) Write(value []byte) (int, error) {
	if writer.status == 0 {
		writer.status = http.StatusOK
	}
	return writer.body.Write(value)
}

func frontendIdempotency(deps Dependencies, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !requiresHTTPIdempotency(r) || deps.Store == nil || deps.Store.Pool == nil {
			next.ServeHTTP(w, r)
			return
		}
		principal, err := deps.SessionService.Authenticate(r.Context(), r)
		if err != nil {
			next.ServeHTTP(w, r)
			return
		}
		// Authenticate before validating idempotency/body fields so anonymous
		// requests consistently receive 401 for protected operations.
		key, ok := requiredIdempotencyKey(r)
		if !ok {
			writeError(w, http.StatusUnprocessableEntity, "idempotency_key_required")
			return
		}
		requestHash, cleanup, err := snapshotIdempotentRequest(r)
		if err != nil {
			if errors.Is(err, errIdempotentRequestTooLarge) {
				writeError(w, http.StatusRequestEntityTooLarge, "request_too_large")
			} else {
				writeError(w, http.StatusBadRequest, "request_read_failed")
			}
			return
		}
		defer cleanup()
		operation := "frontend.http:" + r.Method + ":" + r.URL.Path
		replay, reserved, err := reserveHTTPIdempotency(r.Context(), deps.Store, principal, operation, key, requestHash)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "idempotency_check_failed")
			return
		}
		if replay != nil {
			copyHTTPHeader(w.Header(), replay.Headers)
			w.Header().Set("Idempotency-Replayed", "true")
			w.WriteHeader(replay.Status)
			_, _ = w.Write(replay.Body)
			return
		}
		if !reserved {
			writeError(w, http.StatusConflict, "idempotency_conflict")
			return
		}

		capture := newBufferedResponseWriter(w.Header())
		next.ServeHTTP(capture, r)
		if capture.status == 0 {
			capture.status = http.StatusOK
		}
		if capture.status >= 200 && capture.status < 300 {
			if err := completeHTTPIdempotency(r.Context(), deps.Store, principal, operation, key, requestHash, capture.status, capture.header, capture.body.Bytes()); err != nil {
				writeError(w, http.StatusInternalServerError, "idempotency_save_failed")
				return
			}
		} else {
			_ = releaseHTTPIdempotency(r.Context(), deps.Store, principal, operation, key, requestHash)
		}
		copyHTTPHeader(w.Header(), capture.header)
		w.WriteHeader(capture.status)
		_, _ = w.Write(capture.body.Bytes())
	})
}

func requiresHTTPIdempotency(r *http.Request) bool {
	if !strings.HasPrefix(r.URL.Path, "/api/frontend/") && r.URL.Path != "/api/me/profile" {
		return false
	}
	if r.Method != http.MethodPost && r.Method != http.MethodPatch && r.Method != http.MethodPut && r.Method != http.MethodDelete {
		return false
	}
	if r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/query") {
		return false
	}
	if r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/references/validate") {
		return false
	}
	if strings.HasSuffix(r.URL.Path, "/stream") {
		return false
	}
	return true
}

var errIdempotentRequestTooLarge = errors.New("idempotent request is too large")

func snapshotIdempotentRequest(r *http.Request) (string, func(), error) {
	hasher := sha256.New()
	_, _ = io.WriteString(hasher, r.Method)
	_, _ = io.WriteString(hasher, "\x00"+r.URL.Path+"\x00"+r.URL.RawQuery+"\x00")
	if r.Body == nil || r.Body == http.NoBody {
		return hex.EncodeToString(hasher.Sum(nil)), func() {}, nil
	}
	if r.ContentLength > maxIdempotentRequestBytes {
		return "", func() {}, errIdempotentRequestTooLarge
	}
	temporary, err := os.CreateTemp("", "agentchunzhi-idempotency-*")
	if err != nil {
		return "", func() {}, err
	}
	cleanup := func() {
		_ = temporary.Close()
		_ = os.Remove(temporary.Name())
	}
	written, err := io.Copy(io.MultiWriter(temporary, hasher), io.LimitReader(r.Body, maxIdempotentRequestBytes+1))
	_ = r.Body.Close()
	if err != nil {
		cleanup()
		return "", func() {}, err
	}
	if written > maxIdempotentRequestBytes {
		cleanup()
		return "", func() {}, errIdempotentRequestTooLarge
	}
	if _, err := temporary.Seek(0, io.SeekStart); err != nil {
		cleanup()
		return "", func() {}, err
	}
	r.Body = temporary
	r.ContentLength = written
	return hex.EncodeToString(hasher.Sum(nil)), cleanup, nil
}

func reserveHTTPIdempotency(ctx context.Context, database *store.Store, principal auth.Principal, operation, key, requestHash string) (*idempotentHTTPResponse, bool, error) {
	tx, err := database.Pool.Begin(ctx)
	if err != nil {
		return nil, false, err
	}
	defer tx.Rollback(ctx)
	_, _ = tx.Exec(ctx, `DELETE FROM system.idempotency_keys WHERE organization_id=$1::uuid AND subject_id=$2::uuid AND operation=$3 AND idempotency_key=$4 AND expires_at<=now()`, principal.OrganizationID, principal.UserID, operation, key)
	var id string
	err = tx.QueryRow(ctx, `
		INSERT INTO system.idempotency_keys (organization_id,subject_id,operation,idempotency_key,request_hash,expires_at)
		VALUES ($1::uuid,$2::uuid,$3,$4,$5,now()+interval '24 hours')
		ON CONFLICT (organization_id,subject_id,operation,idempotency_key) DO NOTHING
		RETURNING id::text
	`, principal.OrganizationID, principal.UserID, operation, key, requestHash).Scan(&id)
	if err == nil {
		if err := tx.Commit(ctx); err != nil {
			return nil, false, err
		}
		return nil, true, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return nil, false, err
	}
	var storedHash string
	var status *int
	var rawHeaders, body []byte
	err = tx.QueryRow(ctx, `
		SELECT request_hash,response_status,response_headers,response_bytes
		FROM system.idempotency_keys
		WHERE organization_id=$1::uuid AND subject_id=$2::uuid AND operation=$3 AND idempotency_key=$4
		FOR UPDATE
	`, principal.OrganizationID, principal.UserID, operation, key).Scan(&storedHash, &status, &rawHeaders, &body)
	if err != nil {
		return nil, false, err
	}
	if storedHash != requestHash || status == nil {
		if err := tx.Commit(ctx); err != nil {
			return nil, false, err
		}
		return nil, false, nil
	}
	headers := make(http.Header)
	if len(rawHeaders) > 0 {
		if err := json.Unmarshal(rawHeaders, &headers); err != nil {
			return nil, false, fmt.Errorf("decode idempotent response headers: %w", err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, false, err
	}
	return &idempotentHTTPResponse{Status: *status, Headers: headers, Body: body}, false, nil
}

func completeHTTPIdempotency(ctx context.Context, database *store.Store, principal auth.Principal, operation, key, requestHash string, status int, headers http.Header, body []byte) error {
	rawHeaders, err := json.Marshal(headers)
	if err != nil {
		return err
	}
	command, err := database.Pool.Exec(ctx, `
		UPDATE system.idempotency_keys
		SET response_status=$6,response_headers=$7::jsonb,response_bytes=$8
		WHERE organization_id=$1::uuid AND subject_id=$2::uuid AND operation=$3
		  AND idempotency_key=$4 AND request_hash=$5 AND response_status IS NULL
	`, principal.OrganizationID, principal.UserID, operation, key, requestHash, status, string(rawHeaders), body)
	if err != nil {
		return err
	}
	if command.RowsAffected() != 1 {
		return errors.New("idempotency reservation was lost")
	}
	return nil
}

func releaseHTTPIdempotency(ctx context.Context, database *store.Store, principal auth.Principal, operation, key, requestHash string) error {
	_, err := database.Pool.Exec(ctx, `DELETE FROM system.idempotency_keys WHERE organization_id=$1::uuid AND subject_id=$2::uuid AND operation=$3 AND idempotency_key=$4 AND request_hash=$5 AND response_status IS NULL`, principal.OrganizationID, principal.UserID, operation, key, requestHash)
	return err
}

func copyHTTPHeader(destination, source http.Header) {
	for key := range destination {
		destination.Del(key)
	}
	for key, values := range source {
		for _, value := range values {
			destination.Add(key, value)
		}
	}
}
