DROP DATABASE IF EXISTS hl;
CREATE DATABASE hl;

-- =========================================================================
-- asset_ctx: live snapshots of mark, oracle, mid, OI, day vol.
-- Append-only. Pushed every few seconds per coin via activeAssetCtx WS sub.
-- (Funding rate and premium fields exist in HL's payload but are not stored.)
-- =========================================================================
CREATE TABLE hl.asset_ctx
(
    ts             DateTime64(3, 'UTC') CODEC(DoubleDelta, ZSTD(3)),
    dex            LowCardinality(String),
    coin           LowCardinality(String),
    mark_px        Float64 CODEC(Gorilla, ZSTD(3)),
    oracle_px      Float64 CODEC(Gorilla, ZSTD(3)),
    mid_px         Nullable(Float64) CODEC(ZSTD(3)),
    open_interest  Float64 CODEC(Gorilla, ZSTD(3)),
    day_volume     Float64 CODEC(Gorilla, ZSTD(3)),
    ingested_at    DateTime64(3, 'UTC') DEFAULT now64(3) CODEC(DoubleDelta, ZSTD(3))
)
ENGINE = MergeTree
PARTITION BY toYYYYMM(ts)
ORDER BY (dex, coin, ts)
SETTINGS index_granularity = 8192;

-- =========================================================================
-- candles: OHLCV per (dex, coin, interval, open_time).
-- ReplacingMergeTree because Hyperliquid pushes updates within an interval --
-- we keep replacing the row for the same (dex, coin, interval, open_ts) until
-- the interval closes. version column = ingested_at so the latest write wins.
-- =========================================================================
CREATE TABLE hl.candles
(
    open_ts        DateTime64(3, 'UTC') CODEC(DoubleDelta, ZSTD(3)),
    close_ts       DateTime64(3, 'UTC') CODEC(DoubleDelta, ZSTD(3)),
    dex            LowCardinality(String),
    coin           LowCardinality(String),
    interval       LowCardinality(String),  -- '1m','5m','15m','1h','4h','1d'
    open_px        Float64 CODEC(Gorilla, ZSTD(3)),
    high_px        Float64 CODEC(Gorilla, ZSTD(3)),
    low_px         Float64 CODEC(Gorilla, ZSTD(3)),
    close_px       Float64 CODEC(Gorilla, ZSTD(3)),
    volume         Float64 CODEC(Gorilla, ZSTD(3)),
    num_trades     UInt32 CODEC(ZSTD(3)),
    ingested_at    DateTime64(3, 'UTC') DEFAULT now64(3) CODEC(DoubleDelta, ZSTD(3))
)
ENGINE = ReplacingMergeTree(ingested_at)
PARTITION BY toYYYYMM(open_ts)
ORDER BY (dex, coin, interval, open_ts)
SETTINGS index_granularity = 8192;

-- =========================================================================
-- perp_meta: static-ish per-coin metadata. Refreshed periodically.
-- ReplacingMergeTree on (dex, coin) keeps only the latest row per market.
-- =========================================================================
CREATE TABLE hl.perp_meta
(
    dex            LowCardinality(String),
    coin           LowCardinality(String),
    sz_decimals    UInt8,
    max_leverage   UInt16,
    only_isolated  UInt8,
    ingested_at    DateTime64(3, 'UTC') DEFAULT now64(3)
)
ENGINE = ReplacingMergeTree(ingested_at)
ORDER BY (dex, coin);
