@echo off
setlocal EnableExtensions

set "ACCEPTANCE_API_URL=%API_URL%"
if not "%~1"=="" set "ACCEPTANCE_API_URL=%~1"
if "%ACCEPTANCE_API_URL%"=="" set "ACCEPTANCE_API_URL=http://127.0.0.1:8080"
set "BUSINESS_CONTAINER=%BUSINESS_CONTAINER%"
if "%BUSINESS_CONTAINER%"=="" set "BUSINESS_CONTAINER=agentchunzhi-pgroonga-api"

echo [1/4] API health checks
curl.exe --fail --silent --show-error "%ACCEPTANCE_API_URL%/healthz"
if errorlevel 1 exit /b 1
curl.exe --fail --silent --show-error "%ACCEPTANCE_API_URL%/readyz"
if errorlevel 1 exit /b 1
echo.

echo [2/4] Go unit and package tests
docker exec -w /src "%BUSINESS_CONTAINER%" go test ./...
if errorlevel 1 exit /b 1

if not "%RETRIEVAL_INTEGRATION_DATABASE_URL%"=="" (
  echo [3/4] Real PostgreSQL plus PGroonga integration test
  docker exec -w /src -e RETRIEVAL_INTEGRATION_DATABASE_URL="%RETRIEVAL_INTEGRATION_DATABASE_URL%" "%BUSINESS_CONTAINER%" go test ./internal/retrieval -run TestPGroongaProjectionAndFulltextQuery -count=1 -v
  if errorlevel 1 exit /b 1
) else (
  echo [3/4] Real PostgreSQL plus PGroonga integration test skipped: set RETRIEVAL_INTEGRATION_DATABASE_URL to enable it.
)

echo [4/4] Build verification image
docker build --target build -t agentchunzhi-acceptance-build .
if errorlevel 1 exit /b 1
docker image rm agentchunzhi-acceptance-build >NUL
if errorlevel 1 exit /b 1

echo Acceptance baseline passed. Production backup/restore and load tests remain deployment-specific.
exit /b 0
