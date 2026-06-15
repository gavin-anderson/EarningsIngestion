# Runbook

Operational guide for the hl-ingest production stack. Each alert from Grafana
Cloud (delivered by email) has a section below: what it means, what usually
causes it, how to confirm, and how to fix.

> **Connection details** — box addresses, SSH keys, and credentials are **not**
> in this repo. They live in your password manager and your local ops reference.
> This runbook only covers *what to do*, not *how to log in*.

## Architecture / data path

```
Hyperliquid API
   │  (HTTPS + WSS)
EU edge box  — caddy reverse proxy, TLS + X-Proxy-Key auth
   │  (HTTPS + WSS over the public internet)
US data-plane box — hl-ingest (systemd) ──▶ ClickHouse (Docker)
   │  (Prometheus metrics)
Grafana Cloud — dashboards + alerts (email)
```

**Triage principle: work the data path backwards.** When data stops, check in
order: is the **ingester** running? → is **ClickHouse** up? → is the **edge
proxy** reachable? → is **Hyperliquid** itself up?

Two boxes:
- **EU edge** — the caddy reverse proxy. The only thing that talks to Hyperliquid.
- **US data-plane** — runs the `hl-ingest` systemd service and ClickHouse (Docker). Reachable only over Tailscale.

---

## Alert: hl-ingest no messages

**Symptom:** no WebSocket frames ingested for 5+ minutes (`rate(hl_ingest_messages_total)` ≈ 0).

**Likely causes** (most common first):
1. WebSocket connection dropped and isn't recovering.
2. The EU edge proxy (caddy) is down or misconfigured.
3. Hyperliquid API outage, or it started geo-blocking the proxy.
4. Network path between the boxes broke.

**Diagnose** (on the US data-plane box):
```bash
sudo journalctl -u hl-ingest -n 100      # look for repeating "ws dial ... error"
curl -s http://127.0.0.1:9090/metrics | grep hl_ingest_messages_total
```
Then test the edge proxy from your laptop (PowerShell) — a keyed request should
return the Hyperliquid universe JSON; a timeout/error points at the proxy or HL.

**Remediate:**
- First, restart the ingester: `sudo systemctl restart hl-ingest`.
- If logs show proxy/connection errors → check caddy on the **EU box**
  (`systemctl status caddy`, `journalctl -u caddy -f`); restart it if needed.
- If the proxy is healthy but HL returns errors → likely a Hyperliquid-side
  outage; confirm at status.hyperliquid.xyz and wait it out. Ingestion resumes
  automatically once upstream recovers (the WS loop retries with backoff).

---

## Alert: hl-ingest down

**Symptom:** the metrics scrape fails (`up{job="hl-ingest"} == 0`) — the process isn't running.

**Likely causes:**
1. The service crashed (and may be in a restart loop).
2. Out-of-memory kill.
3. The box rebooted.
4. A bad deploy shipped a broken binary or config.

**Diagnose:**
```bash
systemctl status hl-ingest --no-pager     # state + recent log lines
sudo journalctl -u hl-ingest -n 100       # why it exited
dmesg -T | grep -i "killed process"       # OOM?
```
A constantly-changing Main PID = crash loop.

**Remediate:**
- `sudo systemctl restart hl-ingest`.
- **Crash loop on startup** is almost always config: check `/etc/hl-ingest/.env`
  (proxy URLs, `HL_PROXY_KEY`, `CH_PASSWORD`) and that ClickHouse is up.
- **Bad deploy:** the last green deploy's binary is the prior one — revert the
  offending commit on `main` and let CI redeploy, or rebuild from a known-good
  commit.
- **OOM:** check what's consuming memory (`free -h`, `docker stats`); restart the
  heavy process.

---

## Alert: insert errors elevated

**Symptom:** `hl_ingest_insert_errors_total` rising (> 0.1/s) — ClickHouse is rejecting writes.

**Likely causes:**
1. ClickHouse container is down or unhealthy.
2. Disk full (ClickHouse refuses writes).
3. Schema mismatch after a change.

