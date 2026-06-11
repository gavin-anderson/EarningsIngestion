// cmd/backfill is a one-shot loader for historical candles.
// It is NOT auto-run by the ingest service -- invoke explicitly:
//
//	go run ./cmd/backfill --coin=NVDA [--dex=xyz] [--since=2025-12-07]
//	go run ./cmd/backfill --all [--since=2025-12-07]
//
// Hyperliquid caps each REST candleSnapshot response at ~5000 rows, so we
// page in chunks sized to the interval. Inserts go to ClickHouse in batches
// of 1000.
//
// Per-(dex,coin,interval) checkpointing: on startup for each coin the backfill
// queries max(open_ts) from hl.candles and skips already-ingested ranges.
package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	chdriver "github.com/ClickHouse/clickhouse-go/v2/lib/driver"

	"hl-ingest/internal/clickhouse"
	"hl-ingest/internal/config"
	"hl-ingest/internal/hyperliquid"
	"hl-ingest/internal/markets"
)

const (
	insertBatchSize = 1000

	// Each candleSnapshot response is capped at ~5000 candles, so chunk by
	// exactly that many intervals.
	maxCandlesPerPage = 5000
)

func main() {
	cfg := config.Load()
	log := setupLogger(cfg.LogLevel)
	if err := run(log, cfg); err != nil {
		log.Error("backfill failed", "err", err)
		os.Exit(1)
	}
}

func setupLogger(level string) *slog.Logger {
	lvl := slog.LevelInfo
	switch strings.ToLower(level) {
	case "debug":
		lvl = slog.LevelDebug
	case "warn":
		lvl = slog.LevelWarn
	case "error":
		lvl = slog.LevelError
	}
	h := slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: lvl})
	l := slog.New(h)
	slog.SetDefault(l)
	return l
}

func run(log *slog.Logger, cfg config.Config) error {
	defaultSince := time.Now().UTC().AddDate(0, -6, 0).Format("2006-01-02")

	var (
		coinFlag     = flag.String("coin", "", "Bare coin ticker to backfill (e.g. NVDA). Required unless --all is set.")
		dexFlag      = flag.String("dex", "xyz", "Dex for the coin (default: xyz)")
		allFlag      = flag.Bool("all", false, "Backfill all markets in markets.yaml")
		sinceStr     = flag.String("since", defaultSince, "Start time: YYYY-MM-DD or RFC3339 (default: 6 months ago)")
		untilStr     = flag.String("until", "", "End time: YYYY-MM-DD or RFC3339 (default: now)")
		doCandles    = flag.Bool("candles", true, "Backfill candles")
		intervalsCSV = flag.String("intervals", "1m,5m,15m,1h,4h,1d", "Comma-separated candle intervals")
	)
	flag.Parse()

	if !*allFlag && *coinFlag == "" {
		flag.Usage()
		return fmt.Errorf("--coin or --all is required")
	}

	since, err := parseTime(*sinceStr)
	if err != nil {
		return fmt.Errorf("--since: %w", err)
	}
	until := time.Now().UTC()
	if *untilStr != "" {
		until, err = parseTime(*untilStr)
		if err != nil {
			return fmt.Errorf("--until: %w", err)
		}
	}
	if !until.After(since) {
		return fmt.Errorf("--until (%s) must be after --since (%s)", until.Format(time.RFC3339), since.Format(time.RFC3339))
	}

	ivs := splitCSV(*intervalsCSV)
	if *doCandles && len(ivs) == 0 {
		return fmt.Errorf("--intervals is empty")
	}

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	chCfg := clickhouse.Config{
		Addr:     cfg.CHAddr,
		Database: cfg.CHDatabase,
		Username: cfg.CHUsername,
		Password: cfg.CHPassword,
	}
	conn, err := clickhouse.Open(ctx, chCfg)
	if err != nil {
		return err
	}
	defer conn.Close()
	if err := clickhouse.VerifySchema(ctx, conn); err != nil {
		return err
	}

	rest := hyperliquid.NewRESTClient(cfg.HLRESTURL)

	// Build the list of markets to backfill.
	var mks []markets.Market
	if *allFlag {
		mks, err = markets.Load(cfg.MarketsPath)
		if err != nil {
			return fmt.Errorf("load markets: %w", err)
		}
		log.Info("backfill --all", "markets", len(mks), "since", since.Format(time.RFC3339), "until", until.Format(time.RFC3339))
	} else {
		mks = []markets.Market{{Dex: *dexFlag, Coin: *coinFlag}}
		log.Info("backfill starting", "rest", cfg.HLRESTURL,
			"dex", *dexFlag, "coin", *coinFlag,
			"since", since.Format(time.RFC3339), "until", until.Format(time.RFC3339))
	}

	overallStart := time.Now()

	for _, m := range mks {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := backfillMarket(ctx, log, rest, conn, m, ivs, since, until, *doCandles); err != nil {
			return fmt.Errorf("backfill %s: %w", m.Symbol(), err)
		}
	}

	log.Info("backfill complete", "markets", len(mks), "elapsed", time.Since(overallStart).Round(time.Millisecond))
	return nil
}

