// Package discovery periodically fetches the live HIP-3 universe and diffs it
// against the curated allowlist, logging new and missing markets.
package discovery

import (
	"context"
	"log/slog"
	"time"

	"hl-ingest/internal/hyperliquid"
	"hl-ingest/internal/markets"
)

// FetchUniverse fetches all coin names for a HIP-3 dex and returns them as
// Market structs with bare Coin names (the dex prefix is stripped).
func FetchUniverse(ctx context.Context, rest *hyperliquid.RESTClient, dex string) ([]markets.Market, error) {
	meta, err := rest.MetaDex(ctx, dex)
	if err != nil {
		return nil, err
	}
	result := make([]markets.Market, 0, len(meta.Universe))
	for _, u := range meta.Universe {
		result = append(result, markets.ParseSymbol(u.Name))
	}
	return result, nil
}

// RunLoop runs an initial discovery check then repeats every `every` duration
// until ctx is canceled. Each cycle fetches the live universe for every dex
// represented in allowlist and logs the diff.
//
// A fetch failure logs a warning and continues — the WS feed is load-bearing,
// discovery is advisory.
func RunLoop(ctx context.Context, log *slog.Logger, rest *hyperliquid.RESTClient, allowlist []markets.Market, every time.Duration) {
	runOnce := func() {
		// Collect distinct dexes from the allowlist.
		seen := make(map[string]bool)
		var dexes []string
		for _, m := range allowlist {
			if !seen[m.Dex] {
				seen[m.Dex] = true
				dexes = append(dexes, m.Dex)
			}
		}

		for _, dex := range dexes {
			allowed := markets.FilterByDex(allowlist, dex)
			live, err := FetchUniverse(ctx, rest, dex)
			if err != nil {
				log.Warn("discovery fetch failed", "dex", dex, "err", err)
				continue
			}

			missingFromLive, newInLive := markets.Diff(allowed, live)

			missingCoins := coinNames(missingFromLive)
			newCoins := coinNames(newInLive)

			log.Info("discovery",
				"dex", dex,
				"universe_size", len(live),
				"allowlist_size", len(allowed),
				"in_allowlist_not_in_live", missingCoins,
				"new_in_live_not_in_allowlist", newCoins,
			)
			if len(missingCoins) > 0 {
				log.Warn("allowlist markets missing from live universe — delisted?",
					"dex", dex, "coins", missingCoins)
			}
		}
	}

	log.Info("discovery starting", "interval", every)
	runOnce()

	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			runOnce()
		}
	}
}

func coinNames(ms []markets.Market) []string {
	if len(ms) == 0 {
		return []string{}
	}
	names := make([]string, len(ms))
	for i, m := range ms {
		names[i] = m.Coin
	}
	return names
}
