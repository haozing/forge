# reset-database.ps1 — destructive development database rebuild.
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

# docker-compose.yml hardcodes the migrate service connection to
# ${POSTGRES_DB:-agentchunzhi}; resetting any other database would migrate a
# different target than the one rebuilt here.
$composeDb = if ($env:POSTGRES_DB) { $env:POSTGRES_DB } else { "agentchunzhi" }
if ($Database -ne $composeDb) {
    Write-Error "Database '$Database' does not match the compose migration target '$composeDb' (POSTGRES_DB). Reset '$composeDb' instead."
    exit 1
}

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
# The migrate service reads its connection from docker-compose.yml
# (POSTGRES_USER/POSTGRES_PASSWORD/POSTGRES_DB) — host-side URL env vars are
# deliberately not consulted.
docker compose run --rm migrate
if ($LASTEXITCODE -ne 0) { throw "migrate failed" }

# Schema contract verification: run the contract spot checks and require the
# all-clear marker with zero fail rows; a missing object or wrong database
# fails the psql run itself through ON_ERROR_STOP.
$verifySqlPath = Join-Path $PSScriptRoot "verify-schema.sql"
$verifySql = Get-Content $verifySqlPath -Raw
$verifyOutput = docker exec $container psql -v ON_ERROR_STOP=1 -U $user -d $Database -c $verifySql
if ($LASTEXITCODE -ne 0) { throw "schema contract verification failed to run" }
$verifyOutput | Write-Host
if ($verifyOutput -match "fail:") {
    throw "schema contract verification failed: see fail rows above"
}
if (-not ($verifyOutput -match "schema_contract_ok")) {
    throw "schema contract verification did not report ok"
}

Write-Host "Rebuilding '$Database' complete. Run 'docker compose up api worker' to start from the empty baseline."
