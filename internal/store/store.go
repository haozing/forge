package store

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

var ErrNotFound = errors.New("not found")

type Store struct {
	Pool *pgxpool.Pool
}

func Open(ctx context.Context, databaseURL string) (*Store, error) {
	if databaseURL == "" {
		return nil, errors.New("DATABASE_URL is required")
	}
	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		return nil, fmt.Errorf("create database pool: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("ping database: %w", err)
	}
	return &Store{Pool: pool}, nil
}

func (s *Store) Close() {
	if s != nil && s.Pool != nil {
		s.Pool.Close()
	}
}

// VerifySchemaContract is the only schema interaction allowed for runtime
// processes: every migration file in the baseline root must be recorded in
// system.schema_migrations with a matching checksum. Mismatch means the
// process is pointed at a foreign or stale database and startup must fail.
func VerifySchemaContract(ctx context.Context, s *Store, path string) error {
	if s == nil || s.Pool == nil {
		return errors.New("database store is not initialized")
	}
	info, err := os.Stat(filepath.Clean(path))
	if err != nil {
		return fmt.Errorf("stat migration path: %w", err)
	}
	if !info.IsDir() {
		return errors.New("migration path must be the migration root directory")
	}
	entries, err := os.ReadDir(filepath.Clean(path))
	if err != nil {
		return fmt.Errorf("read migration directory: %w", err)
	}
	files := make([]string, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() {
			return fmt.Errorf("migration subdirectories are not allowed: %s", entry.Name())
		}
		if strings.EqualFold(filepath.Ext(entry.Name()), ".sql") {
			files = append(files, entry.Name())
		}
	}
	sort.Strings(files)
	if len(files) == 0 {
		return errors.New("migration directory contains no SQL files")
	}

	for _, name := range files {
		body, err := os.ReadFile(filepath.Join(path, name))
		if err != nil {
			return fmt.Errorf("read migration %s: %w", name, err)
		}
		sum := sha256.Sum256(body)
		var applied string
		err = s.Pool.QueryRow(ctx,
			`SELECT checksum FROM system.schema_migrations WHERE version = $1`, name).Scan(&applied)
		if errors.Is(err, pgx.ErrNoRows) {
			return fmt.Errorf("schema contract violation: migration %s has not been applied; run cmd/migrate with DATABASE_MIGRATION_URL", name)
		}
		if err != nil {
			return fmt.Errorf("check migration %s: %w", name, err)
		}
		if applied != hex.EncodeToString(sum[:]) {
			return fmt.Errorf("schema contract violation: migration %s checksum mismatch; rebuild the development database from the empty baseline", name)
		}
	}
	var extra int
	if err := s.Pool.QueryRow(ctx,
		`SELECT count(*) FROM system.schema_migrations WHERE version <> ALL($1::text[])`, files).Scan(&extra); err != nil {
		return fmt.Errorf("count recorded migrations: %w", err)
	}
	if extra > 0 {
		return fmt.Errorf("schema contract violation: database contains %d migrations outside the baseline", extra)
	}
	return nil
}
