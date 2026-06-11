package hyperliquid

import (
	"encoding/json"
	"fmt"
	"strconv"
)

// WSEnvelope is the outer wrapper for every WS frame: {"channel": ..., "data": ...}.
// Data is left raw so the dispatcher can route by channel before decoding.
type WSEnvelope struct {
	Channel string          `json:"channel"`
	Data    json.RawMessage `json:"data"`
}

// SubscribeMsg is the request envelope for (un)subscribe operations.
type SubscribeMsg struct {
	Method       string       `json:"method"` // "subscribe" or "unsubscribe"
	Subscription Subscription `json:"subscription"`
}

// Subscription describes a single channel to subscribe to.
// Coin/Interval are channel-specific; omit unused fields with omitempty.
type Subscription struct {
	Type     string `json:"type"`
	Coin     string `json:"coin,omitempty"`
	Interval string `json:"interval,omitempty"`
}

// WSActiveAssetCtx is the data payload for the activeAssetCtx channel.
// For perp coins the inner Ctx is a PerpAssetCtx.
type WSActiveAssetCtx struct {
	Coin string       `json:"coin"`
	Ctx  PerpAssetCtx `json:"ctx"`
}

// PerpAssetCtx mirrors Hyperliquid's perp context object. All numeric fields
// arrive as JSON strings (Hyperliquid never sends raw numbers for prices) so
// they stay as strings here and are parsed at the dispatch boundary.
// HL also sends `funding` and `premium` but we deliberately ignore them.
type PerpAssetCtx struct {
	DayNtlVlm    string `json:"dayNtlVlm"`
	MarkPx       string `json:"markPx"`
	MidPx        string `json:"midPx"`
	OpenInterest string `json:"openInterest"`
	OraclePx     string `json:"oraclePx"`
	PrevDayPx    string `json:"prevDayPx"`
}

// Candle is shared by both the WS candle channel and the REST candleSnapshot
// response. Price/volume fields can arrive as either JSON strings or numbers
// depending on endpoint quirks, so we decode through a flex type.
type Candle struct {
	OpenTimeMs  int64     `json:"t"`
	CloseTimeMs int64     `json:"T"`
	Coin        string    `json:"s"`
	Interval    string    `json:"i"`
	Open        FlexFloat `json:"o"`
	Close       FlexFloat `json:"c"`
	High        FlexFloat `json:"h"`
	Low         FlexFloat `json:"l"`
	Volume      FlexFloat `json:"v"`
	NumTrades   uint32    `json:"n"`
}

// FlexFloat accepts a JSON number or a quoted numeric string.
// Hyperliquid mixes both forms across endpoints, so we tolerate either.
type FlexFloat float64

func (f *FlexFloat) UnmarshalJSON(b []byte) error {
	if len(b) == 0 || string(b) == "null" {
		return nil
	}
	s := string(b)
	if b[0] == '"' {
		if len(b) < 2 {
			return fmt.Errorf("flexfloat: short string %q", s)
		}
		s = string(b[1 : len(b)-1])
		if s == "" {
			return nil
		}
	}
	v, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return fmt.Errorf("flexfloat: parse %q: %w", s, err)
	}
	*f = FlexFloat(v)
	return nil
}

// InfoRequest is the generic POST body for the REST /info endpoint.
// Concrete request bodies are built in rest.go.
type InfoRequest map[string]any

// MetaResponse is the response to `{"type":"meta"}`.
type MetaResponse struct {
	Universe []MetaUniverse `json:"universe"`
}

// MetaUniverse is a single per-coin metadata entry.
type MetaUniverse struct {
	Name         string `json:"name"`
	SzDecimals   uint8  `json:"szDecimals"`
	MaxLeverage  uint16 `json:"maxLeverage"`
	OnlyIsolated bool   `json:"onlyIsolated"`
}

// ParseFloat is a small helper for consumers parsing PerpAssetCtx fields.
func ParseFloat(s string) (float64, error) {
	return strconv.ParseFloat(s, 64)
}

// ParseFloatPtr returns nil if s is empty, otherwise *float64.
// Used for Nullable(Float64) columns where the wire field may be empty.
func ParseFloatPtr(s string) (*float64, error) {
	if s == "" {
		return nil, nil
	}
	v, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return nil, err
	}
	return &v, nil
}
