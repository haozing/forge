[CmdletBinding()]
param(
    [int]$MaxDeadDeliveries = 0,
    [int]$MaxFailedProcessingJobs = 0,
    [int]$MaxRetryingDeliveries = 100
)

$ErrorActionPreference = "Stop"
$dbUser = if ($env:POSTGRES_USER) { $env:POSTGRES_USER } else { "agentchunzhi" }
$dbName = if ($env:POSTGRES_DB) { $env:POSTGRES_DB } else { "agentchunzhi" }
$json = (Get-Content -Raw -LiteralPath (Join-Path $PSScriptRoot 'ops-check.sql') | & docker compose exec -T postgres psql -At -v ON_ERROR_STOP=1 -U $dbUser -d $dbName 2>&1 | Out-String)
if ($LASTEXITCODE -ne 0) { throw $json }
$line = ($json -split "`r?`n" | Where-Object { $_ -match "^\{" } | Select-Object -Last 1)
if (-not $line) { throw "ops query returned no JSON" }
$metrics = $line | ConvertFrom-Json
$metrics | ConvertTo-Json
if ([int]$metrics.dead_deliveries -gt $MaxDeadDeliveries -or [int]$metrics.failed_processing_jobs -gt $MaxFailedProcessingJobs -or [int]$metrics.retrying_deliveries -gt $MaxRetryingDeliveries -or [int]$metrics.stuck_processing_jobs -gt 0) { exit 2 }
