#!/usr/bin/env bash
set -euo pipefail

BACKEND="${1:-http://127.0.0.1:9001}"
ACTION="${2:-fault}" # "fault" or "reset"
FAILURE_RATE="${3:-1.0}"
STATUS_CODE="${4:-500}"
DELAY_MS="${5:-0}"

if [ "$ACTION" = "reset" ]; then
    echo "Resetting $BACKEND to healthy..."
    curl -s -X POST "$BACKEND/-/reset" -H "Content-Type: application/json"
    echo ""
    exit 0
fi

echo "Injecting fault into $BACKEND..."
curl -s -X POST "$BACKEND/-/faults" \
  -H "Content-Type: application/json" \
  -d "{\"failure_rate\":$FAILURE_RATE, \"failure_status\":$STATUS_CODE, \"delay_ms\":$DELAY_MS, \"seed\":42}"
echo ""