func backfillMarket(ctx context.Context, log *slog.Logger, rest *hyperliquid.RESTClient, conn chdriver.Conn,
	m markets.Market, ivs []string, since, until time.Time, doCandles bool) error {

	log.Info("backfilling market", "dex", m.Dex, "coin", m.Coin,
		"since", since.Format(time.RFC3339), "until", until.Format(time.RFC3339))

	if doCandles {
		for _, iv := range ivs {
			start, err := candleCheckpoint(ctx, conn, m, iv, since)
			if err != nil {
				return fmt.Errorf("checkpoint candles %s: %w", iv, err)
			}
			if !start.Before(until) {
				log.Info("candles already complete (checkpoint >= until)", "dex", m.Dex, "coin", m.Coin, "interval", iv)
				continue
			}
			if err := backfillCandles(ctx, log, rest, conn, m, iv, start, until); err != nil {
				return fmt.Errorf("candles %s: %w", iv, err)
			}
		}
	}
	return nil
}

// candleCheckpoint returns the resume point for (dex, coin, interval).
// If max(open_ts) in CH is after `since`, we skip to that point.
func candleCheckpoint(ctx context.Context, conn chdriver.Conn, m markets.Market, interval string, since time.Time) (time.Time, error) {
	var maxTS time.Time
	row := conn.QueryRow(ctx,
		"SELECT max(open_ts) FROM hl.candles WHERE dex = ? AND coin = ? AND interval = ?",
		m.Dex, m.Coin, interval)
	if err := row.Scan(&maxTS); err != nil {
		return since, nil // no rows yet
	}
	if maxTS.IsZero() {
		return since, nil
	}
	if maxTS.After(since) {
		return maxTS, nil
	}
	return since, nil
}

// ============================================================================
// Candles
// ============================================================================

