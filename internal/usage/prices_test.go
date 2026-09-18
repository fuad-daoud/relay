package usage

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestEstimateMissingRow(t *testing.T) {
	p := testPrices()
	usd, ok := p.Estimate("anthropic", "not-a-model", Tokens{In: 1_000_000})
	if ok {
		t.Fatalf("ok = true for a missing row (usd %v); a missing price must never be a number", usd)
	}
	if _, ok := (Prices{}).Estimate("anthropic", "claude-sonnet-5", Tokens{In: 1}); ok {
		t.Fatal("empty table must not price anything")
	}
}

func TestEstimatePerMillion(t *testing.T) {
	usd, ok := testPrices().Estimate("google", "gemini-3.8-flash", Tokens{In: 2_000_000, CacheRead: 1_000_000, Out: 500_000})
	if !ok {
		t.Fatal("expected a row")
	}
	if usd < 2.049 || usd > 2.051 { // 1 + 0.05 + 1
		t.Errorf("usd = %v, want 2.05", usd)
	}
}

func TestLoadPricesMissingFileIsDefault(t *testing.T) {
	p, err := LoadPrices(filepath.Join(t.TempDir(), "prices.json"))
	if err != nil {
		t.Fatalf("missing file must not error: %v", err)
	}
	if p.AsOf != DefaultPrices().AsOf {
		t.Errorf("AsOf = %q, want the embedded default's", p.AsOf)
	}
}

func TestLoadPricesOverlaysDefault(t *testing.T) {
	path := filepath.Join(t.TempDir(), "prices.json")
	body := `{"as_of":"2030-01-01","source":"me","models":{"test/m":{"in":1,"cache_read":0.1,"cache_write":1,"out":2}}}`
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	p, err := LoadPrices(path)
	if err != nil {
		t.Fatal(err)
	}
	if p.AsOf != "2030-01-01" || p.Source != "me" {
		t.Errorf("as_of/source = %q/%q, want the file's", p.AsOf, p.Source)
	}
	if _, ok := p.Estimate("test", "m", Tokens{In: 1}); !ok {
		t.Error("the file's row must be present")
	}
	for k := range DefaultPrices().Models {
		if _, ok := p.Models[k]; !ok {
			t.Errorf("default row %q must survive the overlay", k)
		}
	}
}

func TestLoadPricesMalformed(t *testing.T) {
	path := filepath.Join(t.TempDir(), "prices.json")
	if err := os.WriteFile(path, []byte(`{"models": 5}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadPrices(path); !errors.Is(err, ErrBadPrices) {
		t.Errorf("err = %v, want ErrBadPrices", err)
	}
	if err := os.WriteFile(path, []byte(`{"models":{"test/m":{"in":-1}}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadPrices(path); !errors.Is(err, ErrBadPrices) {
		t.Errorf("negative price: err = %v, want ErrBadPrices", err)
	}
}

func TestDefaultPricesParses(t *testing.T) {
	p := DefaultPrices()
	if p.AsOf == "" || p.Source == "" {
		t.Errorf("embedded default must carry as_of and source: %+v", p)
	}
	for k, m := range p.Models {
		if m.In < 0 || m.CacheRead < 0 || m.CacheWrite < 0 || m.Out < 0 {
			t.Errorf("%s: negative price %+v", k, m)
		}
	}
}
