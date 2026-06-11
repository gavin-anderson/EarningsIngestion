# hl-ingest

Hyperliquid perp market-data ingest into a local ClickHouse instance.

Scope: ~48 publicly-listed-company equity perps from Hyperliquid's **xyz**
HIP-3 builder-deployed dex. The set is defined in [`markets.yaml`](markets.yaml)
and managed by hand — a daily discovery loop alerts on additions/removals but
never auto-subscribes. User-level data (positions, account value, PNL) is
**out of scope**.

## Multi-coin scope

Hyperliquid has two distinct perp universes:

| Universe | Meta endpoint | Contents |
| --- | --- | --- |
| Main perp dex | `{"type":"meta"}` | ~230 crypto perps |
| **xyz** (HIP-3) | `{"type":"meta","dex":"xyz"}` | ~87 perps: equity, FX, commodities |

All equity perps live on the **xyz** dex. Coin symbols on xyz are prefixed:
`xyz:NVDA`, `xyz:AAPL`, etc. — that is the literal API symbol used in REST
and WS calls, not a display convention.

We ingest only the allowlist defined in `markets.yaml`. The daily discovery
loop fetches the live xyz universe, diffs it against the allowlist, and logs:
- `new_in_live_not_in_allowlist` — new tickers to consider adding (edit `markets.yaml` by hand)
- `in_allowlist_not_in_live` — allowlist entries that disappeared from xyz (possible delisting)

Other HIP-3 dexes (cash, flx, km, hyna, …) are out of scope for now.

## What gets ingested

| Source | Channel / endpoint | Destination table |
| --- | --- | --- |
| WS | `activeAssetCtx` per coin | `hl.asset_ctx` |
| WS | `candle` per `(coin, interval)` for 1m, 5m, 15m, 1h, 4h, 1d | `hl.candles` |
| REST | `meta dex=xyz` (startup + hourly refresh) | `hl.perp_meta` |
| REST (backfill) | `candleSnapshot` | `hl.candles` |
| REST (backfill) | `fundingHistory` | `hl.funding_history` |

Trades are explicitly **not** ingested.

All tables include a `dex` column. For now every row has `dex='xyz'`.

## Dev loop

```bash
make install-tools   # one-time: install air for hot reload
make dev             # docker up + migrate-if-needed + hot-reload ingest
```

