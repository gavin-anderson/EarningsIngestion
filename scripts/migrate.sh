#!/usr/bin/env bash
set -euo pipefail
docker exec -i clickhouse clickhouse-client --multiquery < schema.sql
echo "Schema applied."
