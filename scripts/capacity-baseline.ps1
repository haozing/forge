[CmdletBinding()]
param(
    [string]$Url = "http://127.0.0.1:8080/healthz",
	[int]$Requests = 1000,
	[int]$Concurrency = 25,
	[int]$MaxP95Milliseconds = 500,
	[double]$MaxErrorRate = 0,
	[string]$BuildImage = "agentchunzhi-wip-build"
)

$ErrorActionPreference = "Stop"
& docker run --rm -v "${PWD}:/src" -w /src $BuildImage go run ./cmd/loadtest `
    -url $Url -requests $Requests -concurrency $Concurrency `
    -max-p95 "${MaxP95Milliseconds}ms" -max-error-rate $MaxErrorRate
if ($LASTEXITCODE -ne 0) { throw "capacity baseline failed" }
