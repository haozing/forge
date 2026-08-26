package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"time"

	"agentchunzhi/internal/auth"
	"agentchunzhi/internal/config"
	"agentchunzhi/internal/store"
)

func main() {
	cfg := config.Load()
	organizationName := requiredEnv("ORG_NAME")
	loginName := requiredEnv("ADMIN_LOGIN")
	displayName := envOrDefault("ADMIN_DISPLAY_NAME", loginName)
	password := requiredEnv("ADMIN_PASSWORD")

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	db, err := store.Open(ctx, cfg.DatabaseURL)
	if err != nil {
		log.Fatal(err)
	}
	defer db.Close()
	if err := store.ApplyMigration(ctx, db, cfg.MigrationPath); err != nil {
		log.Fatalf("migration failed: %v", err)
	}
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
		INSERT INTO organization.organizations (name)
		VALUES ($1)
		RETURNING id
	`, organizationName).Scan(&organizationID); err != nil {
		log.Fatalf("create organization: %v", err)
	}
	if err := tx.QueryRow(ctx, `
		INSERT INTO identity.users (organization_id, user_type, login_name, display_name, password_hash, member_role)
		VALUES ($1::uuid, 'member', $2, $3, $4, 'admin')
		RETURNING id
	`, organizationID, loginName, displayName, passwordHash).Scan(&userID); err != nil {
		log.Fatalf("create admin user: %v", err)
	}
	if err := tx.Commit(ctx); err != nil {
		log.Fatalf("commit bootstrap transaction: %v", err)
	}
	fmt.Printf("created organization=%s admin_user=%s login=%s\n", organizationID, userID, loginName)
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
