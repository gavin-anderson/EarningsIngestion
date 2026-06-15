// cmd/ingest is the live market-data ingest service.
// It subscribes to Hyperliquid's WebSocket feed for activeAssetCtx and candles
// across all markets listed in markets.yaml and streams the data into a local
// ClickHouse instance.
//
// Lifecycle:
//  1. Open + ping CH, probe schema.
//  2. Load markets.yaml allowlist.
//  3. Fetch + upsert xyz perp_meta (and refresh every hour in the background).
//  4. Start discovery loop (daily diff vs live xyz universe).
//  5. Connect WS, subscribe, then read-dispatch-batch until ctx is canceled.
//  6. On SIGINT/SIGTERM: cancel, drain batchers, flush, close.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"

	chdriver "github.com/ClickHouse/clickhouse-go/v2/lib/driver"
	"github.com/prometheus/client_golang/prometheus/promhttp"

	"hl-ingest/internal/clickhouse"
	"hl-ingest/internal/config"
	"hl-ingest/internal/discovery"
	"hl-ingest/internal/hyperliquid"
	"hl-ingest/internal/markets"
	"hl-ingest/internal/metrics"
)

// Candle intervals to subscribe to per market.
var intervals = []string{"1m", "5m", "15m", "1h", "4h", "1d"}

const (
	batchMaxRows     = 1000
	batchMaxInterval = 1 * time.Second
	batchChanBuffer  = 4096

	metricsAddr       = ":9090"
	metaRefreshIn     = 1 * time.Hour
	discoveryInterval = 24 * time.Hour
)

func main() {
	cfg := config.Load()
	log := setupLogger(cfg.LogLevel)
	if err := run(log, cfg); err != nil {
		log.Error("ingest exited with error", "err", err)
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
	rootCtx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	mks, err := markets.Load(cfg.MarketsPath)
	if err != nil {
		return fmt.Errorf("load markets: %w", err)
	}
	log.Info("markets loaded", "path", cfg.MarketsPath, "count", len(mks))

	chCfg := clickhouse.Config{
		Addr:     cfg.CHAddr,
		Database: cfg.CHDatabase,
		Username: cfg.CHUsername,
		Password: cfg.CHPassword,
	}
	conn, err := clickhouse.Open(rootCtx, chCfg)
	if err != nil {
		return err
	}
	defer conn.Close()
	if err := clickhouse.VerifySchema(rootCtx, conn); err != nil {
		return err
	}
	log.Info("clickhouse connected", "addr", chCfg.Addr, "db", chCfg.Database)
	log.Info("hyperliquid endpoints", "rest", cfg.HLRESTURL, "ws", cfg.HLWSURL)

	httpSrv := startMetricsServer(metricsAddr, log)
	defer func() {
		sctx, c := context.WithTimeout(context.Background(), 5*time.Second)
		defer c()
		_ = httpSrv.Shutdown(sctx)
	}()

	rest := hyperliquid.NewRESTClient(cfg.HLRESTURL)
	rest.ProxyKey = cfg.HLProxyKey

	if err := syncMeta(rootCtx, log, rest, conn, mks); err != nil {
		log.Warn("initial meta sync failed", "err", err)
	}

	assetCh := make(chan AssetCtxRow, batchChanBuffer)
	candleCh := make(chan CandleRow, batchChanBuffer)

	var wg sync.WaitGroup
	wg.Add(4)
	go func() { defer wg.Done(); metaRefreshLoop(rootCtx, log, rest, conn, mks) }()
	go func() { defer wg.Done(); discovery.RunLoop(rootCtx, log, rest, mks, discoveryInterval) }()
	go func() { defer wg.Done(); batchAssetCtx(log, conn, assetCh) }()
	go func() { defer wg.Done(); batchCandles(log, conn, candleCh) }()

	wsErr := wsLoop(rootCtx, log, cfg.HLWSURL, cfg.HLProxyKey, mks, assetCh, candleCh)

	close(assetCh)
	close(candleCh)
	wg.Wait()
	log.Info("ingest stopped cleanly")

	if errors.Is(wsErr, context.Canceled) || wsErr == nil {
		return nil
	}
	return wsErr
}

func startMetricsServer(addr string, log *slog.Logger) *http.Server {
	mux := http.NewServeMux()
	mux.Handle("/metrics", promhttp.Handler())
	srv := &http.Server{Addr: addr, Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	go func() {
		log.Info("metrics server listening", "addr", addr)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Error("metrics server failed", "err", err)
		}
	}()
	return srv
}

// ============================================================================
// Row types -- one per destination table.
// ============================================================================

type AssetCtxRow struct {
	TS        time.Time
	Dex       string
	Coin      string
	MarkPx    float64
	OraclePx  float64
	MidPx     *float64
	OI        float64
	DayVolume float64
}

type CandleRow struct {
	OpenTS    time.Time
	CloseTS   time.Time
	Dex       string
	Coin      string
	Interval  string
	Open      float64
	High      float64
	Low       float64
	Close     float64
	Volume    float64
	NumTrades uint32
}

// ============================================================================
// Meta sync -- xyz dex only
// ============================================================================

func syncMeta(ctx context.Context, log *slog.Logger, rest *hyperliquid.RESTClient, conn chdriver.Conn, mks []markets.Market) error {
	// Sync each distinct dex present in the allowlist.
	dexSeen := make(map[string]bool)
	for _, m := range mks {
		if dexSeen[m.Dex] {
			continue
		}
		dexSeen[m.Dex] = true
		if err := syncMetaDex(ctx, log, rest, conn, m.Dex); err != nil {
			return err
		}
	}
	return nil
}

func syncMetaDex(ctx context.Context, log *slog.Logger, rest *hyperliquid.RESTClient, conn chdriver.Conn, dex string) error {
	meta, err := rest.MetaDex(ctx, dex)
	if err != nil {
		return fmt.Errorf("fetch meta dex=%s: %w", dex, err)
	}
	if len(meta.Universe) == 0 {
		return fmt.Errorf("meta dex=%s returned empty universe", dex)
	}
	batch, err := conn.PrepareBatch(ctx, "INSERT INTO hl.perp_meta (dex, coin, sz_decimals, max_leverage, only_isolated)")
	if err != nil {
		return fmt.Errorf("prepare meta batch: %w", err)
	}
	for _, u := range meta.Universe {
		m := markets.ParseSymbol(u.Name) // "xyz:NVDA" → {Dex:"xyz", Coin:"NVDA"}
		var iso uint8
		if u.OnlyIsolated {
			iso = 1
		}
		if err := batch.Append(m.Dex, m.Coin, u.SzDecimals, u.MaxLeverage, iso); err != nil {
			return fmt.Errorf("append meta row %s: %w", u.Name, err)
		}
	}
	if err := batch.Send(); err != nil {
		return fmt.Errorf("send meta batch: %w", err)
	}
	log.Info("synced perp_meta", "dex", dex, "rows", len(meta.Universe))
	return nil
}

func metaRefreshLoop(ctx context.Context, log *slog.Logger, rest *hyperliquid.RESTClient, conn chdriver.Conn, mks []markets.Market) {
	t := time.NewTicker(metaRefreshIn)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if err := syncMeta(ctx, log, rest, conn, mks); err != nil {
				log.Warn("meta refresh failed", "err", err)
			}
		}
	}
}

