#!/usr/bin/env bash
# Nightly ClickHouse backup to the off-box SFTP target (Hetzner Storage Box).
# Intended to run from cron. Reads config/secrets from the repo .env beside it.
# Creates a full remote backup; old ones are pruned per BACKUPS_TO_KEEP_REMOTE.
set -euo pipefail
cd "$(dirname "$0")/.."
set -a; source .env; set +a
NAME="daily-$(date +%Y%m%d-%H%M%S)"
docker compose -f docker-compose.prod.yml --profile backup run --rm clickhouse-backup create_remote "$NAME"
echo "backup $NAME complete"
