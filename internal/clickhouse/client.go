package clickhouse

import (
	"context"
	"fmt"
	"time"

	"github.com/ClickHouse/clickhouse-go/v2"
	"github.com/ClickHouse/clickhouse-go/v2/lib/driver"
)

// Config is the minimal subset of options we need for local dev.
// Production deploys would extend this with TLS / pool tuning.
type Config struct {
	Addr     string // host:port for the native protocol (9000)
	Database string
	Username string
	Password string
}

// Default returns the local dev configuration that matches docker-compose.yml.
func Default() Config {
	return Config{
		Addr:     "localhost:9000",
		Database: "hl",
		Username: "default",
		Password: "",
	}
}

// Open dials ClickHouse and verifies connectivity with a Ping.
// The returned driver.Conn is safe for concurrent use.
func Open(ctx context.Context, cfg Config) (driver.Conn, error) {
	conn, err := clickhouse.Open(&clickhouse.Options{
		Addr: []string{cfg.Addr},
		Auth: clickhouse.Auth{
			Database: cfg.Database,
			Username: cfg.Username,
			Password: cfg.Password,
		},
		DialTimeout: 5 * time.Second,
		// LZ4 keeps insert payloads small at very low CPU cost.
		Compression: &clickhouse.Compression{Method: clickhouse.CompressionLZ4},
	})
	if err != nil {
		return nil, fmt.Errorf("ch open: %w", err)
	}
	if err := conn.Ping(ctx); err != nil {
		conn.Close()
		return nil, fmt.Errorf("ch ping (is ClickHouse running? try `make up`): %w", err)
	}
	return conn, nil
}

// VerifySchema runs a zero-row probe to confirm the expected tables exist
// before the ingest service starts pushing rows. Failing fast here gives the
// operator a clear "run `make migrate`" signal instead of a cryptic error
// thousands of rows later.
func VerifySchema(ctx context.Context, conn driver.Conn) error {
	probes := []string{
		"SELECT 1 FROM hl.asset_ctx LIMIT 0",
		"SELECT 1 FROM hl.candles LIMIT 0",
		"SELECT 1 FROM hl.perp_meta LIMIT 0",
	}
	for _, q := range probes {
		if err := conn.Exec(ctx, q); err != nil {
			return fmt.Errorf("schema check failed (run `make migrate`): %w", err)
		}
	}
	return nil
}
