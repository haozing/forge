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

// ReplayIdempotentSeed re-executes a self-idempotent seed migration file
// (e.g. 0043 builtin resource models) after schema migrations have run.
// Schema migrations are tracked by checksum and therefore skip already-applied
// files; a data seed embedded in one would never reach organizations created
// after registration. Replay relies on the file's ON CONFLICT guards instead,
// so boot-time invocation keeps every existing organization covered.
func ReplayIdempotentSeed(ctx context.Context, s *Store, path, filename string) error {
	if s == nil || s.Pool == nil {
		return errors.New("database store is not initialized")
	}
	target := filepath.Join(filepath.Clean(path), filename)
	body, err := os.ReadFile(target)
	if err != nil {
		return fmt.Errorf("read idempotent seed %s: %w", filename, err)
	}
	if _, err := s.Pool.Exec(ctx, string(body)); err != nil {
		return fmt.Errorf("replay idempotent seed %s: %w", filename, err)
	}
	return nil
}

func ApplyMigration(ctx context.Context, s *Store, path string) error {
	if s == nil || s.Pool == nil {
		return errors.New("database store is not initialized")
	}
	info, err := os.Stat(filepath.Clean(path))
	if err != nil {
		return fmt.Errorf("stat migration path: %w", err)
	}
	if !info.IsDir() {
		return applyMigrationFile(ctx, s, path)
	}
	entries, err := os.ReadDir(filepath.Clean(path))
	if err != nil {
		return fmt.Errorf("read migration directory: %w", err)
	}
	paths := make([]string, 0, len(entries))
	for _, entry := range entries {
		if !entry.IsDir() && strings.EqualFold(filepath.Ext(entry.Name()), ".sql") {
			paths = append(paths, filepath.Join(path, entry.Name()))
		}
	}
	sort.Strings(paths)
	if len(paths) == 0 {
		return errors.New("migration directory contains no SQL files")
	}
	for _, migrationPath := range paths {
		if err := applyMigrationFile(ctx, s, migrationPath); err != nil {
			return err
		}
	}
	return nil
}

func applyMigrationFile(ctx context.Context, s *Store, path string) error {
	sql, err := os.ReadFile(filepath.Clean(path))
	if err != nil {
		return fmt.Errorf("read migration: %w", err)
	}
	conn, err := s.Pool.Acquire(ctx)
	if err != nil {
		return fmt.Errorf("acquire migration connection: %w", err)
	}
	defer conn.Release()
	if _, err := conn.Exec(ctx, `CREATE SCHEMA IF NOT EXISTS system`); err != nil {
		return fmt.Errorf("prepare migration schema: %w", err)
	}
	if _, err := conn.Exec(ctx, `
		CREATE TABLE IF NOT EXISTS system.schema_migrations (
			version text PRIMARY KEY,
			checksum text NOT NULL,
			applied_at timestamptz NOT NULL DEFAULT now()
		)
	`); err != nil {
		return fmt.Errorf("prepare migration table: %w", err)
	}
	version := filepath.Base(path)
	checksum := sha256.Sum256(sql)
	checksumHex := hex.EncodeToString(checksum[:])
	if _, err := conn.Exec(ctx, `SELECT pg_advisory_lock(hashtext('agentchunzhi:migrations'))`); err != nil {
		return fmt.Errorf("lock migrations: %w", err)
	}
	defer func() {
		_, _ = conn.Exec(context.Background(), `SELECT pg_advisory_unlock(hashtext('agentchunzhi:migrations'))`)
	}()
	var appliedChecksum string
	err = conn.QueryRow(ctx, `SELECT checksum FROM system.schema_migrations WHERE version = $1`, version).Scan(&appliedChecksum)
	if err == nil {
		if appliedChecksum == "managed-by-migration-runner" {
			if _, updateErr := conn.Exec(ctx, `UPDATE system.schema_migrations SET checksum = $2, applied_at = now() WHERE version = $1`, version, checksumHex); updateErr != nil {
				return fmt.Errorf("update migration checksum %s: %w", version, updateErr)
			}
			return nil
		}
		if appliedChecksum != checksumHex {
			return fmt.Errorf("migration %s checksum changed", version)
		}
		return nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return fmt.Errorf("check migration %s: %w", version, err)
	}
	// pgx can execute a migration as one simple-protocol statement. Splitting
	// on semicolons corrupts dollar-quoted functions and procedural blocks.
	if _, err = conn.Exec(ctx, string(sql)); err != nil {
		_, _ = conn.Exec(context.Background(), `ROLLBACK`)
		return fmt.Errorf("apply migration: %w", err)
	}
	_, err = conn.Exec(ctx, `
		INSERT INTO system.schema_migrations (version, checksum)
		VALUES ($1, $2)
		ON CONFLICT (version) DO UPDATE
		SET checksum = EXCLUDED.checksum, applied_at = now()
	`, version, checksumHex)
	if err != nil {
		return fmt.Errorf("record migration: %w", err)
	}
	return nil
}
