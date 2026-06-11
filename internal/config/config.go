// Package config loads runtime configuration from a .env file (if present)
// plus environment variables. .env is loaded once at process start; existing
// shell env vars take precedence over file values (standard godotenv behavior).
//
// All settings have sane defaults so the service runs out of the box; .env
// is only required for overrides like routing through a geo proxy.
package config

import (
	"os"

	"github.com/joho/godotenv"
)

// Hyperliquid endpoint URLs. Override via .env when going through a proxy.
const (
	DefaultRESTURL = "https://api.hyperliquid.xyz/info"
	DefaultWSURL   = "wss://api.hyperliquid.xyz/ws"
)

// ClickHouse defaults match docker-compose.yml.
const (
	DefaultCHAddr     = "localhost:9000"
	DefaultCHDatabase = "hl"
	DefaultCHUsername = "default"
	DefaultCHPassword = ""
)

// Config is the resolved runtime configuration shared by both binaries.
type Config struct {
	HLRESTURL string
	HLWSURL   string

	CHAddr     string
	CHDatabase string
	CHUsername string
	CHPassword string

	LogLevel    string
	MarketsPath string // path to markets.yaml
}

// Load reads .env from the current working directory (silently no-op if
// missing) and resolves each setting. Call this before constructing the
// logger so LogLevel reflects .env values.
func Load() Config {
	_ = godotenv.Load()
	return Config{
		HLRESTURL:   envOr("HL_REST_URL", DefaultRESTURL),
		HLWSURL:     envOr("HL_WS_URL", DefaultWSURL),
		CHAddr:      envOr("CH_ADDR", DefaultCHAddr),
		CHDatabase:  envOr("CH_DATABASE", DefaultCHDatabase),
		CHUsername:  envOr("CH_USERNAME", DefaultCHUsername),
		CHPassword:  envOrEmpty("CH_PASSWORD", DefaultCHPassword),
		LogLevel:    envOr("LOG_LEVEL", "info"),
		MarketsPath: envOr("MARKETS_PATH", "markets.yaml"),
	}
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

// envOrEmpty allows an explicit empty-string override (password may
// legitimately be empty in dev).
func envOrEmpty(key, def string) string {
	if v, ok := os.LookupEnv(key); ok {
		return v
	}
	return def
}
