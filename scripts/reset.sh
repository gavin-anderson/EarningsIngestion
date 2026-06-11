#!/usr/bin/env bash
set -euo pipefail
docker compose down -v
docker compose up -d
echo "Waiting for ClickHouse..."
until docker exec clickhouse clickhouse-client --query "SELECT 1" >/dev/null 2>&1; do sleep 1; done
./scripts/migrate.sh
