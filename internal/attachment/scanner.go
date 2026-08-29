package attachment

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"strings"
	"time"

	"agentchunzhi/internal/objectstore"
	"agentchunzhi/internal/store"

	"github.com/jackc/pgx/v5"
)

var ErrInfected = errors.New("attachment malware detected")

type Scanner interface {
	Scan(context.Context, io.Reader) error
}

// ClamAVScanner implements clamd's null-terminated INSTREAM protocol. The
// object is never copied to a local file and the caller controls the deadline.
type ClamAVScanner struct {
	Address string
	Timeout time.Duration
}

func (s ClamAVScanner) Scan(ctx context.Context, source io.Reader) error {
	if strings.TrimSpace(s.Address) == "" || source == nil {
		return errors.New("ClamAV scanner is not configured")
	}
	timeout := s.Timeout
	if timeout <= 0 {
		timeout = 2 * time.Minute
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	conn, err := (&net.Dialer{}).DialContext(ctx, "tcp", s.Address)
	if err != nil {
		return fmt.Errorf("connect to ClamAV: %w", err)
	}
	defer conn.Close()
	if deadline, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(deadline)
	}
	if _, err := conn.Write([]byte("zINSTREAM\x00")); err != nil {
		return fmt.Errorf("start ClamAV stream: %w", err)
	}

	buffer := make([]byte, 32*1024)
	for {
		n, readErr := source.Read(buffer)
		if n > 0 {
			var size [4]byte
			binary.BigEndian.PutUint32(size[:], uint32(n))
			if _, err := conn.Write(size[:]); err != nil {
				return fmt.Errorf("write ClamAV chunk size: %w", err)
			}
			if _, err := conn.Write(buffer[:n]); err != nil {
				return fmt.Errorf("write ClamAV chunk: %w", err)
			}
		}
		if errors.Is(readErr, io.EOF) {
			break
		}
		if readErr != nil {
			return fmt.Errorf("read attachment for ClamAV: %w", readErr)
		}
	}
	if _, err := conn.Write([]byte{0, 0, 0, 0}); err != nil {
		return fmt.Errorf("finish ClamAV stream: %w", err)
	}
	response, err := bufio.NewReader(io.LimitReader(conn, 4096)).ReadString(0)
	if err != nil && !errors.Is(err, io.EOF) {
		return fmt.Errorf("read ClamAV response: %w", err)
	}
	response = strings.TrimSpace(strings.TrimSuffix(response, "\x00"))
	switch {
	case strings.HasSuffix(response, " OK") || response == "stream: OK":
		return nil
	case strings.Contains(response, " FOUND"):
		return ErrInfected
	default:
		return fmt.Errorf("ClamAV scan failed: %s", response)
	}
}

type ScanProcessor struct {
	Store   *store.Store
	Objects objectstore.ObjectStore
	Scanner Scanner
}

func (p ScanProcessor) Process(ctx context.Context, organizationID, attachmentID string) error {
	if p.Store == nil || p.Store.Pool == nil || p.Objects == nil || p.Scanner == nil {
		return errors.New("attachment scanner is not initialized")
	}
	var objectKey, expectedChecksum, status string
	var expectedSize int64
	err := p.Store.Pool.QueryRow(ctx, `
		SELECT object_key, byte_size, sha256, status
		FROM asset.attachments
		WHERE organization_id = $1::uuid AND id = $2::uuid AND deleted_at IS NULL
	`, organizationID, attachmentID).Scan(&objectKey, &expectedSize, &expectedChecksum, &status)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return fmt.Errorf("load attachment for scan: %w", err)
	}
	if status == "clean" || status == "rejected" {
		return nil
	}

	object, err := p.Objects.Get(ctx, objectstore.ObjectRef{Key: objectKey})
	if err != nil {
		return fmt.Errorf("open attachment for scan: %w", err)
	}
	defer object.Body.Close()
	hash := sha256.New()
	counting := &countWriter{Writer: hash}
	stream := io.TeeReader(object.Body, counting)
	if err := p.Scanner.Scan(ctx, stream); err != nil {
		if errors.Is(err, ErrInfected) {
			return p.setStatus(ctx, organizationID, attachmentID, "rejected")
		}
		return err
	}
	if counting.N != expectedSize || !strings.EqualFold(hex.EncodeToString(hash.Sum(nil)), expectedChecksum) {
		return errors.New("attachment object integrity check failed")
	}
	return p.setStatus(ctx, organizationID, attachmentID, "clean")
}

func (p ScanProcessor) Fail(ctx context.Context, organizationID, attachmentID string) error {
	if p.Store == nil || p.Store.Pool == nil {
		return errors.New("database store is not initialized")
	}
	return p.setStatus(ctx, organizationID, attachmentID, "failed")
}

// CleanupExpired soft-deletes expired attachments that are not referenced by
// any draft or sealed version and removes their objects. Attachments bound to
// a draft or version survive regardless of their expiry stamp.
func (p ScanProcessor) CleanupExpired(ctx context.Context) (int, error) {
	if p.Store == nil || p.Store.Pool == nil {
		return 0, errors.New("database store is not initialized")
	}
	rows, err := p.Store.Pool.Query(ctx, `
		SELECT at.organization_id::text, at.id::text, at.object_key
		FROM asset.attachments at
		WHERE at.expires_at IS NOT NULL AND at.expires_at <= now()
		  AND at.deleted_at IS NULL
		  AND NOT EXISTS (SELECT 1 FROM asset.asset_draft_attachments lda WHERE lda.attachment_id = at.id)
		  AND NOT EXISTS (SELECT 1 FROM asset.asset_version_attachments lva WHERE lva.attachment_id = at.id)
	`)
	if err != nil {
		return 0, fmt.Errorf("list expired attachments: %w", err)
	}
	type expired struct{ orgID, id, objectKey string }
	items := []expired{}
	for rows.Next() {
		var item expired
		if err := rows.Scan(&item.orgID, &item.id, &item.objectKey); err != nil {
			rows.Close()
			return 0, err
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return 0, err
	}
	rows.Close()
	deleted := 0
	for _, item := range items {
		tag, err := p.Store.Pool.Exec(ctx, `
			UPDATE asset.attachments SET deleted_at = now(), updated_at = now()
			WHERE organization_id = $1::uuid AND id = $2::uuid AND deleted_at IS NULL
		`, item.orgID, item.id)
		if err != nil {
			return deleted, fmt.Errorf("expire attachment: %w", err)
		}
		if tag.RowsAffected() == 0 {
			continue
		}
		deleted++
		if p.Objects != nil && item.objectKey != "" {
			if err := p.Objects.Delete(ctx, objectstore.ObjectRef{Key: item.objectKey}); err != nil {
				return deleted, fmt.Errorf("delete expired attachment object: %w", err)
			}
		}
	}
	return deleted, nil
}

func (p ScanProcessor) setStatus(ctx context.Context, organizationID, attachmentID, status string) error {
	tag, err := p.Store.Pool.Exec(ctx, `
		UPDATE asset.attachments
		SET status = $3, updated_at = now()
		WHERE organization_id = $1::uuid AND id = $2::uuid AND deleted_at IS NULL
	`, organizationID, attachmentID, status)
	if err != nil {
		return fmt.Errorf("update attachment scan status: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

type countWriter struct {
	io.Writer
	N int64
}

func (w *countWriter) Write(value []byte) (int, error) {
	n, err := w.Writer.Write(value)
	w.N += int64(n)
	return n, err
}