**Diagnose:**
```bash
docker ps                                  # is the "clickhouse" container Up?
docker logs clickhouse --tail 100          # errors: disk, "too many parts", memory
df -h                                       # disk full?
cd ~/hl-ingest && set -a; source .env; set +a && bash scripts/verify.sh
```

**Remediate:**
- ClickHouse down → `docker compose -f docker-compose.prod.yml up -d`.
- Disk full → see **disk filling** below.
- Schema issue → compare `schema.sql` with the live tables; only re-migrate
  deliberately (`scripts/migrate.sh` is destructive — it drops the `hl` db).

---

## Alert: buffer backing up

**Symptom:** `hl_ingest_buffer_depth` > 100 — the batchers can't drain into ClickHouse fast enough.

**Likely causes:**
1. ClickHouse is slow or stalled (usually the same root cause as insert errors).
2. "Too many parts" — inserts are too frequent / merges falling behind.

**Diagnose:**
```bash
docker stats clickhouse --no-stream        # CPU/IO pressure
docker logs clickhouse --tail 100          # look for "Too many parts"
df -h
```

**Remediate:**
- If ClickHouse is wedged → `docker compose -f docker-compose.prod.yml restart clickhouse`.
- "Too many parts" → usually transient as background merges catch up; if
  persistent, it indicates inserts are too frequent (a code-level batching tune).
- A brief backlog that self-clears after a ClickHouse hiccup is normal.

---

## Alert: disk filling

**Symptom:** root filesystem under 20% free.

**Likely causes:**
1. ClickHouse data growth (`asset_ctx` grows continuously).
2. Docker image/log/volume buildup.
3. Log files (journald, caddy, backup logs).

**Diagnose:**
```bash
df -h
docker system df                           # Docker's footprint
du -xhd1 /var/lib/docker | sort -h | tail
```

**Remediate:**
- Reclaim Docker space: `docker system prune -f` (safe; removes dangling images/caches).
- **The real fix for ongoing growth is a retention policy (TTL)** on `asset_ctx`,
  not cleanup. Backups do *not* reduce growth. Add a TTL to cap history, e.g.
  keep N months and let ClickHouse drop older partitions.
- Short term, the box disk can be resized up in the Hetzner console.

---

## Common operations

```bash
# Restart the ingester
sudo systemctl restart hl-ingest

# Restart ClickHouse (data persists in the Docker volume)
cd ~/hl-ingest && set -a; source .env; set +a
docker compose -f docker-compose.prod.yml restart clickhouse

# Deploy code: push to main — GitHub Actions builds, ships, and restarts.
# Pull config-only changes (compose/schema/markets) onto the box:
cd ~/hl-ingest && git pull
```

---

## Disaster recovery — restore from backup

Backups run nightly to off-box storage (Hetzner Storage Box) via
`clickhouse-backup`. To restore after data loss or a rebuilt box:

```bash
cd ~/hl-ingest
set -a; source .env; set +a

# 1. Make sure ClickHouse is running
docker compose -f docker-compose.prod.yml up -d

# 2. Find the backup to restore
docker compose -f docker-compose.prod.yml --profile backup run --rm clickhouse-backup list remote

# 3. Download + restore it (schema + data). Stop hl-ingest first to avoid
#    concurrent writes during the restore:
sudo systemctl stop hl-ingest
docker compose -f docker-compose.prod.yml --profile backup run --rm \
  clickhouse-backup restore_remote <backup-name>
sudo systemctl start hl-ingest
```

After restore, `scripts/verify.sh` should show the expected row counts, and live
ingestion resumes forward from "now." Note `candles` and `funding_history` are
also re-derivable via `hl-backfill --all` if a backup is unavailable; `asset_ctx`
(point-in-time snapshots) is only recoverable from backup.

---

## Escalation

This is a single-operator, single-box system with no HA. "Escalation" means:
1. Don't panic — most failures self-recover (WS reconnects, service auto-restart).
2. The data path is short; isolate which hop broke using the triage principle.
3. Worst case (box lost): provision a new box, restore from backup, re-point DNS
   if the proxy box changed.
