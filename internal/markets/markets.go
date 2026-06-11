// Package markets loads and validates the curated allowlist of (dex, coin)
// pairs from markets.yaml.
package markets

import (
	"fmt"
	"os"
	"strings"

	"gopkg.in/yaml.v3"
)

// Market is one (dex, coin) pair from the allowlist.
// Coin is the bare ticker (e.g. "NVDA"); Symbol() returns the full API symbol.
type Market struct {
	Dex  string `yaml:"dex"`
	Coin string `yaml:"coin"`
}

// Symbol returns the full API symbol used in REST and WS calls (e.g. "xyz:NVDA").
// For the main perp dex (Dex=="") it returns Coin unchanged.
func (m Market) Symbol() string {
	if m.Dex == "" {
		return m.Coin
	}
	return m.Dex + ":" + m.Coin
}

// ParseSymbol splits an API symbol like "xyz:NVDA" into Market{Dex:"xyz", Coin:"NVDA"}.
// If there is no ":" the Dex is empty (main perp dex).
func ParseSymbol(symbol string) Market {
	if i := strings.IndexByte(symbol, ':'); i >= 0 {
		return Market{Dex: symbol[:i], Coin: symbol[i+1:]}
	}
	return Market{Dex: "", Coin: symbol}
}

type yamlFile struct {
	Markets []Market `yaml:"markets"`
}

// Load reads path (typically "markets.yaml"), parses it, and validates entries.
func Load(path string) ([]Market, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	var f yamlFile
	if err := yaml.Unmarshal(data, &f); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	if len(f.Markets) == 0 {
		return nil, fmt.Errorf("%s has no entries", path)
	}
	seen := make(map[string]bool, len(f.Markets))
	for _, m := range f.Markets {
		if m.Dex == "" || m.Coin == "" {
			return nil, fmt.Errorf("market entry missing dex or coin: %+v", m)
		}
		key := m.Symbol()
		if seen[key] {
			return nil, fmt.Errorf("duplicate market in %s: %s", path, key)
		}
		seen[key] = true
	}
	return f.Markets, nil
}

// Contains reports whether ms contains m (matched by Dex and Coin).
func Contains(ms []Market, m Market) bool {
	for _, x := range ms {
		if x.Dex == m.Dex && x.Coin == m.Coin {
			return true
		}
	}
	return false
}

// Diff returns markets only in a, and markets only in b.
func Diff(a, b []Market) (onlyInA, onlyInB []Market) {
	for _, m := range a {
		if !Contains(b, m) {
			onlyInA = append(onlyInA, m)
		}
	}
	for _, m := range b {
		if !Contains(a, m) {
			onlyInB = append(onlyInB, m)
		}
	}
	return
}

// FilterByDex returns markets where Dex == dex.
func FilterByDex(ms []Market, dex string) []Market {
	var out []Market
	for _, m := range ms {
		if m.Dex == dex {
			out = append(out, m)
		}
	}
	return out
}

// Symbols returns the full API symbol for each market.
func Symbols(ms []Market) []string {
	out := make([]string, len(ms))
	for i, m := range ms {
		out[i] = m.Symbol()
	}
	return out
}
