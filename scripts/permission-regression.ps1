[CmdletBinding()]
param(
    [string]$DatabaseUrl = "postgresql://agentchunzhi:dev-only-change-me@postgres:5432/agentchunzhi?sslmode=disable",
    [string]$DockerNetwork = "agentchunzhi",
    [string]$BuildImage = "agentchunzhi-wip-build"
)

$ErrorActionPreference = "Stop"
& docker compose up -d --wait postgres
if ($LASTEXITCODE -ne 0) { throw "postgres did not become healthy" }
& docker run --rm --network $DockerNetwork `
    -v "${PWD}:/src" -w /src `
    -e "PERMISSION_INTEGRATION_DATABASE_URL=$DatabaseUrl" `
    $BuildImage go test ./internal/authz -run TestPermissionScopesIntegration -count=1 -v
if ($LASTEXITCODE -ne 0) { throw "permission regression failed" }
