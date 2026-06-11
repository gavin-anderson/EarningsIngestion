package hyperliquid

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"

	"github.com/coder/websocket"
)

// DefaultWSURL is the public Hyperliquid mainnet WS endpoint.
// Override via the url arg to NewWSClient (e.g. from HL_WS_URL in .env).
const DefaultWSURL = "wss://api.hyperliquid.xyz/ws"

// WSClient is a thin wrapper over coder/websocket scoped to a single connection.
// It is NOT safe for concurrent use across goroutines; treat one client as the
// owner of one connection.
type WSClient struct {
	URL string
	log *slog.Logger

	conn *websocket.Conn
}

// NewWSClient builds a client targeting url. Pass "" to use DefaultWSURL.
func NewWSClient(url string, log *slog.Logger) *WSClient {
	if log == nil {
		log = slog.Default()
	}
	if url == "" {
		url = DefaultWSURL
	}
	return &WSClient{URL: url, log: log}
}

// Connect dials the WS endpoint. ReadLimit is raised because activeAssetCtx
// frames can be large (impactPxs arrays etc.) and the default 32KiB is tight.
func (c *WSClient) Connect(ctx context.Context) error {
	conn, _, err := websocket.Dial(ctx, c.URL, nil)
	if err != nil {
		return fmt.Errorf("ws dial %s: %w", c.URL, err)
	}
	conn.SetReadLimit(1 << 24) // 16 MiB
	c.conn = conn
	return nil
}

// Subscribe sends one subscription request. Subscriptions are independent --
// failure to subscribe to one channel does not implicitly close others.
func (c *WSClient) Subscribe(ctx context.Context, sub Subscription) error {
	if c.conn == nil {
		return fmt.Errorf("ws subscribe: not connected")
	}
	msg := SubscribeMsg{Method: "subscribe", Subscription: sub}
	b, err := json.Marshal(msg)
	if err != nil {
		return fmt.Errorf("marshal subscribe: %w", err)
	}
	if err := c.conn.Write(ctx, websocket.MessageText, b); err != nil {
		return fmt.Errorf("write subscribe %+v: %w", sub, err)
	}
	return nil
}

// ReadEnvelope blocks until a frame arrives or the context is canceled.
// The raw Data field is left for the caller to decode per-channel.
func (c *WSClient) ReadEnvelope(ctx context.Context) (*WSEnvelope, error) {
	if c.conn == nil {
		return nil, fmt.Errorf("ws read: not connected")
	}
	_, data, err := c.conn.Read(ctx)
	if err != nil {
		return nil, err
	}
	var env WSEnvelope
	if err := json.Unmarshal(data, &env); err != nil {
		return nil, fmt.Errorf("decode envelope: %w", err)
	}
	return &env, nil
}

// Close sends a normal-closure frame. Safe to call on a nil/closed conn.
func (c *WSClient) Close() error {
	if c.conn == nil {
		return nil
	}
	err := c.conn.Close(websocket.StatusNormalClosure, "")
	c.conn = nil
	return err
}