// ============================================================================
// WS loop: connect -> subscribe -> read -> reconnect on failure.
// ============================================================================

func wsLoop(ctx context.Context, log *slog.Logger, wsURL, proxyKey string, mks []markets.Market, assetCh chan<- AssetCtxRow, candleCh chan<- CandleRow) error {
	const (
		minBackoff = 1 * time.Second
		maxBackoff = 60 * time.Second
	)
	backoff := minBackoff

	for {
		if err := ctx.Err(); err != nil {
			return err
		}

		client := hyperliquid.NewWSClient(wsURL, log)
		client.ProxyKey = proxyKey
		if err := client.Connect(ctx); err != nil {
			log.Error("ws connect failed", "err", err, "backoff", backoff)
			metrics.WSReconnectsTotal.Inc()
			if !sleep(ctx, backoff) {
				return ctx.Err()
			}
			backoff = nextBackoff(backoff, maxBackoff)
			continue
		}
		log.Info("ws connected", "url", client.URL)

		if err := subscribeAll(ctx, log, client, mks); err != nil {
			log.Error("subscribe failed", "err", err)
			_ = client.Close()
			metrics.WSReconnectsTotal.Inc()
			if !sleep(ctx, backoff) {
				return ctx.Err()
			}
			backoff = nextBackoff(backoff, maxBackoff)
			continue
		}

		gotData := false
		for {
			env, err := client.ReadEnvelope(ctx)
			if err != nil {
				if ctx.Err() != nil {
					_ = client.Close()
					return ctx.Err()
				}
				log.Error("ws read", "err", err)
				_ = client.Close()
				break
			}
			if !gotData {
				gotData = true
				backoff = minBackoff
			}
			metrics.MessagesTotal.WithLabelValues(env.Channel).Inc()
			dispatch(env, log, assetCh, candleCh)
		}

		metrics.WSReconnectsTotal.Inc()
		if !sleep(ctx, backoff) {
			return ctx.Err()
		}
		backoff = nextBackoff(backoff, maxBackoff)
	}
}

