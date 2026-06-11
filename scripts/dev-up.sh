#!/usr/bin/env bash
# Idempotent dev-environment bring-up: ensure ClickHouse is running, wait
# for it to accept queries, and apply schema.sql if the hl database is
# missing. Safe to run repeatedly -- existing data is not touched.
set -euo pipefail

# 1. Start the container if not already running.
if ! docker ps --format '{{.Names}}' | grep -q '^clickhouse$'; then
  echo "==> Starting ClickHouse..."
  docker compose up -d
else
  echo "==> ClickHouse already running."
fi

# 2. Wait for the server to accept queries.
echo -n "==> Waiting for ClickHouse to be ready"
until docker exec clickhouse clickhouse-client --query "SELECT 1" >/dev/null 2>&1; do
  echo -n "."
  sleep 1
done
echo " ready."

# 3. Apply schema only if the database is missing. EXISTS returns "1" or "0".
exists=$(docker exec clickhouse clickhouse-client --query "EXISTS DATABASE hl" 2>/dev/null || echo "0")
if [ "$exists" != "1" ]; then
  echo "==> Database 'hl' missing -- applying schema."
  ./scripts/migrate.sh
else
  echo "==> Database 'hl' already exists -- skipping migrate."
fi

echo "==> Dev environment ready."
