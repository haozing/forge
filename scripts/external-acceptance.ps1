[CmdletBinding()]
param(
    [string]$ApiUrl = "http://127.0.0.1:8080",
    [string]$BuildImage = "agentchunzhi-wip-build",
    [switch]$RequireOSS,
    [switch]$RequireASR,
    [string]$ASRSampleFile = $env:ASR_SAMPLE_FILE
)

$ErrorActionPreference = "Stop"
function Require-Env([string]$Name) {
    if ([string]::IsNullOrWhiteSpace((Get-Item "Env:$Name" -ErrorAction SilentlyContinue).Value)) { throw "$Name is required for this external acceptance" }
}

foreach ($path in @("/healthz", "/readyz")) {
    & curl.exe --fail --silent --show-error --max-time 10 "$ApiUrl$path" | Out-Null
    if ($LASTEXITCODE -ne 0) { throw "API health check failed: $path" }
}

function Invoke-ProviderAcceptance([string]$Provider) {
    $envArgs = @()
    foreach ($name in @("OSS_REGION", "OSS_BUCKET", "OSS_ENDPOINT", "OSS_PREFIX", "OSS_ACCESS_KEY_ID", "OSS_ACCESS_KEY_SECRET", "OSS_SESSION_TOKEN", "ASR_ENDPOINT", "ASR_TOKEN", "ASR_MODEL", "ASR_PROVIDER", "ASR_REGION", "ASR_ENGINE", "TENCENTCLOUD_SECRET_ID", "TENCENTCLOUD_SECRET_KEY", "ASR_TIMEOUT_SECONDS")) {
        $value = (Get-Item "Env:$name" -ErrorAction SilentlyContinue).Value
        if (-not [string]::IsNullOrWhiteSpace($value)) { $envArgs += @("-e", "$name=$value") }
    }
    $mountArgs = @()
    if ($Provider -eq "asr") {
        if ([string]::IsNullOrWhiteSpace($ASRSampleFile)) { throw "ASR_SAMPLE_FILE is required for ASR acceptance" }
        $sample = (Resolve-Path -LiteralPath $ASRSampleFile).Path
        $envArgs += @("-e", "ASR_SAMPLE_FILE=/tmp/acceptance-media")
        $mountArgs += @("-v", "${sample}:/tmp/acceptance-media:ro")
    }
    & docker run --rm @mountArgs -v "${PWD}:/src" -w /src @envArgs $BuildImage go run ./cmd/provider-acceptance $Provider
    if ($LASTEXITCODE -ne 0) { throw "$Provider provider acceptance failed" }
}

if ($RequireOSS) {
    Require-Env "OSS_REGION"; Require-Env "OSS_BUCKET"; Require-Env "OSS_ACCESS_KEY_ID"; Require-Env "OSS_ACCESS_KEY_SECRET"
    Invoke-ProviderAcceptance "oss"
}
if ($RequireASR) {
    $asrProvider = (Get-Item "Env:ASR_PROVIDER" -ErrorAction SilentlyContinue).Value
    if ($asrProvider -eq "tencent") {
        Require-Env "TENCENTCLOUD_SECRET_ID"; Require-Env "TENCENTCLOUD_SECRET_KEY"
    } else {
        Require-Env "ASR_ENDPOINT"; Require-Env "ASR_TOKEN"
    }
    Invoke-ProviderAcceptance "asr"
}
Write-Host "External acceptance completed for the requested providers."
exit 0
