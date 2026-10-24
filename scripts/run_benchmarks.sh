#!/usr/bin/env bash
set -euo pipefail

echo "==================================================================="
echo " SentinelProxy — Benchmark & Controlled Experiment Suite"
echo "==================================================================="

echo -e "\n[1/2] Running Go microbenchmarks..."
go test -bench . -benchmem -run "^$" ./bench

echo -e "\n[2/2] Running 4 Controlled Experiment Conditions (A, B, C, D)..."
go test -v ./bench -run TestExperiments_FullEvaluationMatrix

echo -e "\nBenchmark outputs generated in results/benchmarks/:"
ls -la results/benchmarks/
