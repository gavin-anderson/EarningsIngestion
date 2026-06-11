package hyperliquid

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

// DefaultRESTURL is the public Hyperliquid mainnet REST /info endpoint.
// Override via the url arg to NewRESTClient (e.g. from HL_REST_URL in .env).
const DefaultRESTURL = "https://api.hyperliquid.xyz/info"

// RESTClient is a small POST-only client for /info queries.
// It applies polite rate limiting and retries 429/5xx with exponential backoff.
type RESTClient struct {
	URL  string
	HTTP *http.Client

	// MinInterval is the minimum gap between requests. Hyperliquid's docs
	// suggest staying well under their per-IP limits; 100ms is generous.
	MinInterval time.Duration

	lastCall time.Time
}

// NewRESTClient builds a client targeting url. Pass "" to use DefaultRESTURL.
func NewRESTClient(url string) *RESTClient {
	if url == "" {
		url = DefaultRESTURL
	}
	return &RESTClient{
		URL:         url,
		HTTP:        &http.Client{Timeout: 30 * time.Second},
		MinInterval: 100 * time.Millisecond,
	}
}

// Meta fetches the main perp universe metadata (crypto perps, ~230 coins).
func (c *RESTClient) Meta(ctx context.Context) (*MetaResponse, error) {
	return c.MetaDex(ctx, "")
}

// MetaDex fetches the perp universe for a specific dex.
// Pass dex="" for the main perp dex; pass e.g. "xyz" for a HIP-3 builder dex.
func (c *RESTClient) MetaDex(ctx context.Context, dex string) (*MetaResponse, error) {
	req := InfoRequest{"type": "meta"}
	if dex != "" {
		req["dex"] = dex
	}
	body, err := c.do(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("meta dex=%q: %w", dex, err)
	}
	var m MetaResponse
	if err := json.Unmarshal(body, &m); err != nil {
		return nil, fmt.Errorf("meta dex=%q decode: %w", dex, err)
	}
	return &m, nil
}

// CandleSnapshot fetches historical candles. The server caps each response at
// ~5000 candles, so callers must page by adjusting startMs/endMs in chunks.
func (c *RESTClient) CandleSnapshot(ctx context.Context, coin, interval string, startMs, endMs int64) ([]Candle, error) {
	req := InfoRequest{
		"type": "candleSnapshot",
		"req": map[string]any{
			"coin":      coin,
			"interval":  interval,
			"startTime": startMs,
			"endTime":   endMs,
		},
	}
	body, err := c.do(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("candleSnapshot %s %s: %w", coin, interval, err)
	}
	var out []Candle
	if err := json.Unmarshal(body, &out); err != nil {
		return nil, fmt.Errorf("candleSnapshot decode: %w", err)
	}
	return out, nil
}

// do POSTs payload as JSON. Retries 429/5xx with exponential backoff (500ms,
// 1s, 2s, ...). Hard-fails on other 4xx — those are bugs in our request shape,
// not transient server issues.
func (c *RESTClient) do(ctx context.Context, payload any) ([]byte, error) {
	b, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("marshal: %w", err)
	}

	// Throttle to MinInterval since last call.
	if gap := time.Since(c.lastCall); gap < c.MinInterval {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(c.MinInterval - gap):
		}
	}

	const maxAttempts = 6
	backoff := 500 * time.Millisecond
	var lastErr error
	for attempt := 0; attempt < maxAttempts; attempt++ {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.URL, bytes.NewReader(b))
		if err != nil {
			return nil, fmt.Errorf("build request: %w", err)
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "application/json")

		c.lastCall = time.Now()
		resp, err := c.HTTP.Do(req)
		if err != nil {
			lastErr = err
			if !sleep(ctx, backoff) {
				return nil, ctx.Err()
			}
			backoff *= 2
			continue
		}
		body, rerr := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		if rerr != nil {
			lastErr = rerr
			if !sleep(ctx, backoff) {
				return nil, ctx.Err()
			}
			backoff *= 2
			continue
		}

		switch {
		case resp.StatusCode == http.StatusOK:
			return body, nil
		case resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500:
			lastErr = fmt.Errorf("status %d: %s", resp.StatusCode, truncate(string(body), 200))
			if !sleep(ctx, backoff) {
				return nil, ctx.Err()
			}
			backoff *= 2
			continue
		default:
			return nil, fmt.Errorf("status %d (non-retryable): %s", resp.StatusCode, truncate(string(body), 500))
		}
	}
	return nil, fmt.Errorf("exhausted retries: %w", lastErr)
}

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

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
