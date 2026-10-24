#!/usr/bin/env bash
set -euo pipefail

echo "==================================================================="
echo " SentinelProxy — Resilient API Gateway Live Demo"
echo "==================================================================="

# 1. Build Binaries
echo "[1/6] Building project binaries..."
mkdir -p bin
go build -o bin/sentinelproxy ./cmd/sentinelproxy
go build -o bin/mock-backend ./cmd/mock-backend
go build -o bin/loadgen ./cmd/loadgen

# 2. Start Mock Backends
echo "[2/6] Starting mock backends (port 9001, port 9002)..."
./bin/mock-backend -id backend-1 -port 9001 &
PID_B1=$!
./bin/mock-backend -id backend-2 -port 9002 &
PID_B2=$!

# 3. Start SentinelProxy
echo "[3/6] Starting SentinelProxy gateway (port 8080)..."
./bin/sentinelproxy -config configs/sentinelproxy.local.yaml &
PID_GW=$!

cleanup() {
    echo -e "\nStopping processes..."
    kill -TERM "$PID_GW" "$PID_B1" "$PID_B2" 2>/dev/null || true
    wait "$PID_GW" "$PID_B1" "$PID_B2" 2>/dev/null || true
    echo "All demo processes stopped cleanly."
}
trap cleanup EXIT

# 4. Wait for Gateway readiness
echo "[4/6] Waiting for SentinelProxy /healthz..."
for i in {1..15}; do
    if curl -s -f http://127.0.0.1:8080/healthz >/dev/null 2>&1; then
        echo "  -> Gateway is healthy and ready on http://127.0.0.1:8080"
        break
    fi
    sleep 0.3
done

# 5. Run Demonstration
echo -e "\n[5/6] Step A: Healthy traffic load-balancing..."
for i in {1..6}; do
    curl -s http://127.0.0.1:8080/api/payments/echo | grep -o '"backend_id":"[^"]*"' || true
done

echo -e "\nStep B: Injecting faults into backend-1 (100% 500 error)..."
curl -s -X POST http://127.0.0.1:9001/-/faults \
  -H "Content-Type: application/json" \
  -d '{"failure_rate":1.0, "failure_status":500, "seed":42}'

echo -e "\nStep C: Observing Circuit Breaker state change and automatic failover..."
for i in {1..10}; do
    curl -s -w " | Status: %{http_code}\n" http://127.0.0.1:8080/api/payments/echo || true
done

echo -e "\nStep D: Recovering backend-1..."
curl -s -X POST http://127.0.0.1:9001/-/reset -H "Content-Type: application/json"
echo "  -> Waiting for open_duration cooldown (6s)..."
sleep 6

echo -e "\nStep E: Verifying Half-Open probe pass & full circuit closure..."
for i in {1..6}; do
    curl -s http://127.0.0.1:8080/api/payments/echo | grep -o '"backend_id":"[^"]*"' || true
done

# 6. Prometheus Metrics Sample
echo -e "\n[6/6] Inspecting live Prometheus metrics (/metrics)..."
curl -s http://127.0.0.1:8080/metrics | grep -E '^sentinelproxy_requests_total|^sentinelproxy_circuit_breaker' | head -n 10

echo "==================================================================="
echo " Live Demo Completed Successfully!"
echo "==================================================================="
