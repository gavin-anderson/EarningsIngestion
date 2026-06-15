# hl-ingest

Connects to an external API for time-series data and saves it into ClickHouse.
A personal project — mostly for collecting data and playing around with it.

## Stack

- **Go** — a small service that streams live data and backfills history
- **ClickHouse** — time-series storage
- **Prometheus** metrics (shipped to Grafana Cloud for dashboards + alerts)

## Run it locally

```bash
make dev     # start ClickHouse, apply schema, run the ingester with hot reload
```

Run `make help` for the other targets (build, test, verify, backfill).

## Configuration

Runtime config comes from `.env` (gitignored) plus environment variables:

```bash
cp .env.example .env   # then fill in values
```

See `.env.example` for the available settings.
```