func subscribeAll(ctx context.Context, log *slog.Logger, client *hyperliquid.WSClient, mks []markets.Market) error {
	for _, m := range mks {
		sym := m.Symbol() // e.g. "xyz:NVDA"
		if err := client.Subscribe(ctx, hyperliquid.Subscription{
			Type: "activeAssetCtx",
			Coin: sym,
		}); err != nil {
			return fmt.Errorf("subscribe activeAssetCtx %s: %w", sym, err)
		}
		metrics.SubsActive.WithLabelValues(m.Dex, m.Coin).Set(1)
		log.Info("subscribed", "channel", "activeAssetCtx", "dex", m.Dex, "coin", m.Coin)

		for _, iv := range intervals {
			if err := client.Subscribe(ctx, hyperliquid.Subscription{
				Type:     "candle",
				Coin:     sym,
				Interval: iv,
			}); err != nil {
				return fmt.Errorf("subscribe candle %s %s: %w", sym, iv, err)
			}
			log.Info("subscribed", "channel", "candle", "dex", m.Dex, "coin", m.Coin, "interval", iv)
		}
	}
	return nil
}

// dispatch routes a single envelope to the right batcher channel.
func dispatch(env *hyperliquid.WSEnvelope, log *slog.Logger, assetCh chan<- AssetCtxRow, candleCh chan<- CandleRow) {
	switch env.Channel {
	case "activeAssetCtx":
		var d hyperliquid.WSActiveAssetCtx
		if err := json.Unmarshal(env.Data, &d); err != nil {
			log.Warn("decode activeAssetCtx", "err", err)
			return
		}
		row, err := assetCtxToRow(d)
		if err != nil {
			log.Warn("parse activeAssetCtx fields", "coin", d.Coin, "err", err)
			return
		}
		select {
		case assetCh <- row:
		default:
			metrics.InsertErrorsTotal.WithLabelValues("asset_ctx").Inc()
			log.Warn("asset_ctx channel full -- dropping row", "dex", row.Dex, "coin", row.Coin)
		}

	case "candle":
		var c hyperliquid.Candle
		if err := json.Unmarshal(env.Data, &c); err != nil {
			log.Warn("decode candle", "err", err)
			return
		}
		m := markets.ParseSymbol(c.Coin) // "xyz:NVDA" → {Dex:"xyz", Coin:"NVDA"}
		row := CandleRow{
			OpenTS:    time.UnixMilli(c.OpenTimeMs).UTC(),
			CloseTS:   time.UnixMilli(c.CloseTimeMs).UTC(),
			Dex:       m.Dex,
			Coin:      m.Coin,
			Interval:  c.Interval,
			Open:      float64(c.Open),
			High:      float64(c.High),
			Low:       float64(c.Low),
			Close:     float64(c.Close),
			Volume:    float64(c.Volume),
			NumTrades: c.NumTrades,
		}
		select {
		case candleCh <- row:
		default:
			metrics.InsertErrorsTotal.WithLabelValues("candles").Inc()
			log.Warn("candles channel full -- dropping row", "dex", row.Dex, "coin", row.Coin, "interval", row.Interval)
		}

	case "subscriptionResponse":
		log.Debug("subscription ack", "data", string(env.Data))

	case "pong":
		// no-op

	default:
		log.Debug("unknown channel", "channel", env.Channel)
	}
}

func assetCtxToRow(d hyperliquid.WSActiveAssetCtx) (AssetCtxRow, error) {
	m := markets.ParseSymbol(d.Coin) // "xyz:NVDA" → {Dex:"xyz", Coin:"NVDA"}
	row := AssetCtxRow{TS: time.Now().UTC(), Dex: m.Dex, Coin: m.Coin}
	var err error
	if row.MarkPx, err = hyperliquid.ParseFloat(d.Ctx.MarkPx); err != nil {
		return row, fmt.Errorf("markPx: %w", err)
	}
	if row.OraclePx, err = hyperliquid.ParseFloat(d.Ctx.OraclePx); err != nil {
		return row, fmt.Errorf("oraclePx: %w", err)
	}
	if row.OI, err = hyperliquid.ParseFloat(d.Ctx.OpenInterest); err != nil {
		return row, fmt.Errorf("openInterest: %w", err)
	}
	if row.DayVolume, err = hyperliquid.ParseFloat(d.Ctx.DayNtlVlm); err != nil {
		return row, fmt.Errorf("dayNtlVlm: %w", err)
	}
	if row.MidPx, err = hyperliquid.ParseFloatPtr(d.Ctx.MidPx); err != nil {
		return row, fmt.Errorf("midPx: %w", err)
	}
	return row, nil
}

