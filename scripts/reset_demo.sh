#!/usr/bin/env bash
set -euo pipefail

echo "Resetting mock backends..."
curl -s -X POST http://127.0.0.1:9001/-/reset -H "Content-Type: application/json" || true
curl -s -X POST http://127.0.0.1:9002/-/reset -H "Content-Type: application/json" || true
echo "All mock backends reset to healthy."
