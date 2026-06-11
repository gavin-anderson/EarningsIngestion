#!/usr/bin/env bash
set -euo pipefail
docker exec -i clickhouse clickhouse-client --query "
  SELECT tbl, dex, rows, coins, first, last
  FROM (
    SELECT 'asset_ctx' AS tbl, dex, count() AS rows, uniqExact(coin) AS coins,
           toString(min(ts)) AS first, toString(max(ts)) AS last
    FROM hl.asset_ctx GROUP BY dex
    UNION ALL
    SELECT 'candles' AS tbl, dex, count() AS rows, uniqExact(coin) AS coins,
           toString(min(open_ts)) AS first, toString(max(open_ts)) AS last
    FROM hl.candles GROUP BY dex
    UNION ALL
    SELECT 'perp_meta' AS tbl, dex, count() AS rows, uniqExact(coin) AS coins,
           '-' AS first, '-' AS last
    FROM hl.perp_meta GROUP BY dex
  )
  ORDER BY tbl, dex
  FORMAT PrettyCompact
"