// ============================================================================
// Batchers: one goroutine per table, batch on size OR time.
// ============================================================================

func batchAssetCtx(log *slog.Logger, conn chdriver.Conn, in <-chan AssetCtxRow) {
	buf := make([]AssetCtxRow, 0, batchMaxRows)
	t := time.NewTicker(batchMaxInterval)
	defer t.Stop()

	flush := func() {
		if len(buf) == 0 {
			return
		}
		if err := insertAssetCtx(conn, buf); err != nil {
			metrics.InsertErrorsTotal.WithLabelValues("asset_ctx").Inc()
			log.Error("insert asset_ctx", "rows", len(buf), "err", err)
		} else {
			metrics.RowsInsertedTotal.WithLabelValues("asset_ctx").Add(float64(len(buf)))
		}
		buf = buf[:0]
	}
	defer flush()

	for {
		select {
		case row, ok := <-in:
			if !ok {
				return
			}
			buf = append(buf, row)
			metrics.BufferDepth.WithLabelValues("asset_ctx").Set(float64(len(in)))
			if len(buf) >= batchMaxRows {
				flush()
			}
		case <-t.C:
			flush()
		}
	}
}

func batchCandles(log *slog.Logger, conn chdriver.Conn, in <-chan CandleRow) {
	buf := make([]CandleRow, 0, batchMaxRows)
	t := time.NewTicker(batchMaxInterval)
	defer t.Stop()

	flush := func() {
		if len(buf) == 0 {
			return
		}
		if err := insertCandles(conn, buf); err != nil {
			metrics.InsertErrorsTotal.WithLabelValues("candles").Inc()
			log.Error("insert candles", "rows", len(buf), "err", err)
		} else {
			metrics.RowsInsertedTotal.WithLabelValues("candles").Add(float64(len(buf)))
		}
		buf = buf[:0]
	}
	defer flush()

	for {
		select {
		case row, ok := <-in:
			if !ok {
				return
			}
			buf = append(buf, row)
			metrics.BufferDepth.WithLabelValues("candles").Set(float64(len(in)))
			if len(buf) >= batchMaxRows {
				flush()
			}
		case <-t.C:
			flush()
		}
	}
}

func insertAssetCtx(conn chdriver.Conn, rows []AssetCtxRow) error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	t0 := time.Now()
	defer func() {
		metrics.InsertDuration.WithLabelValues("asset_ctx").Observe(time.Since(t0).Seconds())
	}()

	batch, err := conn.PrepareBatch(ctx,
		"INSERT INTO hl.asset_ctx (ts, dex, coin, mark_px, oracle_px, mid_px, open_interest, day_volume)")
	if err != nil {
		return fmt.Errorf("prepare batch: %w", err)
	}
	for _, r := range rows {
		if err := batch.Append(r.TS, r.Dex, r.Coin, r.MarkPx, r.OraclePx, r.MidPx, r.OI, r.DayVolume); err != nil {
			return fmt.Errorf("append: %w", err)
		}
	}
	return batch.Send()
}

func insertCandles(conn chdriver.Conn, rows []CandleRow) error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	t0 := time.Now()
	defer func() {
		metrics.InsertDuration.WithLabelValues("candles").Observe(time.Since(t0).Seconds())
	}()

	batch, err := conn.PrepareBatch(ctx,
		"INSERT INTO hl.candles (open_ts, close_ts, dex, coin, interval, open_px, high_px, low_px, close_px, volume, num_trades)")
	if err != nil {
		return fmt.Errorf("prepare batch: %w", err)
	}
	for _, r := range rows {
		if err := batch.Append(r.OpenTS, r.CloseTS, r.Dex, r.Coin, r.Interval, r.Open, r.High, r.Low, r.Close, r.Volume, r.NumTrades); err != nil {
			return fmt.Errorf("append: %w", err)
		}
	}
	return batch.Send()
}

// ============================================================================
// Helpers
// ============================================================================

func sleep(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}

func nextBackoff(cur, max time.Duration) time.Duration {
	next := cur * 2
	if next > max {
		return max
	}
	return next
}
