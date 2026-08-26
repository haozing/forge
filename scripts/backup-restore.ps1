[CmdletBinding()]
param(
    [string]$BackupPath = (Join-Path (Get-Location) "artifacts\agentchunzhi.dump"),
    [string]$RestoreDatabase = "agentchunzhi_restore_check",
    [switch]$SkipRestore
)

$ErrorActionPreference = "Stop"
$dbUser = if ($env:POSTGRES_USER) { $env:POSTGRES_USER } else { "agentchunzhi" }
$dbName = if ($env:POSTGRES_DB) { $env:POSTGRES_DB } else { "agentchunzhi" }
if ($RestoreDatabase -notmatch '^agentchunzhi_[a-z0-9_]*restore[a-z0-9_]*$') { throw "RestoreDatabase must be an isolated agentchunzhi_*restore* database name" }
$backupDir = Split-Path -Parent $BackupPath
New-Item -ItemType Directory -Force -Path $backupDir | Out-Null

Write-Host "Creating logical backup: $BackupPath"
& docker compose exec -T postgres pg_dump -Fc -U $dbUser -d $dbName -f /tmp/agentchunzhi-drill.dump
if ($LASTEXITCODE -ne 0) { throw "pg_dump failed" }
& docker compose cp "postgres:/tmp/agentchunzhi-drill.dump" $BackupPath
if ($LASTEXITCODE -ne 0) { throw "copying backup from postgres failed" }

$hash = (Get-FileHash -Algorithm SHA256 -LiteralPath $BackupPath).Hash
Write-Host "Backup SHA256: $hash"
if ($SkipRestore) { exit 0 }

Write-Host "Restoring into isolated database: $RestoreDatabase"
& docker compose exec -T postgres psql -v ON_ERROR_STOP=1 -U $dbUser -d $dbName -c "DROP DATABASE IF EXISTS `"$RestoreDatabase`";"
if ($LASTEXITCODE -ne 0) { throw "dropping previous restore database failed" }
& docker compose exec -T postgres psql -v ON_ERROR_STOP=1 -U $dbUser -d $dbName -c "CREATE DATABASE `"$RestoreDatabase`";"
if ($LASTEXITCODE -ne 0) { throw "creating restore database failed" }
& docker compose cp $BackupPath "postgres:/tmp/agentchunzhi-drill-restore.dump"
if ($LASTEXITCODE -ne 0) { throw "copying backup into postgres failed" }
& docker compose exec -T postgres pg_restore --exit-on-error --no-owner -U $dbUser -d $RestoreDatabase /tmp/agentchunzhi-drill-restore.dump
if ($LASTEXITCODE -ne 0) { throw "pg_restore failed" }

$rowCount = & docker compose exec -T postgres psql -At -U $dbUser -d $RestoreDatabase -c "SELECT count(*) FROM system.schema_migrations"
if ($LASTEXITCODE -ne 0 -or [int]($rowCount.Trim()) -lt 1) { throw "restore verification failed: schema_migrations is empty" }
Write-Host "Backup and restore drill passed. schema_migrations rows=$($rowCount.Trim())"
