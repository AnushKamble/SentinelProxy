# SentinelProxy Smoke Test Script (PowerShell)
param(
    [string]$GatewayAddr = "http://127.0.0.1:8080"
)

Write-Host "Running SentinelProxy Smoke Test against $GatewayAddr..." -ForegroundColor Cyan

# 1. Healthz
$health = curl.exe -s -w "%{http_code}" "$GatewayAddr/healthz"
if ($health -match "200$") {
    Write-Host "[PASS] GET /healthz returned 200 OK" -ForegroundColor Green
} else {
    Write-Host "[FAIL] GET /healthz returned $health" -ForegroundColor Red
}

# 2. Readyz
$ready = curl.exe -s -w "%{http_code}" "$GatewayAddr/readyz"
if ($ready -match "200$") {
    Write-Host "[PASS] GET /readyz returned 200 OK" -ForegroundColor Green
} else {
    Write-Host "[FAIL] GET /readyz returned $ready" -ForegroundColor Red
}

# 3. Status
$status = curl.exe -s -w "%{http_code}" "$GatewayAddr/-/status"
if ($status -match "200$") {
    Write-Host "[PASS] GET /-/status returned 200 OK" -ForegroundColor Green
} else {
    Write-Host "[FAIL] GET /-/status returned $status" -ForegroundColor Red
}

# 4. Metrics
$metrics = curl.exe -s -w "%{http_code}" "$GatewayAddr/metrics"
if ($metrics -match "200$") {
    Write-Host "[PASS] GET /metrics returned 200 OK" -ForegroundColor Green
} else {
    Write-Host "[FAIL] GET /metrics returned $metrics" -ForegroundColor Red
}

Write-Host "Smoke test sequence complete." -ForegroundColor Cyan
