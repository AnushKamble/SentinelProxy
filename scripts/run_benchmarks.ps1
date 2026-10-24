# Benchmark Execution Script
# Runs Go microbenchmarks and the 4 controlled experiments matrix (5 repetitions per condition)

$ErrorActionPreference = "Stop"

Write-Host "===================================================================" -ForegroundColor Cyan
Write-Host " SentinelProxy — Benchmark & Controlled Experiment Suite" -ForegroundColor Cyan
Write-Host "===================================================================" -ForegroundColor Cyan

# 1. Microbenchmarks
Write-Host "`n[1/2] Running Go microbenchmarks (router, circuit breaker, rate limiter)..." -ForegroundColor Yellow
go test -bench . -benchmem -run "^$" ./bench

# 2. Controlled Experiment Suite (5 runs per condition)
Write-Host "`n[2/2] Running 4 Controlled Experiment Conditions (A, B, C, D)..." -ForegroundColor Yellow
go test -v ./bench -run TestExperiments_FullEvaluationMatrix

Write-Host "`nBenchmark outputs generated in results/benchmarks/:" -ForegroundColor Green
Get-ChildItem -Path "results/benchmarks" | Format-Table -AutoSize
