// Package migration implements the standalone migrator contract: SQL files in
// the migration root, each applied with its checksum record inside one
// explicit transaction guarded by an advisory lock. API and worker never
// execute DDL; they only verify the schema contract.
package migration

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
	"github.com/jackc/pgx/v5/pgconn"
)

// Run applies every pending migration in path. path must be a directory;
// nested directories and non-SQL files are rejected so the file set is always
// the flat, name-ordered baseline.
func Run(ctx context.Context, conn *pgx.Conn, path string) error {
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

	for _, name := range files {
		if err := applyFile(ctx, conn, path, name); err != nil {
			return err
		}
	}
	return nil
}

func applyFile(ctx context.Context, conn *pgx.Conn, path, name string) error {
	body, err := os.ReadFile(filepath.Join(path, name))
	if err != nil {
		return fmt.Errorf("read migration %s: %w", name, err)
	}
	sum := sha256.Sum256(body)
	checksum := hex.EncodeToString(sum[:])

	tx, err := conn.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin migration %s: %w", name, err)
	}
	defer tx.Rollback(context.Background())

	// One explicit transaction holds the advisory lock, the DDL and the
	// checksum record: a crash leaves either everything or nothing.
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext('agentchunzhi:migrations'))`); err != nil {
		return fmt.Errorf("lock migrations: %w", err)
	}
	var applied string
	err = tx.QueryRow(ctx, `SELECT checksum FROM system.schema_migrations WHERE version = $1`, name).Scan(&applied)
	switch {
	case err == nil:
		if applied != checksum {
			return fmt.Errorf("migration %s checksum changed: applied baseline is immutable before first shared deployment", name)
		}
		return tx.Commit(ctx)
	case errors.Is(err, pgx.ErrNoRows):
		// fall through to apply
	default:
		return fmt.Errorf("check migration %s: %w", name, err)
	}

	// Simple protocol so multi-statement files execute verbatim inside the
	// same transaction.
	if _, err := tx.Conn().PgConn().Exec(ctx, string(body)).ReadAll(); err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) {
			return fmt.Errorf("apply migration %s: %s (position %d)", name, pgErr.Message, pgErr.Position)
		}
		return fmt.Errorf("apply migration %s: %w", name, err)
	}
	if _, err := tx.Exec(ctx,
		`INSERT INTO system.schema_migrations (version, checksum) VALUES ($1, $2)`,
		name, checksum,
	); err != nil {
		return fmt.Errorf("record migration %s: %w", name, err)
	}
	return tx.Commit(ctx)
}

// GrantRuntimeRole gives the restricted application role its explicit
// privileges. The migration role stays the only object owner; runtime
// processes connect with this role and can never execute DDL.
func GrantRuntimeRole(ctx context.Context, conn *pgx.Conn, role string) error {
	if strings.TrimSpace(role) == "" {
		return nil
	}
	_, err := conn.Exec(ctx, fmt.Sprintf(`DO $$ BEGIN
		IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = '%s') THEN
			CREATE ROLE %s NOLOGIN;
		END IF;
	END $$;`, role, role))
	if err != nil {
		return fmt.Errorf("ensure runtime role: %w", err)
	}
	schemas := []string{"organization", "identity", "security", "notification",
		`"authorization"`, "model", "asset", "content", "integration",
		"retrieval", "site", "audit", "system"}
	for _, schema := range schemas {
		if _, err := conn.Exec(ctx, fmt.Sprintf(`GRANT USAGE ON SCHEMA %s TO %s`, schema, role)); err != nil {
			return fmt.Errorf("grant schema usage: %w", err)
		}
		if _, err := conn.Exec(ctx, fmt.Sprintf(
			`GRANT SELECT, INSERT, UPDATE, DELETE ON ALL TABLES IN SCHEMA %s TO %s`, schema, role)); err != nil {
			return fmt.Errorf("grant tables: %w", err)
		}
		if _, err := conn.Exec(ctx, fmt.Sprintf(
			`GRANT USAGE, SELECT ON ALL SEQUENCES IN SCHEMA %s TO %s`, schema, role)); err != nil {
			return fmt.Errorf("grant sequences: %w", err)
		}
		if _, err := conn.Exec(ctx, fmt.Sprintf(
			`GRANT EXECUTE ON ALL FUNCTIONS IN SCHEMA %s TO %s`, schema, role)); err != nil {
			return fmt.Errorf("grant functions: %w", err)
		}
	}
	return nil
}
