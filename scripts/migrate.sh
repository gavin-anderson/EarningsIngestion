#!/usr/bin/env bash
set -euo pipefail
# CH_PASSWORD comes from the environment (source .env first in prod). Empty
# string matches the passwordless dev ClickHouse, so this works in both.
docker exec -i clickhouse clickhouse-client --password "${CH_PASSWORD:-}" --multiquery < schema.sql
echo "Schema applied."
