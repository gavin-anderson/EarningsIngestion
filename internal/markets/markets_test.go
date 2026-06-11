package markets

import (
	"os"
	"path/filepath"
	"testing"
)

func TestParseSymbol(t *testing.T) {
	tests := []struct {
		sym     string
		wantDex string
		wantCoin string
	}{
		{"xyz:NVDA", "xyz", "NVDA"},
		{"xyz:AAPL", "xyz", "AAPL"},
		{"BTC", "", "BTC"},
		{"", "", ""},
	}
	for _, tc := range tests {
		m := ParseSymbol(tc.sym)
		if m.Dex != tc.wantDex || m.Coin != tc.wantCoin {
			t.Errorf("ParseSymbol(%q) = {%q,%q}, want {%q,%q}", tc.sym, m.Dex, m.Coin, tc.wantDex, tc.wantCoin)
		}
	}
}

func TestSymbol(t *testing.T) {
	tests := []struct{ m Market; want string }{
		{Market{"xyz", "NVDA"}, "xyz:NVDA"},
		{Market{"", "BTC"}, "BTC"},
	}
	for _, tc := range tests {
		if got := tc.m.Symbol(); got != tc.want {
			t.Errorf("Market%+v.Symbol() = %q, want %q", tc.m, got, tc.want)
		}
	}
}

func TestLoadValid(t *testing.T) {
	yaml := `
markets:
  - dex: xyz
    coin: NVDA
  - dex: xyz
    coin: AAPL
`
	f := writeTemp(t, yaml)
	mks, err := Load(f)
	if err != nil {
		t.Fatal(err)
	}
	if len(mks) != 2 {
		t.Fatalf("got %d markets, want 2", len(mks))
	}
	if mks[0].Symbol() != "xyz:NVDA" {
		t.Errorf("first market symbol = %q, want xyz:NVDA", mks[0].Symbol())
	}
}

func TestLoadDuplicate(t *testing.T) {
	yaml := `
markets:
  - dex: xyz
    coin: NVDA
  - dex: xyz
    coin: NVDA
`
	f := writeTemp(t, yaml)
	if _, err := Load(f); err == nil {
		t.Fatal("expected error for duplicate market")
	}
}

func TestLoadEmpty(t *testing.T) {
	yaml := `markets: []`
	f := writeTemp(t, yaml)
	if _, err := Load(f); err == nil {
		t.Fatal("expected error for empty markets")
	}
}

func TestDiff(t *testing.T) {
	a := []Market{{"xyz", "NVDA"}, {"xyz", "AAPL"}, {"xyz", "TSLA"}}
	b := []Market{{"xyz", "NVDA"}, {"xyz", "AAPL"}, {"xyz", "MSFT"}}
	onlyA, onlyB := Diff(a, b)
	if len(onlyA) != 1 || onlyA[0].Coin != "TSLA" {
		t.Errorf("onlyA = %v, want [{xyz TSLA}]", onlyA)
	}
	if len(onlyB) != 1 || onlyB[0].Coin != "MSFT" {
		t.Errorf("onlyB = %v, want [{xyz MSFT}]", onlyB)
	}
}

func writeTemp(t *testing.T, content string) string {
	t.Helper()
	f := filepath.Join(t.TempDir(), "markets.yaml")
	if err := os.WriteFile(f, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return f
}
