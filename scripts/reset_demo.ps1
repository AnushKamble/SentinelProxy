# Reset Mock Backends to healthy operation
$ErrorActionPreference = "SilentlyContinue"

Write-Host "Resetting mock backend 1 (9001)..." -ForegroundColor Yellow
Invoke-RestMethod -Uri "http://127.0.0.1:9001/-/reset" -Method Post -ContentType "application/json" | Out-Null

Write-Host "Resetting mock backend 2 (9002)..." -ForegroundColor Yellow
Invoke-RestMethod -Uri "http://127.0.0.1:9002/-/reset" -Method Post -ContentType "application/json" | Out-Null

Write-Host "All mock backends reset to healthy." -ForegroundColor Green
