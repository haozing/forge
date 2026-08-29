// cmd/migrate is the only process allowed to execute DDL. It connects with
// DATABASE_MIGRATION_URL (schema owner), applies the flat migration baseline,
// runs River's own migrations and grants the restricted runtime role its
// explicit privileges. API and worker only verify the schema contract.
package main

import (
	"context"
	"errors"
	"fmt"
	"os"

	"agentchunzhi/internal/eventing"
	"agentchunzhi/internal/migration"

	"github.com/jackc/pgx/v5/pgxpool"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "migrate:", err)
		os.Exit(1)
	}
}

func run() error {
	ctx := context.Background()
	migrationURL := os.Getenv("DATABASE_MIGRATION_URL")
	if migrationURL == "" {
		return errors.New("DATABASE_MIGRATION_URL is required")
	}
	path := os.Getenv("MIGRATION_PATH")
	if path == "" {
		path = "db/migrations"
	}
	runtimeRole := os.Getenv("DATABASE_RUNTIME_ROLE")

	pool, err := pgxpool.New(ctx, migrationURL)
	if err != nil {
		return fmt.Errorf("connect with migration role: %w", err)
	}
	defer pool.Close()
	if err := pool.Ping(ctx); err != nil {
		return fmt.Errorf("ping database: %w", err)
	}

	conn, err := pool.Acquire(ctx)
	if err != nil {
		return fmt.Errorf("acquire migration connection: %w", err)
	}
	defer conn.Release()

	if err := migration.Run(ctx, conn.Conn(), path); err != nil {
		return err
	}
	if err := eventing.Migrate(ctx, pool); err != nil {
		return fmt.Errorf("river migrations: %w", err)
	}
	if err := migration.GrantRuntimeRole(ctx, conn.Conn(), runtimeRole); err != nil {
		return err
	}
	fmt.Println("migrate: baseline applied")
	return nil
}
