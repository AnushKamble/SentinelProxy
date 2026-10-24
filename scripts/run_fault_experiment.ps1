param(
    [string]$Backend = "http://127.0.0.1:9001",
    [double]$FailureRate = 1.0,
    [int]$StatusCode = 500,
    [int]$DelayMs = 0,
    [switch]$Hang,
    [switch]$Reset
)

$ErrorActionPreference = "Stop"

if ($Reset) {
    Write-Host "Resetting backend at $Backend to healthy state..." -ForegroundColor Green
    $resp = Invoke-RestMethod -Uri "$Backend/-/reset" -Method Post -ContentType "application/json"
    Write-Host "Response: $($resp | ConvertTo-Json -Compress)" -ForegroundColor Cyan
    exit 0
}

$hangVal = if ($Hang) { $true } else { $false }
$payload = @{
    failure_rate   = $FailureRate
    failure_status = $StatusCode
    delay_ms       = $DelayMs
    hang           = $hangVal
    seed           = 42
} | ConvertTo-Json -Compress

Write-Host "Injecting fault into $Backend..." -ForegroundColor Yellow
Write-Host "Payload: $payload" -ForegroundColor Gray

$resp = Invoke-RestMethod -Uri "$Backend/-/faults" -Method Post -Body $payload -ContentType "application/json"
Write-Host "Fault injected successfully: $($resp | ConvertTo-Json -Compress)" -ForegroundColor Green
