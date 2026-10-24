# SentinelProxy Native Multi-Process Demo Runner
# Starts mock-backend 1 & 2, starts SentinelProxy gateway, runs interactive resilience scenarios, and cleans up.

$ErrorActionPreference = "Stop"

Write-Host "===================================================================" -ForegroundColor Cyan
Write-Host " SentinelProxy - Resilient API Gateway Live Demo" -ForegroundColor Cyan
Write-Host "===================================================================" -ForegroundColor Cyan

# 1. Build Binaries
Write-Host "[1/6] Building project binaries..." -ForegroundColor Yellow
if (!(Test-Path -Path "bin")) { New-Item -ItemType Directory -Path "bin" -Force | Out-Null }
go build -o bin/sentinelproxy.exe ./cmd/sentinelproxy
go build -o bin/mock-backend.exe ./cmd/mock-backend
go build -o bin/loadgen.exe ./cmd/loadgen
Write-Host "  -> Binaries compiled into bin/" -ForegroundColor Green

# 2. Start Mock Backends
Write-Host "[2/6] Starting mock backends (port 9001, port 9002)..." -ForegroundColor Yellow
$procB1 = Start-Process -FilePath ".\bin\mock-backend.exe" -ArgumentList "-id backend-1 -port 9001" -PassThru -NoNewWindow
$procB2 = Start-Process -FilePath ".\bin\mock-backend.exe" -ArgumentList "-id backend-2 -port 9002" -PassThru -NoNewWindow

# 3. Start SentinelProxy
Write-Host "[3/6] Starting SentinelProxy gateway (port 8080)..." -ForegroundColor Yellow
$procGW = Start-Process -FilePath ".\bin\sentinelproxy.exe" -ArgumentList "-config configs/sentinelproxy.local.yaml" -PassThru -NoNewWindow

function Cleanup {
    Write-Host ""
    Write-Host "Stopping processes..." -ForegroundColor Yellow
    if ($procGW -and !$procGW.HasExited) { Stop-Process -Id $procGW.Id -Force -ErrorAction SilentlyContinue }
    if ($procB1 -and !$procB1.HasExited) { Stop-Process -Id $procB1.Id -Force -ErrorAction SilentlyContinue }
    if ($procB2 -and !$procB2.HasExited) { Stop-Process -Id $procB2.Id -Force -ErrorAction SilentlyContinue }
    Write-Host "All demo processes stopped cleanly." -ForegroundColor Green
}

try {
    # 4. Wait for Gateway readiness
    Write-Host "[4/6] Waiting for SentinelProxy /healthz..." -ForegroundColor Yellow
    $ready = $false
    for ($i = 0; $i -lt 15; $i++) {
        Start-Sleep -Milliseconds 300
        try {
            $resp = Invoke-WebRequest -Uri "http://127.0.0.1:8080/healthz" -UseBasicParsing -TimeoutSec 1
            if ($resp.StatusCode -eq 200) { $ready = $true; break }
        } catch { }
    }
    if (!$ready) { throw "Gateway failed to become healthy within 5 seconds" }
    Write-Host "  -> Gateway is healthy and accepting traffic on http://127.0.0.1:8080" -ForegroundColor Green

    # 5. Run Live Demonstration
    Write-Host ""
    Write-Host "[5/6] Step A: Healthy traffic load-balancing..." -ForegroundColor Yellow
    for ($i = 1; $i -le 6; $i++) {
        $resp = Invoke-RestMethod -Uri "http://127.0.0.1:8080/api/payments/echo" -Method Get
        Write-Host "  Req $i -> Status 200 OK | Backend: $($resp.backend_id) | Method: $($resp.method)" -ForegroundColor Gray
    }

    Write-Host ""
    Write-Host "Step B: Injecting faults into backend-1 [100% 500 error]..." -ForegroundColor Yellow
    $faultBody = '{"failure_rate":1.0, "failure_status":500, "seed":42}'
    Invoke-RestMethod -Uri "http://127.0.0.1:9001/-/faults" -Method Post -Body $faultBody -ContentType "application/json" | Out-Null
    Write-Host "  -> backend-1 injected with 500 Internal Server Error" -ForegroundColor Red

    Write-Host ""
    Write-Host "Step C: Observing Circuit Breaker state change and automatic failover..." -ForegroundColor Yellow
    for ($i = 1; $i -le 10; $i++) {
        try {
            $resp = Invoke-RestMethod -Uri "http://127.0.0.1:8080/api/payments/echo" -Method Get -TimeoutSec 2
            Write-Host "  Req $i -> Status 200 OK | Backend: $($resp.backend_id) [surviving candidate]" -ForegroundColor Green
        } catch {
            Write-Host "  Req $i -> Request failed / circuit breaker tripped" -ForegroundColor Magenta
        }
    }

    Write-Host ""
    Write-Host "Step D: Recovering backend-1..." -ForegroundColor Yellow
    Invoke-RestMethod -Uri "http://127.0.0.1:9001/-/reset" -Method Post -ContentType "application/json" | Out-Null
    Write-Host "  -> backend-1 reset to healthy. Waiting for breaker cooldown..." -ForegroundColor Gray
    Start-Sleep -Seconds 6

    Write-Host ""
    Write-Host "Step E: Verifying Half-Open probe pass and full circuit closure..." -ForegroundColor Yellow
    for ($i = 1; $i -le 6; $i++) {
        $resp = Invoke-RestMethod -Uri "http://127.0.0.1:8080/api/payments/echo" -Method Get
        Write-Host "  Req $i -> Status 200 OK | Backend: $($resp.backend_id)" -ForegroundColor Green
    }

    # 6. Prometheus Metrics Sample
    Write-Host ""
    Write-Host "[6/6] Inspecting live Prometheus metrics (/metrics)..." -ForegroundColor Yellow
    $metrics = (Invoke-WebRequest -Uri "http://127.0.0.1:8080/metrics" -UseBasicParsing).Content
    $lines = $metrics -split "`r?`n"
    $matching = $lines | Where-Object { $_ -match "^sentinelproxy_requests_total|^sentinelproxy_circuit_breaker" }
    $matching | Select-Object -First 10 | ForEach-Object { Write-Host "  $_" -ForegroundColor DarkCyan }

    Write-Host ""
    Write-Host "===================================================================" -ForegroundColor Cyan
    Write-Host " Live Demo Completed Successfully!" -ForegroundColor Green
    Write-Host "===================================================================" -ForegroundColor Cyan
}
finally {
    Cleanup
}
