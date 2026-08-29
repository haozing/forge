// cmd/bootstrap creates the initial organization and its first organization
// admin. It runs after cmd/migrate with the runtime DATABASE_URL and re-applies
// the deterministic builtin model seed for the new organization.
package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"strings"
	"time"

	"agentchunzhi/internal/auth"
	"agentchunzhi/internal/config"
	"agentchunzhi/internal/store"
)

func main() {
	cfg := config.Load()
	organizationSlug := requiredEnv("ORG_SLUG")
	organizationName := requiredEnv("ORG_NAME")
	adminEmail := normalizeEmail(requiredEnv("ADMIN_EMAIL"))
	displayName := envOrDefault("ADMIN_DISPLAY_NAME", strings.Split(adminEmail, "@")[0])
	password := requiredEnv("ADMIN_PASSWORD")

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	db, err := store.Open(ctx, cfg.DatabaseURL)
	if err != nil {
		log.Fatal(err)
	}
	defer db.Close()
	passwordHash, err := auth.HashPassword(password)
	if err != nil {
		log.Fatalf("hash admin password: %v", err)
	}
	tx, err := db.Pool.Begin(ctx)
	if err != nil {
		log.Fatalf("begin bootstrap transaction: %v", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var organizationID, userID string
	if err := tx.QueryRow(ctx, `
		INSERT INTO organization.organizations (slug, name)
		VALUES ($1, $2)
		RETURNING id
	`, organizationSlug, organizationName).Scan(&organizationID); err != nil {
		log.Fatalf("create organization: %v", err)
	}
	if err := tx.QueryRow(ctx, `
		INSERT INTO identity.users (organization_id, user_type, email, display_name, password_hash, organization_role)
		VALUES ($1::uuid, 'member', $2, $3, $4, 'admin')
		RETURNING id
	`, organizationID, adminEmail, displayName, passwordHash).Scan(&userID); err != nil {
		log.Fatalf("create admin user: %v", err)
	}
	if err := tx.Commit(ctx); err != nil {
		log.Fatalf("commit bootstrap transaction: %v", err)
	}
	if err := store.SeedBuiltinResourceModels(ctx, db, organizationID); err != nil {
		log.Fatalf("seed builtin resource models: %v", err)
	}
	fmt.Printf("created organization=%s admin_user=%s email=%s\n", organizationID, userID, adminEmail)
}

func normalizeEmail(value string) string {
	value = strings.TrimSpace(strings.ToLower(value))
	return value
}

func requiredEnv(key string) string {
	value := os.Getenv(key)
	if value == "" {
		log.Fatalf("%s is required", key)
	}
	return value
}

func envOrDefault(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}
