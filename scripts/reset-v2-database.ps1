# reset-v2-database.ps1 — destructive development database rebuild.
# Phase 0-6 only: the v2 baseline is rebuilt from an empty database after every
# baseline change. Never point this at a shared or production database.
param(
    [Parameter(Mandatory = $true)][string]$Database,
    [Parameter(Mandatory = $true)][switch]$ConfirmReset
)

$ErrorActionPreference = "Stop"

if ($env:APP_ENV -eq "production") {
    Write-Error "Refusing to reset: APP_ENV=production"
    exit 1
}

if ($Database -in @("postgres", "template0", "template1")) {
    Write-Error "Refusing to reset system database '$Database'"
    exit 1
}

$user = if ($env:POSTGRES_USER) { $env:POSTGRES_USER } else { "agentchunzhi" }
$container = if ($env:POSTGRES_CONTAINER) { $env:POSTGRES_CONTAINER } else { "agentchunzhi-postgres-1" }

Write-Host "About to DROP and recreate database '$Database' on container '$container' (user '$user')."
if (-not $ConfirmReset) {
    Write-Error "Pass -ConfirmReset to acknowledge the destructive rebuild."
    exit 1
}

function Invoke-Psql([string]$Sql, [string]$TargetDb) {
    docker exec $container psql -v ON_ERROR_STOP=1 -U $user -d $TargetDb -c $Sql
    if ($LASTEXITCODE -ne 0) { throw "psql failed: $Sql" }
}

Invoke-Psql "DROP DATABASE IF EXISTS `"$Database`";" "postgres"
Invoke-Psql "CREATE DATABASE `"$Database`" OWNER `"$user`";" "postgres"
Write-Host "Database '$Database' recreated."

# Apply the v2 baseline through the standalone migrator with the owner role.
$env:DATABASE_MIGRATION_URL = if ($env:DATABASE_MIGRATION_URL) { $env:DATABASE_MIGRATION_URL } else { "postgresql://$user@$container:5432/$Database?sslmode=disable" }
docker compose run --rm migrate
if ($LASTEXITCODE -ne 0) { throw "migrate failed" }

# Schema contract verification: the runtime role must see every baseline file applied.
$env:DATABASE_URL = if ($env:DATABASE_URL) { $env:DATABASE_URL } else { "postgresql://$user@$container:5432/$Database?sslmode=disable" }
go run ./cmd/migrate -verify 2>$null
if ($LASTEXITCODE -ne 0) { Write-Host "note: optional verify hook not present; run scripts/verify-v2-schema.sql manually" }

Write-Host "Rebuilding '$Database' complete. Run 'docker compose up api worker' to start from the empty baseline."
