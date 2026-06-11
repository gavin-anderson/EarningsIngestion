package metrics

import "github.com/prometheus/client_golang/prometheus"

var (
	// MessagesTotal counts WS frames received, partitioned by channel name.
	MessagesTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "hl_ingest_messages_total",
			Help: "Total WebSocket frames received, by channel.",
		},
		[]string{"channel"},
	)

	// RowsInsertedTotal counts successfully inserted rows per table.
	RowsInsertedTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "hl_ingest_rows_inserted_total",
			Help: "Total rows inserted into ClickHouse, by table.",
		},
		[]string{"table"},
	)

	// InsertErrorsTotal counts failed batch inserts.
	InsertErrorsTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "hl_ingest_insert_errors_total",
			Help: "Total failed batch inserts, by table.",
		},
		[]string{"table"},
	)

	// InsertDuration tracks insert latency. Buckets cover 1ms..5s --
	// healthy local CH inserts land in the low tens of milliseconds.
	InsertDuration = prometheus.NewHistogramVec(
		prometheus.HistogramOpts{
			Name:    "hl_ingest_insert_duration_seconds",
			Help:    "Histogram of ClickHouse batch insert latency, by table.",
			Buckets: []float64{0.001, 0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5},
		},
		[]string{"table"},
	)

	// WSReconnectsTotal counts each reconnection attempt (successful or not).
	WSReconnectsTotal = prometheus.NewCounter(
		prometheus.CounterOpts{
			Name: "hl_ingest_ws_reconnects_total",
			Help: "Total WebSocket reconnect attempts.",
		},
	)

	// BufferDepth shows current backlog in each table's batcher channel.
	BufferDepth = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "hl_ingest_buffer_depth",
			Help: "Current depth of each batcher's input channel, by table.",
		},
		[]string{"table"},
	)

	// SubsActive tracks the number of active WS subscriptions per (dex, coin).
	// Set to 1 on subscribe, 0 on unsubscribe or reconnect reset.
	SubsActive = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "hl_ingest_subs_active",
			Help: "Active WebSocket subscriptions, by dex and coin.",
		},
		[]string{"dex", "coin"},
	)
)

func init() {
	prometheus.MustRegister(
		MessagesTotal,
		RowsInsertedTotal,
		InsertErrorsTotal,
		InsertDuration,
		WSReconnectsTotal,
		BufferDepth,
		SubsActive,
	)
}