`make dev` is the npm-`dev`-style one-shot: it brings up ClickHouse, applies
the schema if the `hl` database is missing, then runs the ingest service
under [air](https://github.com/air-verse/air) so .go file saves trigger an
automatic rebuild + restart.

Other primary targets:

| Target | What it does |
| --- | --- |
| `make build` | Compile both binaries to `./bin/`. |
| `make start` | Run the compiled `bin/ingest` (run `make build` first). |
| `make test` | `go test ./...` |
| `make clean` | Delete `bin/` and `tmp/`. |
| `make verify` | Sanity-check row counts per dex in CH. |
| `make backfill COIN=NVDA` | Single-coin historical loader (6mo default). |
| `make backfill ALL=1` | All-markets historical loader (~4h overnight). |

Lower-level building blocks: `make up`, `make down`, `make migrate`,
`make reset`, `make run`, `make tidy`.

Open metrics while running: `curl localhost:9090/metrics | grep hl_ingest`.

## Layout

```
hl-ingest/
├── docker-compose.yml
├── schema.sql
├── markets.yaml                 # curated (dex, coin) allowlist
├── Makefile
├── .air.toml
├── .env.example
├── go.mod
├── scripts/
│   ├── dev-up.sh
│   ├── migrate.sh
│   ├── reset.sh
│   └── verify.sh                # per-dex row counts
├── internal/
│   ├── clickhouse/client.go
│   ├── config/config.go         # adds MARKETS_PATH env var
│   ├── discovery/discovery.go   # daily universe diff loop
│   ├── markets/markets.go       # load + validate markets.yaml
│   ├── hyperliquid/
│   │   ├── types.go
│   │   ├── ws.go
│   │   └── rest.go              # MetaDex for HIP-3 universe fetch
│   └── metrics/metrics.go       # adds hl_ingest_subs_active{dex,coin}
└── cmd/
    ├── ingest/main.go           # live service, all 48 markets
    └── backfill/main.go         # --all flag, per-coin checkpointing
```

## Ingest service behavior

* Connects to ClickHouse at `localhost:9000`, db `hl`. Pings + probes the schema
  on startup. If either fails, logs a clear error and exits non-zero.
* Loads `markets.yaml` on startup (fatal if missing or invalid).
* Fetches xyz `meta` on startup and every hour in a background goroutine.
* Runs the discovery loop on startup and every 24h.
* Subscribes to `activeAssetCtx` + 6 candle intervals per market (48 × 7 = **336 subscriptions**; well within the 1,000-sub-per-IP WS cap).
* Two batchers (one per table): flush on **1000 rows OR 1 second**, whichever comes first.
* On WS read error: reconnect with exponential backoff (1s → 60s, resets on first successful frame). Re-subscribes everything on each reconnect.
* On SIGINT/SIGTERM: cancels root context, flushes both batchers, closes WS and DB, exits 0.

### Metrics (`:9090/metrics`)

| Metric | Type | Labels |
| --- | --- | --- |
| `hl_ingest_messages_total` | counter | `channel` |
| `hl_ingest_rows_inserted_total` | counter | `table` |
| `hl_ingest_insert_errors_total` | counter | `table` |
| `hl_ingest_insert_duration_seconds` | histogram | `table` |
| `hl_ingest_ws_reconnects_total` | counter | — |
| `hl_ingest_buffer_depth` | gauge | `table` |
| `hl_ingest_subs_active` | gauge | `dex`, `coin` |

## Backfill

```bash
# Single coin (6mo default since):
go run ./cmd/backfill --coin=NVDA [--dex=xyz] [--since=2025-12-07]

# All markets in markets.yaml (~4h at 100ms/request throttle):
go run ./cmd/backfill --all [--since=2025-12-07]

# Via make:
make backfill COIN=NVDA SINCE=2025-12-07
make backfill ALL=1
make backfill ALL=1 SINCE=2025-12-07

# Flags:
#   --coin       bare ticker (required unless --all)
#   --dex        dex name (default: xyz)
#   --all        iterate every entry in markets.yaml
#   --since      start time: YYYY-MM-DD or RFC3339 (default: 6 months ago)
#   --until      end time (default: now)
#   --candles    backfill candles (default true)
#   --funding    backfill funding history (default true)
#   --intervals  candle intervals (default "1m,5m,15m,1h,4h,1d")
```

Pages REST requests in 5000-of-interval-unit chunks (HL's per-response cap),
inserts in batches of 1000, retries 429/5xx with exponential backoff.

**Per-coin checkpointing:** at the start of each `(dex, coin, interval)` the
backfill queries `max(open_ts)` from `hl.candles` and skips already-ingested
ranges. Same for funding. Killing and restarting `--all` is safe — it picks
up where it left off per coin per interval.

`ReplacingMergeTree` on `(dex, coin, interval, open_ts)` and `(dex, coin, ts)`
makes re-running the backfill idempotent regardless.

### Rate-limit budget (47 markets)

| Phase | Subscriptions / requests | Notes |
| --- | --- | --- |
| Live WS | 48 × 7 = 336 subs | Under 1,000-sub cap; single connection |
| Backfill per coin | ~6,000 weight ≈ 5 min of REST budget | 1m is the heavy interval |
| Backfill all 48 | ~4 hours single-threaded | Good overnight job |

## Markets allowlist

Edit `markets.yaml` to add or remove markets. Format:

```yaml
markets:
  - dex: xyz
    coin: NVDA   # bare ticker; API symbol = xyz:NVDA
  - dex: xyz
    coin: AAPL
  # ...
```

The `coin` field is the bare ticker. The full API symbol (`xyz:NVDA`) is
constructed internally as `dex + ":" + coin`. Adding a market takes effect on
the next service restart; the discovery loop will stop reporting it as
`new_in_live_not_in_allowlist`.

### Off-hours oracle behavior

Equity perps trade 24/7 on Hyperliquid but the underlying cash markets don't.
Oracle prices and funding rates can be noisy on weekends and after-hours as the
oracle relies on external price feeds with reduced liquidity. This is expected
behavior — no quality flag is added yet.

## Configuration

Runtime config is loaded from `.env` in the working directory (if present)
plus environment variables. Shell env vars take precedence over `.env`.

Copy the template to start:

```bash
cp .env.example .env
# then edit .env
```

| Variable | Default | Notes |
| --- | --- | --- |
| `HL_REST_URL` | `https://api.hyperliquid.xyz/info` | Set to a geo proxy `/info` URL when running outside an HL-allowed region. |
| `HL_WS_URL` | `wss://api.hyperliquid.xyz/ws` | Set to a WS proxy URL when one is available. |
| `MARKETS_PATH` | `markets.yaml` | Path to the allowlist file. |
| `CH_ADDR` | `localhost:9000` | ClickHouse native protocol host:port. |
| `CH_DATABASE` | `hl` | — |
| `CH_USERNAME` | `default` | — |
| `CH_PASSWORD` | (empty) | — |
| `LOG_LEVEL` | `info` | `debug`, `info`, `warn`, `error`. |

`.env` is gitignored; `.env.example` is the committed template.

## Dependencies

* [`github.com/ClickHouse/clickhouse-go/v2`](https://github.com/ClickHouse/clickhouse-go) — native CH driver
* [`github.com/coder/websocket`](https://github.com/coder/websocket) — WS client
* [`github.com/prometheus/client_golang`](https://github.com/prometheus/client_golang) — metrics
* [`github.com/joho/godotenv`](https://github.com/joho/godotenv) — `.env` loading
* [`gopkg.in/yaml.v3`](https://pkg.go.dev/gopkg.in/yaml.v3) — markets.yaml parsing

After cloning, run `make tidy` to populate indirect dependencies.

## Out of scope (intentional)

* Other HIP-3 dexes (cash, flx, km, hyna, …) — xyz only.
* Auto-subscribe to new markets — human approval via `markets.yaml` edit only.
* Pre-IPO names (SPACEX, OPENAI, ANTHROPIC) — not on xyz.
* Cross-dex basis / funding analysis — downstream consumer concern.
* Gap detection / auto-backfill on reconnect — backfill stays manual.
* User-level data (positions, PNL) — fetched live by the app.
* Auth, TLS, production deploy — local dev only.
* Migration tooling — `schema.sql` + drop-and-recreate is fine.
* Trades ingestion.
* Multi-IP sharding for backfill — single IP is enough at ~4h.