func backfillCandles(ctx context.Context, log *slog.Logger, rest *hyperliquid.RESTClient, conn chdriver.Conn,
	m markets.Market, interval string, since, until time.Time) error {

	step, err := intervalChunkDuration(interval)
	if err != nil {
		return err
	}
	log.Info("backfilling candles", "dex", m.Dex, "coin", m.Coin, "interval", interval,
		"since", since.Format(time.RFC3339), "until", until.Format(time.RFC3339),
		"chunkHours", int(step.Hours()))

	sym := m.Symbol() // "xyz:NVDA"
	totalFetched, totalInserted := 0, 0
	pageStart := since

	for pageStart.Before(until) {
		if err := ctx.Err(); err != nil {
			return err
		}
		pageEnd := pageStart.Add(step)
		if pageEnd.After(until) {
			pageEnd = until
		}

		t0 := time.Now()
		candles, err := rest.CandleSnapshot(ctx, sym, interval, pageStart.UnixMilli(), pageEnd.UnixMilli())
		if err != nil {
			return fmt.Errorf("page [%s, %s]: %w",
				pageStart.Format(time.RFC3339), pageEnd.Format(time.RFC3339), err)
		}
		totalFetched += len(candles)

		inserted, err := insertCandlesPaged(ctx, conn, m, candles)
		if err != nil {
			return fmt.Errorf("insert page: %w", err)
		}
		totalInserted += inserted

		log.Info("candle page",
			"dex", m.Dex, "coin", m.Coin, "interval", interval,
			"from", pageStart.Format(time.RFC3339), "to", pageEnd.Format(time.RFC3339),
			"fetched", len(candles), "inserted", inserted,
			"elapsed", time.Since(t0).Round(time.Millisecond))

		pageStart = pageEnd
	}

	log.Info("candle backfill done",
		"dex", m.Dex, "coin", m.Coin, "interval", interval,
		"totalFetched", totalFetched, "totalInserted", totalInserted)
	return nil
}

func insertCandlesPaged(ctx context.Context, conn chdriver.Conn, m markets.Market, candles []hyperliquid.Candle) (int, error) {
	if len(candles) == 0 {
		return 0, nil
	}
	total := 0
	for i := 0; i < len(candles); i += insertBatchSize {
		end := i + insertBatchSize
		if end > len(candles) {
			end = len(candles)
		}
		chunk := candles[i:end]
		batch, err := conn.PrepareBatch(ctx,
			"INSERT INTO hl.candles (open_ts, close_ts, dex, coin, interval, open_px, high_px, low_px, close_px, volume, num_trades)")
		if err != nil {
			return total, fmt.Errorf("prepare: %w", err)
		}
		for _, c := range chunk {
			err := batch.Append(
				time.UnixMilli(c.OpenTimeMs).UTC(),
				time.UnixMilli(c.CloseTimeMs).UTC(),
				m.Dex, m.Coin,
				c.Interval,
				float64(c.Open), float64(c.High), float64(c.Low), float64(c.Close),
				float64(c.Volume), c.NumTrades,
			)
			if err != nil {
				return total, fmt.Errorf("append: %w", err)
			}
		}
		if err := batch.Send(); err != nil {
			return total, fmt.Errorf("send: %w", err)
		}
		total += len(chunk)
	}
	return total, nil
}

// ============================================================================
// Helpers
// ============================================================================

func parseTime(s string) (time.Time, error) {
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t.UTC(), nil
	}
	if t, err := time.Parse("2006-01-02", s); err == nil {
		return t.UTC(), nil
	}
	return time.Time{}, fmt.Errorf("not RFC3339 or YYYY-MM-DD: %q", s)
}

func splitCSV(s string) []string {
	out := []string{}
	for _, p := range strings.Split(s, ",") {
		p = strings.TrimSpace(p)
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}

func intervalChunkDuration(interval string) (time.Duration, error) {
	per, err := intervalDuration(interval)
	if err != nil {
		return 0, err
	}
	return time.Duration(maxCandlesPerPage) * per, nil
}

func intervalDuration(interval string) (time.Duration, error) {
	switch interval {
	case "1m":
		return time.Minute, nil
	case "3m":
		return 3 * time.Minute, nil
	case "5m":
		return 5 * time.Minute, nil
	case "15m":
		return 15 * time.Minute, nil
	case "30m":
		return 30 * time.Minute, nil
	case "1h":
		return time.Hour, nil
	case "2h":
		return 2 * time.Hour, nil
	case "4h":
		return 4 * time.Hour, nil
	case "8h":
		return 8 * time.Hour, nil
	case "12h":
		return 12 * time.Hour, nil
	case "1d":
		return 24 * time.Hour, nil
	case "3d":
		return 72 * time.Hour, nil
	case "1w":
		return 7 * 24 * time.Hour, nil
	}
	return 0, fmt.Errorf("unknown interval %q", interval)
}
