# Round usage: the record (#142, slice 1)

> **For agentic workers:** execute the tasks in order; each ends green and
> commits. Steps use checkbox (`- [ ]`) syntax for tracking.

**Design spec:** `docs/specs/2026-09-18-round-usage-design.md` -- read it
first; every rule below is a restatement of one of its sections.
**Issue:** #142

**Goal:** every round's `report` log entry (and every consult's `findings`
entry) carries what the round consumed -- tokens, model, duration, dollars
-- with the provenance of the dollar figure (`measured` / `estimated` /
`unknown`). Nothing prints yet; the field lands in `log.jsonl`.

**Architecture:** a new package `internal/usage` knows each harness's own
record shape (a headless round's `NNN-builder.jsonl`; claude's project
transcripts; opencode's SQLite via the `sqlite3` binary) and nothing about
rounds, mirroring `internal/transcript`. One function in `internal/relay`
builds a `usage.Source` from a binding, calls the reader inside a 5 s
deadline, folds the samples with a price table, and attaches the result.
It never errors: every failure is `Basis: unknown` plus a `Note`.

**Tech stack:** Go standard library only. No new module dependencies.

## Where you are working

| path | what it is |
| --- | --- |
| `~/.local/state/relay/.worktrees/round-usage` | **the git worktree. Every source edit goes here.** Your shell's cwd, branch `relay/round-usage`. |
| `~/.local/state/relay/round-usage` | relay's drop directory: `NNN-plan.md`, `NNN-report.md`, `NNN-done`. Never edit source here. |

## Stop rather than improvise

If a step is impossible as written, or the plan contradicts what you find
in the code, **stop and say so in your report**. Do not bend a test to fit.
A halt that surfaces a design error is worth more than a green suite.

## Running commands

`make` is intercepted on this machine; run the constituents directly and
say so in the report:

```bash
test -z "$(gofmt -l .)" || gofmt -l .
go vet ./...
go test -race -count=1 ./...
cp go.mod /tmp/gm; cp go.sum /tmp/gs; go mod tidy; cmp go.mod /tmp/gm && cmp go.sum /tmp/gs
```

Do not run `herdr`, `claude`, `agy`, `opencode` or `sqlite3`. No test
under `cmd/relay` (CI runners have no herdr; `cmd/relay` tests must not
reach a subcommand). Every rule is tested as a pure function in
`internal/usage`, `internal/relay`, `internal/store` or `internal/doctor`.

## Global constraints

- No new `go.mod` dependencies. `sqlite3` is an external binary reached
  through an `Exec` interface, faked in tests.
- `internal/usage` imports nothing from `internal/relay`, `internal/store`
  or `internal/candidate`. `internal/store` may import `internal/usage`.
- Nothing in this plan prints usage anywhere: no change to `relay log`,
  `relay status`, `internal/ui`, `status.go`, `sort.go`, `waiting.go`.
  The surfaces are a separate plan. If a step seems to need one, stop.
- `Basis` values are exactly `measured`, `estimated`, `unknown`. A missing
  price row is `unknown`, never `0`. A `plan` candidate is never "free".
- `Out` includes thinking/reasoning tokens.
- A round always closes, whatever the reader does.
- One commit per task, message prefixed `feat(usage):` (tasks 1-4),
  `feat(store):` (5), `feat(relay):` (6), `feat(doctor):` (7). Each
  message ends with the attribution lines your harness gives you, if any.

---

### Task 1: `internal/usage` -- types, `Fold`, `Prices`

**Files:**
- Create: `internal/usage/usage.go`, `internal/usage/prices.go`,
  `internal/usage/prices_default.json`
- Test: `internal/usage/usage_test.go`, `internal/usage/prices_test.go`

**Interfaces:**
- Produces (used by every later task):

```go
package usage

type Basis string
const (
	Measured  Basis = "measured"
	Estimated Basis = "estimated"
	Unknown   Basis = "unknown"
)

type Tokens struct {
	In         int64 `json:"in"`
	CacheRead  int64 `json:"cache_read"`
	CacheWrite int64 `json:"cache_write"`
	Out        int64 `json:"out"`
}
func (t Tokens) Add(o Tokens) Tokens
func (t Tokens) Total() int64
func (t Tokens) CacheRatio() float64

type Cost struct {
	USD   float64 `json:"usd"`
	Basis Basis   `json:"basis"`
	Plan  bool    `json:"plan,omitempty"`
}

type Usage struct {
	Harness    string `json:"harness"`
	Provider   string `json:"provider"`
	Model      string `json:"model"`
	DurationMS int64  `json:"duration_ms"`
	Tokens     Tokens `json:"tokens"`
	Cost       Cost   `json:"cost"`
	Samples    int    `json:"samples"`
	Note       string `json:"note,omitempty"`
}

type Sample struct {
	Provider string
	Model    string
	Tokens   Tokens
	USD      float64
	HasCost  bool
}

func Fold(samples []Sample, prices Prices, plan bool, note string) Usage

type ModelPrice struct {
	In         float64 `json:"in"`
	CacheRead  float64 `json:"cache_read"`
	CacheWrite float64 `json:"cache_write"`
	Out        float64 `json:"out"`
}
type Prices struct {
	AsOf   string                `json:"as_of"`
	Source string                `json:"source"`
	Models map[string]ModelPrice `json:"models"`
}
var ErrBadPrices = errors.New("prices.json does not validate")
func DefaultPrices() Prices
func LoadPrices(path string) (Prices, error)
func (p Prices) Estimate(provider, model string, t Tokens) (float64, bool)
```

- [ ] **Step 0: Bring the design docs into the branch.** They exist only
in the main repo's working tree so far. Copy them in and include them in
this task's commit:

```bash
mkdir -p docs/specs docs/plans
cp /home/fuad/projects/relay/docs/specs/2026-09-18-round-usage-design.md docs/specs/
cp /home/fuad/projects/relay/docs/plans/2026-09-18-round-usage.md docs/plans/
```

- [ ] **Step 1: Write the failing tests for `Tokens` and `Fold`.**

`internal/usage/usage_test.go`:

```go
package usage

import (
	"strings"
	"testing"
)

func testPrices() Prices {
	return Prices{AsOf: "2026-09-18", Models: map[string]ModelPrice{
		// USD per million tokens. Round numbers so the sums are exact.
		"anthropic/claude-sonnet-5": {In: 3, CacheRead: 0.3, CacheWrite: 3.75, Out: 15},
		"google/gemini-3.8-flash":   {In: 0.5, CacheRead: 0.05, CacheWrite: 0, Out: 2},
	}}
}

func TestTokensArithmetic(t *testing.T) {
	a := Tokens{In: 10, CacheRead: 20, CacheWrite: 30, Out: 40}
	b := Tokens{In: 1, CacheRead: 2, CacheWrite: 3, Out: 4}
	got := a.Add(b)
	if got != (Tokens{In: 11, CacheRead: 22, CacheWrite: 33, Out: 44}) {
		t.Errorf("Add = %+v", got)
	}
	if got.Total() != 110 {
		t.Errorf("Total = %d, want 110", got.Total())
	}
	// 22 cache-read of 66 prompt tokens.
	if r := got.CacheRatio(); r < 0.333 || r > 0.334 {
		t.Errorf("CacheRatio = %v, want 1/3", r)
	}
	if (Tokens{Out: 5}).CacheRatio() != 0 {
		t.Error("CacheRatio with no prompt tokens must be 0, not NaN")
	}
}

func TestFoldNoSamplesIsUnknown(t *testing.T) {
	u := Fold(nil, testPrices(), false, "no stream")
	if u.Cost.Basis != Unknown || u.Samples != 0 || u.Note != "no stream" {
		t.Errorf("Fold(nil) = %+v", u)
	}
	if u.Cost.USD != 0 {
		t.Errorf("USD = %v, want 0", u.Cost.USD)
	}
}

func TestFoldAllMeasured(t *testing.T) {
	s := []Sample{
		{Provider: "anthropic", Model: "claude-sonnet-5", Tokens: Tokens{In: 100, Out: 10}, USD: 0.02, HasCost: true},
		{Provider: "anthropic", Model: "claude-sonnet-5", Tokens: Tokens{In: 50, Out: 5}, USD: 0.01, HasCost: true},
	}
	u := Fold(s, Prices{}, false, "")
	if u.Cost.Basis != Measured {
		t.Fatalf("basis = %q, want measured", u.Cost.Basis)
	}
	if u.Cost.USD < 0.0299 || u.Cost.USD > 0.0301 {
		t.Errorf("USD = %v, want 0.03", u.Cost.USD)
	}
	if u.Tokens != (Tokens{In: 150, Out: 15}) || u.Samples != 2 {
		t.Errorf("tokens/samples = %+v/%d", u.Tokens, u.Samples)
	}
	if u.Model != "claude-sonnet-5" || u.Provider != "anthropic" {
		t.Errorf("model/provider = %q/%q", u.Model, u.Provider)
	}
	if u.Note != "" {
		t.Errorf("Note = %q, want empty for a single model", u.Note)
	}
}

func TestFoldEstimatesFromPrices(t *testing.T) {
	s := []Sample{{Provider: "anthropic", Model: "claude-sonnet-5",
		Tokens: Tokens{In: 1_000_000, CacheRead: 1_000_000, CacheWrite: 1_000_000, Out: 1_000_000}}}
	u := Fold(s, testPrices(), false, "")
	if u.Cost.Basis != Estimated {
		t.Fatalf("basis = %q, want estimated", u.Cost.Basis)
	}
	// 3 + 0.3 + 3.75 + 15
	if u.Cost.USD < 22.049 || u.Cost.USD > 22.051 {
		t.Errorf("USD = %v, want 22.05", u.Cost.USD)
	}
}

func TestFoldMixedMeasuredAndEstimated(t *testing.T) {
	s := []Sample{
		{Provider: "anthropic", Model: "claude-sonnet-5", Tokens: Tokens{In: 1_000_000}, USD: 5, HasCost: true},
		{Provider: "anthropic", Model: "claude-sonnet-5", Tokens: Tokens{In: 1_000_000}},
	}
	u := Fold(s, testPrices(), false, "")
	if u.Cost.Basis != Estimated {
		t.Fatalf("basis = %q, want estimated when any sample is estimated", u.Cost.Basis)
	}
	if u.Cost.USD < 7.999 || u.Cost.USD > 8.001 { // 5 measured + 3 estimated
		t.Errorf("USD = %v, want 8", u.Cost.USD)
	}
}

func TestFoldUnpricedModelIsUnknownNeverZero(t *testing.T) {
	s := []Sample{{Provider: "google", Model: "gemini-9-ultra", Tokens: Tokens{In: 500, Out: 20}}}
	u := Fold(s, testPrices(), false, "")
	if u.Cost.Basis != Unknown {
		t.Fatalf("basis = %q, want unknown for an unpriced model", u.Cost.Basis)
	}
	if u.Cost.USD != 0 {
		t.Errorf("USD = %v, want 0 under unknown", u.Cost.USD)
	}
	if !strings.Contains(u.Note, "no price for google/gemini-9-ultra") {
		t.Errorf("Note = %q, want the missing row named", u.Note)
	}
	if u.Tokens != (Tokens{In: 500, Out: 20}) {
		t.Errorf("tokens must still be recorded under unknown: %+v", u.Tokens)
	}
}

func TestFoldPicksModelWithMostOutput(t *testing.T) {
	s := []Sample{
		{Provider: "anthropic", Model: "claude-sonnet-5", Tokens: Tokens{Out: 10}, USD: 1, HasCost: true},
		{Provider: "anthropic", Model: "claude-opus-5", Tokens: Tokens{Out: 100}, USD: 1, HasCost: true},
		{Provider: "anthropic", Model: "claude-sonnet-5", Tokens: Tokens{Out: 20}, USD: 1, HasCost: true},
	}
	u := Fold(s, Prices{}, false, "")
	if u.Model != "claude-opus-5" {
		t.Errorf("Model = %q, want the model with most Out tokens", u.Model)
	}
	if u.Note != "2 models" {
		t.Errorf("Note = %q, want \"2 models\"", u.Note)
	}
}

func TestFoldCarriesPlanFlag(t *testing.T) {
	s := []Sample{{Provider: "anthropic", Model: "claude-sonnet-5", Tokens: Tokens{In: 1}, USD: 0, HasCost: true}}
	u := Fold(s, Prices{}, true, "")
	if !u.Cost.Plan {
		t.Error("Plan must be copied through")
	}
	if u.Cost.Basis != Measured {
		t.Errorf("basis = %q; a plan lane with a measured 0 stays measured, the printer says plan", u.Cost.Basis)
	}
}

func TestFoldNegativeCountIsZeroAndNoted(t *testing.T) {
	s := []Sample{{Provider: "anthropic", Model: "claude-sonnet-5", Tokens: Tokens{In: -5, Out: 3}, USD: 1, HasCost: true}}
	u := Fold(s, Prices{}, false, "")
	if u.Tokens.In != 0 || u.Tokens.Out != 3 {
		t.Errorf("tokens = %+v, want the negative clamped", u.Tokens)
	}
	if !strings.Contains(u.Note, "negative") {
		t.Errorf("Note = %q, want the clamp mentioned", u.Note)
	}
}
```

`internal/usage/prices_test.go`:

```go
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
```

- [ ] **Step 2: Run the tests to verify they fail.**

Run: `go test ./internal/usage/`
Expected: build failure -- the package does not exist.

- [ ] **Step 3: Write `usage.go`.**

```go
// Package usage reads what a round consumed from each harness's own
// record and folds it into one figure with its provenance (#142). It
// knows harness record shapes and nothing else: no rounds, no bindings,
// no store. Nothing in relay decides anything on what it returns.
package usage

import (
	"fmt"
	"sort"
)

// Basis is where the dollar figure came from -- provenance, not the truth
// of the bill.
type Basis string

const (
	// Measured: the harness itself reported dollars.
	Measured Basis = "measured"
	// Estimated: relay multiplied harness-reported tokens by a price table.
	Estimated Basis = "estimated"
	// Unknown: no record, no tokens, no price row, or no way to read.
	Unknown Basis = "unknown"
)

// Tokens are counts as the provider bills them. Out includes thinking and
// reasoning tokens; every provider bills those as output.
type Tokens struct {
	In         int64 `json:"in"`
	CacheRead  int64 `json:"cache_read"`
	CacheWrite int64 `json:"cache_write"`
	Out        int64 `json:"out"`
}

// Add returns the field-wise sum.
func (t Tokens) Add(o Tokens) Tokens {
	return Tokens{In: t.In + o.In, CacheRead: t.CacheRead + o.CacheRead, CacheWrite: t.CacheWrite + o.CacheWrite, Out: t.Out + o.Out}
}

// Total is every token the round moved.
func (t Tokens) Total() int64 { return t.In + t.CacheRead + t.CacheWrite + t.Out }

// CacheRatio is the share of prompt tokens served from cache; 0 when there
// were no prompt tokens.
func (t Tokens) CacheRatio() float64 {
	prompt := t.In + t.CacheRead + t.CacheWrite
	if prompt == 0 {
		return 0
	}
	return float64(t.CacheRead) / float64(prompt)
}

// Cost is dollars with provenance. Plan marks a subscription lane: the
// printer says "plan", never "$0" and never "free".
type Cost struct {
	USD   float64 `json:"usd"`
	Basis Basis   `json:"basis"`
	Plan  bool    `json:"plan,omitempty"`
}

// Usage is what one round consumed. A reader that finds nothing returns
// Basis Unknown with a Note; the zero value (Basis "") is never written.
type Usage struct {
	Harness  string `json:"harness"`
	Provider string `json:"provider"`
	// Model is the model most Out tokens went to; "" when nothing was read.
	Model string `json:"model"`
	// DurationMS is the round's wall time, 0 when its start is unknown.
	DurationMS int64  `json:"duration_ms"`
	Tokens     Tokens `json:"tokens"`
	Cost       Cost   `json:"cost"`
	// Samples is how many records were folded; 0 under Unknown means
	// nothing was found.
	Samples int `json:"samples"`
	// Note says why Unknown, or "n models" when more than one was seen.
	Note string `json:"note,omitempty"`
}

// Sample is one billed message as a reader found it. HasCost says USD came
// from the record; false means the record carried tokens only.
type Sample struct {
	Provider string
	Model    string
	Tokens   Tokens
	USD      float64
	HasCost  bool
}

// Fold sums samples into a Usage. Tokens always sum. Cost, in order:
//   - no samples: Unknown, Note as given
//   - every sample HasCost: Measured, USD = sum of the records
//   - otherwise each sample without HasCost is estimated from prices; if
//     every one resolves, Estimated and USD = measured + estimated; if
//     any does not, Unknown, USD 0, Note "no price for provider/model".
//
// Model is the model with the most Out tokens; Note gains "n models" when
// more than one was seen. plan is copied through.
func Fold(samples []Sample, prices Prices, plan bool, note string) Usage {
	u := Usage{Cost: Cost{Basis: Unknown, Plan: plan}, Note: note}
	if len(samples) == 0 {
		return u
	}
	u.Samples = len(samples)
	u.Note = ""

	var notes []string
	outByModel := map[string]int64{}
	providerByModel := map[string]string{}
	usd := 0.0
	basis := Measured
	for _, s := range samples {
		tok, clamped := clamp(s.Tokens)
		if clamped && !contains(notes, "negative count clamped to 0") {
			notes = append(notes, "negative count clamped to 0")
		}
		u.Tokens = u.Tokens.Add(tok)
		outByModel[s.Model] += tok.Out
		providerByModel[s.Model] = s.Provider
		switch {
		case s.HasCost:
			usd += s.USD
		case basis == Unknown:
			// already failed; keep summing tokens only
		default:
			est, ok := prices.Estimate(s.Provider, s.Model, tok)
			if !ok {
				basis = Unknown
				usd = 0
				notes = append(notes, fmt.Sprintf("no price for %s/%s", s.Provider, s.Model))
				continue
			}
			basis = Estimated
			usd += est
		}
	}
	if basis == Unknown {
		usd = 0 // a measured sample after an unpriced one must not leak a partial figure
	}
	u.Cost.Basis = basis
	u.Cost.USD = usd

	models := make([]string, 0, len(outByModel))
	for m := range outByModel {
		models = append(models, m)
	}
	sort.Slice(models, func(i, j int) bool {
		if outByModel[models[i]] != outByModel[models[j]] {
			return outByModel[models[i]] > outByModel[models[j]]
		}
		return models[i] < models[j]
	})
	u.Model = models[0]
	u.Provider = providerByModel[u.Model]
	if len(models) > 1 {
		notes = append(notes, fmt.Sprintf("%d models", len(models)))
	}
	u.Note = join(notes)
	return u
}

func clamp(t Tokens) (Tokens, bool) {
	clamped := false
	fix := func(v *int64) {
		if *v < 0 {
			*v = 0
			clamped = true
		}
	}
	fix(&t.In)
	fix(&t.CacheRead)
	fix(&t.CacheWrite)
	fix(&t.Out)
	return t, clamped
}

func contains(ss []string, s string) bool {
	for _, x := range ss {
		if x == s {
			return true
		}
	}
	return false
}

func join(ss []string) string {
	out := ""
	for i, s := range ss {
		if i > 0 {
			out += "; "
		}
		out += s
	}
	return out
}
```

- [ ] **Step 4: Write `prices.go` and `prices_default.json`.**

`prices.go`:

```go
package usage

import (
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"os"
)

//go:embed prices_default.json
var defaultPricesJSON []byte

// ErrBadPrices reports a prices.json that does not validate. The caller
// prints it and continues with the default: a bad price file must never
// stop a round from closing.
var ErrBadPrices = errors.New("prices.json does not validate")

// ModelPrice is USD per million tokens.
type ModelPrice struct {
	In         float64 `json:"in"`
	CacheRead  float64 `json:"cache_read"`
	CacheWrite float64 `json:"cache_write"`
	Out        float64 `json:"out"`
}

// Prices is the table. Models is keyed "provider/model" in the form
// candidate refs use, minus the harness.
type Prices struct {
	AsOf   string                `json:"as_of"`
	Source string                `json:"source"`
	Models map[string]ModelPrice `json:"models"`
}

// DefaultPrices is the embedded table shipped with relay.
func DefaultPrices() Prices {
	var p Prices
	if err := json.Unmarshal(defaultPricesJSON, &p); err != nil {
		panic("prices_default.json: " + err.Error()) // a build artefact, caught by TestDefaultPricesParses
	}
	if p.Models == nil {
		p.Models = map[string]ModelPrice{}
	}
	return p
}

// LoadPrices reads path over the embedded default: a missing file is the
// default with no error; a present file's rows replace default rows of
// the same key, and its as_of and source win; a malformed file is
// ErrBadPrices.
func LoadPrices(path string) (Prices, error) {
	base := DefaultPrices()
	raw, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return base, nil
	}
	if err != nil {
		return base, fmt.Errorf("%s: %w", path, err)
	}
	var file Prices
	if err := json.Unmarshal(raw, &file); err != nil {
		return base, fmt.Errorf("%s: %v: %w", path, err, ErrBadPrices)
	}
	for k, m := range file.Models {
		if m.In < 0 || m.CacheRead < 0 || m.CacheWrite < 0 || m.Out < 0 {
			return base, fmt.Errorf("%s: %s: negative price: %w", path, k, ErrBadPrices)
		}
		base.Models[k] = m
	}
	if file.AsOf != "" {
		base.AsOf = file.AsOf
	}
	if file.Source != "" {
		base.Source = file.Source
	}
	return base, nil
}

// Estimate prices t for provider/model. ok is false when there is no row;
// a missing row is never a number.
func (p Prices) Estimate(provider, model string, t Tokens) (float64, bool) {
	m, ok := p.Models[provider+"/"+model]
	if !ok {
		return 0, false
	}
	const million = 1_000_000
	usd := float64(t.In)*m.In/million +
		float64(t.CacheRead)*m.CacheRead/million +
		float64(t.CacheWrite)*m.CacheWrite/million +
		float64(t.Out)*m.Out/million
	return usd, true
}
```

`prices_default.json` -- **do not invent numbers.** Populate it from the
providers' published pricing pages for the models in
`~/.config/relay/candidates.json`'s providers (anthropic, google,
openrouter) and cite each page in `source`. If you cannot reach those
pages, ship an empty table and say so in your report:

```json
{
  "as_of": "2026-09-18",
  "source": "empty: prices were not fetched at build time; add rows in ~/.config/relay/prices.json",
  "models": {}
}
```

Either way the keys are `"<provider>/<model id as the harness records it>"`,
e.g. `"anthropic/claude-sonnet-5"`, values per million tokens with the
four fields `in`, `cache_read`, `cache_write`, `out`. Tests never depend on
the default's rows.

- [ ] **Step 5: Run the tests to verify they pass.**

Run: `go test -race -count=1 ./internal/usage/`
Expected: PASS.

- [ ] **Step 6: Mutation check.** In `Estimate`, change `return 0, false`
to `return 0, true`. Run `go test ./internal/usage/ -run 'TestEstimateMissingRow|TestFoldUnpriced'`.
Expected: both FAIL. Revert.

- [ ] **Step 7: Commit.**

```bash
git add docs/specs/2026-09-18-round-usage-design.md docs/plans/2026-09-18-round-usage.md internal/usage/
git commit -m "feat(usage): tokens, cost with provenance, price table (#142)"
```

---

### Task 2: claude readers -- headless stream and pane project transcripts

**Files:**
- Create: `internal/usage/claude.go`,
  `internal/usage/testdata/claude-stream.jsonl`,
  `internal/usage/testdata/claude-stream-killed.jsonl`,
  `internal/usage/testdata/claude-project/sess-a.jsonl`,
  `internal/usage/testdata/claude-project/sess-a/subagents/agent-x.jsonl`
- Test: `internal/usage/claude_test.go`

**Interfaces:**
- Consumes: `Sample`, `Tokens` (Task 1).
- Produces:

```go
// ProjectSlug is the directory name claude keeps a cwd's transcripts under:
// every byte of the absolute path outside [A-Za-z0-9] becomes '-'.
func ProjectSlug(cwd string) string

// claudeStream reads a headless round's stream-json. The result event's
// usage is the sample (HasCost when total_cost_usd is present); with no
// result event, the assistant events deduped by message.id are the
// samples, tokens only. Model: last assistant message.model, else init.
func claudeStream(r io.Reader, fallbackProvider string) []Sample

// claudeProject reads every *.jsonl under fsys (subagents included) and
// returns one sample per assistant message.id whose timestamp is inside
// [start, end] and whose cwd equals worktree. Files with a modtime before
// start are skipped unread.
func claudeProject(fsys fs.FS, worktree string, start, end time.Time, provider string) []Sample
```

- [ ] **Step 1: Write the fixtures.**

`testdata/claude-stream.jsonl` (the headless shape verified 2026-09-18; a
`result` event with `total_cost_usd`; two assistant events, the second
written twice under one id as claude does):

```
{"type":"system","subtype":"init","cwd":"/wt","session_id":"sess-1","tools":["Bash"],"model":"claude-sonnet-5"}
{"type":"assistant","message":{"id":"msg_1","model":"claude-sonnet-5","role":"assistant","content":[{"type":"text","text":"hi"}],"usage":{"input_tokens":10,"cache_creation_input_tokens":100,"cache_read_input_tokens":0,"output_tokens":5}},"session_id":"sess-1"}
{"type":"user","message":{"role":"user","content":[{"type":"tool_result","content":"ok"}]},"session_id":"sess-1"}
{"type":"assistant","message":{"id":"msg_2","model":"claude-sonnet-5","role":"assistant","content":[{"type":"text","text":"a"}],"usage":{"input_tokens":2,"cache_creation_input_tokens":0,"cache_read_input_tokens":100,"output_tokens":20}},"session_id":"sess-1"}
{"type":"assistant","message":{"id":"msg_2","model":"claude-sonnet-5","role":"assistant","content":[{"type":"tool_use","name":"Bash","input":{}}],"usage":{"input_tokens":2,"cache_creation_input_tokens":0,"cache_read_input_tokens":100,"output_tokens":20}},"session_id":"sess-1"}
{"type":"result","subtype":"success","is_error":false,"num_turns":2,"result":"done","session_id":"sess-1","total_cost_usd":0.0299,"usage":{"input_tokens":12,"cache_creation_input_tokens":100,"cache_read_input_tokens":100,"output_tokens":25}}
relay-exit:0
```

`testdata/claude-stream-killed.jsonl` -- the same file without its
`result` line (the round was killed), ending in `relay-exit:137`.

`testdata/claude-project/sess-a.jsonl` (the pane shape; `cwd` on every
record; `msg_p2` written twice; `msg_p3` outside the window; `msg_p4` in
another cwd):

```
{"type":"user","timestamp":"2026-09-18T07:05:00.000Z","cwd":"/wt","sessionId":"sess-a","message":{"role":"user","content":"go"}}
{"type":"assistant","timestamp":"2026-09-18T07:05:10.000Z","cwd":"/wt","sessionId":"sess-a","message":{"id":"msg_p1","model":"claude-opus-5","role":"assistant","usage":{"input_tokens":3,"cache_creation_input_tokens":50,"cache_read_input_tokens":0,"output_tokens":40}}}
{"type":"assistant","timestamp":"2026-09-18T07:06:00.000Z","cwd":"/wt","sessionId":"sess-a","message":{"id":"msg_p2","model":"claude-opus-5","role":"assistant","usage":{"input_tokens":1,"cache_creation_input_tokens":0,"cache_read_input_tokens":50,"output_tokens":10}}}
{"type":"assistant","timestamp":"2026-09-18T07:06:00.500Z","cwd":"/wt","sessionId":"sess-a","message":{"id":"msg_p2","model":"claude-opus-5","role":"assistant","usage":{"input_tokens":1,"cache_creation_input_tokens":0,"cache_read_input_tokens":50,"output_tokens":10}}}
{"type":"assistant","timestamp":"2026-09-18T09:00:00.000Z","cwd":"/wt","sessionId":"sess-a","message":{"id":"msg_p3","model":"claude-opus-5","role":"assistant","usage":{"input_tokens":999,"cache_creation_input_tokens":0,"cache_read_input_tokens":0,"output_tokens":999}}}
{"type":"assistant","timestamp":"2026-09-18T07:07:00.000Z","cwd":"/elsewhere","sessionId":"sess-a","message":{"id":"msg_p4","model":"claude-opus-5","role":"assistant","usage":{"input_tokens":777,"cache_creation_input_tokens":0,"cache_read_input_tokens":0,"output_tokens":777}}}
```

`testdata/claude-project/sess-a/subagents/agent-x.jsonl`:

```
{"type":"assistant","timestamp":"2026-09-18T07:08:00.000Z","cwd":"/wt","sessionId":"sess-a","message":{"id":"msg_s1","model":"claude-haiku-4-5","role":"assistant","usage":{"input_tokens":5,"cache_creation_input_tokens":0,"cache_read_input_tokens":20,"output_tokens":7}}}
```

- [ ] **Step 2: Write the failing tests.**

`internal/usage/claude_test.go`:

```go
package usage

import (
	"os"
	"testing"
	"time"
)

func mustOpen(t *testing.T, path string) *os.File {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { f.Close() })
	return f
}

// mustTemp writes body to a temp file and returns it open at offset 0,
// plus its path.
func mustTemp(t *testing.T, body string) (*os.File, string) {
	t.Helper()
	f, err := os.CreateTemp(t.TempDir(), "fixture")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString(body); err != nil {
		t.Fatal(err)
	}
	if _, err := f.Seek(0, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { f.Close() })
	return f, f.Name()
}

func TestProjectSlug(t *testing.T) {
	cases := map[string]string{
		"/home/fuad/projects/relay":  "-home-fuad-projects-relay",
		"/home/fuad/.claude/projects": "-home-fuad--claude-projects",
		"/home/fuad":                  "-home-fuad",
		"/home/fuad/apps/google-cloud-sdk": "-home-fuad-apps-google-cloud-sdk",
		"/home/fuad/.config/nvim":     "-home-fuad--config-nvim",
	}
	for in, want := range cases {
		if got := ProjectSlug(in); got != want {
			t.Errorf("ProjectSlug(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestClaudeStreamUsesResultEvent(t *testing.T) {
	got := claudeStream(mustOpen(t, "testdata/claude-stream.jsonl"), "anthropic")
	if len(got) != 1 {
		t.Fatalf("samples = %d, want 1 (the result event)", len(got))
	}
	s := got[0]
	if !s.HasCost || s.USD < 0.0298 || s.USD > 0.03 {
		t.Errorf("cost = %v/%v, want measured 0.0299", s.USD, s.HasCost)
	}
	if s.Tokens != (Tokens{In: 12, CacheWrite: 100, CacheRead: 100, Out: 25}) {
		t.Errorf("tokens = %+v", s.Tokens)
	}
	if s.Model != "claude-sonnet-5" || s.Provider != "anthropic" {
		t.Errorf("model/provider = %q/%q", s.Model, s.Provider)
	}
}

func TestClaudeStreamKilledFallsBackToAssistantEvents(t *testing.T) {
	got := claudeStream(mustOpen(t, "testdata/claude-stream-killed.jsonl"), "anthropic")
	if len(got) != 2 {
		t.Fatalf("samples = %d, want 2 (msg_1, msg_2 deduped)", len(got))
	}
	var sum Tokens
	for _, s := range got {
		if s.HasCost {
			t.Error("assistant events carry no dollars; HasCost must be false")
		}
		sum = sum.Add(s.Tokens)
	}
	if sum != (Tokens{In: 12, CacheWrite: 100, CacheRead: 100, Out: 25}) {
		t.Errorf("sum = %+v", sum)
	}
}

func TestClaudeStreamEmpty(t *testing.T) {
	f, _ := mustTemp(t, "relay-exit:0\n")
	if got := claudeStream(f, "anthropic"); len(got) != 0 {
		t.Errorf("empty stream = %d samples, want 0", len(got))
	}
}

func TestClaudeProjectWindowCwdAndDedupe(t *testing.T) {
	start := time.Date(2026, 9, 18, 7, 0, 0, 0, time.UTC)
	end := time.Date(2026, 9, 18, 8, 0, 0, 0, time.UTC)
	got := claudeProject(os.DirFS("testdata/claude-project"), "/wt", start, end, "anthropic")
	// msg_p1, msg_p2 (once), msg_s1 from the subagent. Not p3 (outside), not p4 (other cwd).
	if len(got) != 3 {
		t.Fatalf("samples = %d, want 3: %+v", len(got), got)
	}
	var sum Tokens
	models := map[string]bool{}
	for _, s := range got {
		if s.HasCost {
			t.Error("pane transcripts carry no dollars")
		}
		sum = sum.Add(s.Tokens)
		models[s.Model] = true
	}
	if sum != (Tokens{In: 9, CacheWrite: 50, CacheRead: 70, Out: 57}) {
		t.Errorf("sum = %+v", sum)
	}
	if !models["claude-opus-5"] || !models["claude-haiku-4-5"] {
		t.Errorf("models = %v, want the subagent's model included", models)
	}
}

func TestClaudeProjectMissingDir(t *testing.T) {
	got := claudeProject(os.DirFS(t.TempDir()), "/wt", time.Time{}, time.Now(), "anthropic")
	if len(got) != 0 {
		t.Errorf("empty dir = %d samples", len(got))
	}
}
```

- [ ] **Step 3: Run to verify they fail.**

Run: `go test ./internal/usage/ -run 'TestProjectSlug|TestClaude'`
Expected: build failure, undefined `ProjectSlug`, `claudeStream`, `claudeProject`.

- [ ] **Step 4: Write `claude.go`.**

```go
package usage

import (
	"bufio"
	"encoding/json"
	"io"
	"io/fs"
	"path"
	"strings"
	"time"
)

// maxLine bounds one stream or transcript line; claude's tool results can
// run to megabytes.
const maxLine = 16 << 20

// ProjectSlug is the directory name claude keeps a cwd's transcripts under
// (~/.claude/projects/<slug>): every byte of the absolute path outside
// [A-Za-z0-9] becomes '-'. Verified against five entries on 2026-09-18.
func ProjectSlug(cwd string) string {
	var b strings.Builder
	for i := 0; i < len(cwd); i++ {
		c := cwd[i]
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9':
			b.WriteByte(c)
		default:
			b.WriteByte('-')
		}
	}
	return b.String()
}

// claudeUsage is the usage object on assistant messages and result events.
type claudeUsage struct {
	Input      int64 `json:"input_tokens"`
	CacheWrite int64 `json:"cache_creation_input_tokens"`
	CacheRead  int64 `json:"cache_read_input_tokens"`
	Output     int64 `json:"output_tokens"`
}

func (u claudeUsage) tokens() Tokens {
	return Tokens{In: u.Input, CacheRead: u.CacheRead, CacheWrite: u.CacheWrite, Out: u.Output}
}

// claudeEvent covers the fields read from both the headless stream and the
// pane transcript; each record uses a subset.
type claudeEvent struct {
	Type      string  `json:"type"`
	Subtype   string  `json:"subtype"`
	Model     string  `json:"model"` // system/init only
	Timestamp string  `json:"timestamp"`
	CWD       string  `json:"cwd"`
	Message   *struct {
		ID    string       `json:"id"`
		Model string       `json:"model"`
		Usage *claudeUsage `json:"usage"`
	} `json:"message"`
	// result event only
	TotalCostUSD *float64     `json:"total_cost_usd"`
	Usage        *claudeUsage `json:"usage"`
}

func scanLines(r io.Reader, fn func(line []byte)) {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64<<10), maxLine)
	for sc.Scan() {
		line := sc.Bytes()
		if len(line) == 0 || line[0] != '{' {
			continue
		}
		fn(line)
	}
}

// claudeStream reads a headless round's stream-json. The result event's
// usage is the sample (HasCost when total_cost_usd is present); with no
// result event, the assistant events deduped by message.id are the
// samples, tokens only. Model: last assistant message.model, else init.
func claudeStream(r io.Reader, fallbackProvider string) []Sample {
	var (
		model     string
		result    *Sample
		assistant []Sample
		seen      = map[string]bool{}
	)
	scanLines(r, func(line []byte) {
		var ev claudeEvent
		if json.Unmarshal(line, &ev) != nil {
			return
		}
		switch ev.Type {
		case "system":
			if ev.Subtype == "init" && ev.Model != "" && model == "" {
				model = ev.Model
			}
		case "assistant":
			if ev.Message == nil || ev.Message.Usage == nil || seen[ev.Message.ID] {
				return
			}
			seen[ev.Message.ID] = true
			if ev.Message.Model != "" {
				model = ev.Message.Model
			}
			assistant = append(assistant, Sample{Provider: fallbackProvider, Model: ev.Message.Model, Tokens: ev.Message.Usage.tokens()})
		case "result":
			if ev.Usage == nil {
				return
			}
			s := Sample{Provider: fallbackProvider, Tokens: ev.Usage.tokens()}
			if ev.TotalCostUSD != nil {
				s.USD, s.HasCost = *ev.TotalCostUSD, true
			}
			result = &s
		}
	})
	if result != nil {
		result.Model = model
		return []Sample{*result}
	}
	for i := range assistant {
		if assistant[i].Model == "" {
			assistant[i].Model = model
		}
	}
	return assistant
}

// claudeProject reads every *.jsonl under fsys (subagents included) and
// returns one sample per assistant message.id whose timestamp is inside
// [start, end] and whose cwd equals worktree. Files whose modtime is
// before start are skipped unread: a file untouched since before the
// round cannot hold a record inside it.
func claudeProject(fsys fs.FS, worktree string, start, end time.Time, provider string) []Sample {
	var out []Sample
	seen := map[string]bool{}
	_ = fs.WalkDir(fsys, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || path.Ext(p) != ".jsonl" {
			return nil
		}
		if info, err := d.Info(); err == nil && info.ModTime().Before(start) {
			return nil
		}
		f, err := fsys.Open(p)
		if err != nil {
			return nil
		}
		defer f.Close()
		scanLines(f, func(line []byte) {
			var ev claudeEvent
			if json.Unmarshal(line, &ev) != nil || ev.Type != "assistant" || ev.Message == nil || ev.Message.Usage == nil {
				return
			}
			if ev.CWD != worktree || seen[ev.Message.ID] {
				return
			}
			ts, err := time.Parse(time.RFC3339Nano, ev.Timestamp)
			if err != nil || ts.Before(start) || ts.After(end) {
				return
			}
			seen[ev.Message.ID] = true
			out = append(out, Sample{Provider: provider, Model: ev.Message.Model, Tokens: ev.Message.Usage.tokens()})
		})
		return nil
	})
	return out
}
```

- [ ] **Step 5: Run to verify they pass.**

Run: `go test -race -count=1 ./internal/usage/`
Expected: PASS.

- [ ] **Step 6: Mutation check.** Remove the `seen[ev.Message.ID]` guard in
`claudeProject`. Run `go test ./internal/usage/ -run TestClaudeProjectWindowCwdAndDedupe`.
Expected: FAIL (4 samples). Revert.

- [ ] **Step 7: Commit.**

```bash
git add internal/usage/
git commit -m "feat(usage): claude readers for the headless stream and pane transcripts (#142)"
```

---

### Task 3: agy and opencode readers

**Files:**
- Create: `internal/usage/agy.go`, `internal/usage/opencode.go`,
  `internal/usage/exec.go`,
  `internal/usage/testdata/agy-stream.jsonl`,
  `internal/usage/testdata/agy-stream-killed.jsonl`,
  `internal/usage/testdata/opencode-stream.jsonl`,
  `internal/usage/testdata/opencode-db.json`
- Test: `internal/usage/agy_test.go`, `internal/usage/opencode_test.go`

**Interfaces:**
- Consumes: `Sample`, `Tokens`, `scanLines` (Tasks 1-2).
- Produces:

```go
// Exec runs a binary and returns its stdout. Only sqlite3 goes through
// it. cmd/relay wires an exec.CommandContext wrapper; tests wire a fake;
// nil means the binary is not available.
type Exec interface {
	Run(ctx context.Context, bin string, args ...string) ([]byte, error)
}

func agyStream(r io.Reader, fallbackProvider, fallbackModel string) []Sample
func opencodeStream(r io.Reader, provider, model string) []Sample

// OpencodeQuery is the one SQL statement relay runs against opencode.db,
// with the worktree path embedded ('' doubled) and the window in epoch ms.
func OpencodeQuery(worktree string, start, end time.Time) string

// opencodeDB runs sqlite3 -readonly -json <db> <query> through exec and
// parses the rows. note is non-empty when nothing could be read and says why.
func opencodeDB(ctx context.Context, exec Exec, dbPath, worktree string, start, end time.Time) (samples []Sample, note string)

// opencodeRows parses `sqlite3 -json` output: [{"data":"<json>"}, ...].
func opencodeRows(out []byte) []Sample
```

- [ ] **Step 1: Write the fixtures.**

`testdata/agy-stream.jsonl` (verified shape, `internal/transcript/testdata/agy.jsonl`):

```
{"event":"init","conversation_id":"conv-1","init":{"model":"gemini-3.8-flash","cwd":"/wt","tools":["view_file"]}}
{"event":"step_update","step_update":{"conversation_id":"conv-1","step_index":0,"state":"DONE","step_type":"user_input"}}
{"event":"step_update","step_update":{"conversation_id":"conv-1","step_index":1,"state":"DONE","step_type":"agent_response","duration_seconds":6.6,"usage":{"input_tokens":1000,"output_tokens":40,"thinking_tokens":10,"cache_read_tokens":200,"total_tokens":1250}}}
{"event":"step_update","step_update":{"conversation_id":"conv-1","step_index":2,"state":"DONE","step_type":"agent_response","duration_seconds":2.1,"usage":{"input_tokens":500,"output_tokens":35,"thinking_tokens":5,"cache_read_tokens":300,"total_tokens":840}}}
{"event":"result","result":{"conversation_id":"conv-1","status":"SUCCESS","response":"","duration_seconds":9.7,"num_turns":2,"usage":{"input_tokens":1500,"output_tokens":75,"thinking_tokens":15,"cache_read_tokens":500,"total_tokens":2090}}}
relay-exit:0
```

`testdata/agy-stream-killed.jsonl`: the same without the `result` line,
ending `relay-exit:137`.

`testdata/opencode-stream.jsonl` (verified shape, `internal/transcript/testdata/opencode.jsonl`;
no model field anywhere):

```
{"type":"step_start","timestamp":1789589781193,"sessionID":"ses_1","part":{"id":"prt_1","sessionID":"ses_1","messageID":"msg_1","type":"step-start"}}
{"type":"text","timestamp":1789589781500,"sessionID":"ses_1","part":{"id":"prt_2","sessionID":"ses_1","messageID":"msg_1","type":"text","text":"working"}}
{"type":"step_finish","timestamp":1789589782193,"sessionID":"ses_1","part":{"id":"prt_3","sessionID":"ses_1","messageID":"msg_1","type":"step-finish","reason":"tool-calls","cost":0.001,"tokens":{"input":1000,"output":49,"reasoning":57,"cache":{"read":10,"write":20}}}}
{"type":"step_finish","timestamp":1789589783193,"sessionID":"ses_1","part":{"id":"prt_4","sessionID":"ses_1","messageID":"msg_2","type":"step-finish","reason":"stop","cost":0.002,"tokens":{"input":2000,"output":1,"reasoning":0,"cache":{"read":30,"write":0}}}}
relay-exit:0
```

`testdata/opencode-db.json` -- exactly what `sqlite3 -json` prints for the
query (each `data` is a JSON string; a user row and an assistant row with
zero tokens are present and must be ignored/counted correctly):

```json
[{"data":"{\"role\":\"user\",\"time\":{\"created\":1789150607700}}"},
{"data":"{\"parentID\":\"msg_u\",\"role\":\"assistant\",\"agent\":\"plan-executor\",\"path\":{\"cwd\":\"/wt\",\"root\":\"/wt\"},\"cost\":0.00135925,\"tokens\":{\"total\":8896,\"input\":8825,\"output\":64,\"reasoning\":7,\"cache\":{\"write\":0,\"read\":0}},\"modelID\":\"z-ai/glm-5.3-flash\",\"providerID\":\"openrouter\",\"time\":{\"created\":1789150607770,\"completed\":1789150611274}}"},
{"data":"{\"parentID\":\"msg_u\",\"role\":\"assistant\",\"agent\":\"plan-executor\",\"cost\":0,\"tokens\":{\"input\":0,\"output\":0,\"reasoning\":0,\"cache\":{\"read\":0,\"write\":0}},\"modelID\":\"z-ai/glm-5.3-flash\",\"providerID\":\"openrouter\",\"time\":{\"created\":1789150611276}}"}]
```

- [ ] **Step 2: Write the failing tests.**

`internal/usage/agy_test.go`:

```go
package usage

import "testing"

func TestAgyStreamUsesResult(t *testing.T) {
	got := agyStream(mustOpen(t, "testdata/agy-stream.jsonl"), "google", "fallback-model")
	if len(got) != 1 {
		t.Fatalf("samples = %d, want 1", len(got))
	}
	s := got[0]
	if s.HasCost {
		t.Error("agy reports no dollars; HasCost must be false")
	}
	// Out = output 75 + thinking 15.
	if s.Tokens != (Tokens{In: 1500, CacheRead: 500, Out: 90}) {
		t.Errorf("tokens = %+v", s.Tokens)
	}
	if s.Model != "gemini-3.8-flash" || s.Provider != "google" {
		t.Errorf("model/provider = %q/%q, want the init event's model", s.Model, s.Provider)
	}
}

func TestAgyStreamKilledSumsSteps(t *testing.T) {
	got := agyStream(mustOpen(t, "testdata/agy-stream-killed.jsonl"), "google", "fallback-model")
	if len(got) != 2 {
		t.Fatalf("samples = %d, want 2 agent_response steps", len(got))
	}
	var sum Tokens
	for _, s := range got {
		sum = sum.Add(s.Tokens)
	}
	if sum != (Tokens{In: 1500, CacheRead: 500, Out: 90}) {
		t.Errorf("sum = %+v", sum)
	}
}

func TestAgyStreamNoInitUsesFallbackModel(t *testing.T) {
	f, _ := mustTemp(t, `{"event":"result","result":{"usage":{"input_tokens":1,"output_tokens":1,"thinking_tokens":0,"cache_read_tokens":0}}}`)
	got := agyStream(f, "google", "fallback-model")
	if len(got) != 1 || got[0].Model != "fallback-model" {
		t.Errorf("got %+v, want the candidate's model when the stream names none", got)
	}
}
```

`mustTemp` was defined in `claude_test.go` in Task 2; if it is missing, add it there:

```go
func mustTemp(t *testing.T, body string) (*os.File, string) {
	t.Helper()
	f, err := os.CreateTemp(t.TempDir(), "fixture")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString(body); err != nil {
		t.Fatal(err)
	}
	if _, err := f.Seek(0, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { f.Close() })
	return f, f.Name()
}
```

`internal/usage/opencode_test.go`:

```go
package usage

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"
)

type fakeExec struct {
	out  []byte
	err  error
	bin  string
	args []string
}

func (f *fakeExec) Run(_ context.Context, bin string, args ...string) ([]byte, error) {
	f.bin, f.args = bin, args
	return f.out, f.err
}

func TestOpencodeStreamOneSamplePerStep(t *testing.T) {
	got := opencodeStream(mustOpen(t, "testdata/opencode-stream.jsonl"), "openrouter", "z-ai/glm-5.3-flash")
	if len(got) != 2 {
		t.Fatalf("samples = %d, want 2 step_finish parts", len(got))
	}
	// Out = output + reasoning.
	if got[0].Tokens != (Tokens{In: 1000, CacheRead: 10, CacheWrite: 20, Out: 106}) {
		t.Errorf("first = %+v", got[0].Tokens)
	}
	if !got[0].HasCost || got[0].USD != 0.001 {
		t.Errorf("first cost = %v/%v, want measured 0.001", got[0].USD, got[0].HasCost)
	}
	if got[1].Model != "z-ai/glm-5.3-flash" || got[1].Provider != "openrouter" {
		t.Errorf("model/provider = %q/%q, want the candidate's (the stream has none)", got[1].Model, got[1].Provider)
	}
}

func TestOpencodeQueryShape(t *testing.T) {
	start := time.UnixMilli(1789150600000).UTC()
	end := time.UnixMilli(1789150700000).UTC()
	q := OpencodeQuery("/wt/o'brien", start, end)
	for _, want := range []string{
		"select m.data from message m join session s on s.id = m.session_id",
		"s.directory = '/wt/o''brien'",
		"m.time_created between 1789150600000 and 1789150700000",
	} {
		if !strings.Contains(q, want) {
			t.Errorf("query %q\n  lacks %q", q, want)
		}
	}
}

func TestOpencodeRows(t *testing.T) {
	raw, err := os.ReadFile("testdata/opencode-db.json")
	if err != nil {
		t.Fatal(err)
	}
	got := opencodeRows(raw)
	// The user row is skipped; both assistant rows count (a zero row is a
	// record, not an absence).
	if len(got) != 2 {
		t.Fatalf("samples = %d, want 2: %+v", len(got), got)
	}
	if got[0].Tokens != (Tokens{In: 8825, Out: 71}) || !got[0].HasCost || got[0].USD != 0.00135925 {
		t.Errorf("first = %+v", got[0])
	}
	if got[0].Model != "z-ai/glm-5.3-flash" || got[0].Provider != "openrouter" {
		t.Errorf("model/provider = %q/%q", got[0].Model, got[0].Provider)
	}
}

func TestOpencodeDBRunsSqlite3ReadOnly(t *testing.T) {
	raw, _ := os.ReadFile("testdata/opencode-db.json")
	fe := &fakeExec{out: raw}
	db := mustExisting(t)
	got, note := opencodeDB(context.Background(), fe, db, "/wt", time.UnixMilli(0), time.UnixMilli(1))
	if note != "" || len(got) != 2 {
		t.Fatalf("got %d samples, note %q", len(got), note)
	}
	if fe.bin != "sqlite3" {
		t.Errorf("bin = %q", fe.bin)
	}
	joined := strings.Join(fe.args, " ")
	if !strings.Contains(joined, "-readonly") || !strings.Contains(joined, "-json") || !strings.Contains(joined, db) {
		t.Errorf("args = %v, want -readonly -json <db> <query>", fe.args)
	}
}

func TestOpencodeDBNotes(t *testing.T) {
	db := mustExisting(t)
	if _, note := opencodeDB(context.Background(), nil, db, "/wt", time.Time{}, time.Now()); note != "sqlite3 not on PATH" {
		t.Errorf("nil exec: note = %q", note)
	}
	if _, note := opencodeDB(context.Background(), &fakeExec{}, "/nonexistent/opencode.db", "/wt", time.Time{}, time.Now()); note != "no opencode store" {
		t.Errorf("missing db: note = %q", note)
	}
	fe := &fakeExec{err: errors.New("Error: unable to open database\nsecond line")}
	if _, note := opencodeDB(context.Background(), fe, db, "/wt", time.Time{}, time.Now()); note != "sqlite3: Error: unable to open database" {
		t.Errorf("exec error: note = %q, want the first stderr line", note)
	}
}

func mustExisting(t *testing.T) string {
	t.Helper()
	_, name := mustTemp(t, "")
	return name
}
```

- [ ] **Step 3: Run to verify they fail.**

Run: `go test ./internal/usage/ -run 'TestAgy|TestOpencode'`
Expected: build failure, undefined symbols.

- [ ] **Step 4: Write `exec.go`, `agy.go`, `opencode.go`.**

`exec.go`:

```go
package usage

import "context"

// Exec runs a binary and returns its stdout. Only sqlite3 goes through it:
// cmd/relay wires an exec.CommandContext wrapper, tests wire a fake, and
// nil means the binary is not available.
type Exec interface {
	Run(ctx context.Context, bin string, args ...string) ([]byte, error)
}
```

`agy.go`:

```go
package usage

import (
	"encoding/json"
	"io"
)

type agyUsage struct {
	Input     int64 `json:"input_tokens"`
	Output    int64 `json:"output_tokens"`
	Thinking  int64 `json:"thinking_tokens"`
	CacheRead int64 `json:"cache_read_tokens"`
}

func (u agyUsage) tokens() Tokens {
	return Tokens{In: u.Input, CacheRead: u.CacheRead, Out: u.Output + u.Thinking}
}

type agyEvent struct {
	Event string `json:"event"`
	Init  *struct {
		Model string `json:"model"`
	} `json:"init"`
	StepUpdate *struct {
		StepType string    `json:"step_type"`
		Usage    *agyUsage `json:"usage"`
	} `json:"step_update"`
	Result *struct {
		Usage *agyUsage `json:"usage"`
	} `json:"result"`
}

// agyStream reads a headless round's stream. The result event's usage is
// the sample; with no result event, each agent_response step_update with
// usage is a sample. agy reports no dollars. Model: the init event's, else
// the candidate's. agy in a pane keeps no usage record at all (verified
// 2026-09-18), so there is no pane reader.
func agyStream(r io.Reader, fallbackProvider, fallbackModel string) []Sample {
	model := fallbackModel
	var result *Sample
	var steps []Sample
	scanLines(r, func(line []byte) {
		var ev agyEvent
		if json.Unmarshal(line, &ev) != nil {
			return
		}
		switch ev.Event {
		case "init":
			if ev.Init != nil && ev.Init.Model != "" {
				model = ev.Init.Model
			}
		case "step_update":
			if ev.StepUpdate != nil && ev.StepUpdate.StepType == "agent_response" && ev.StepUpdate.Usage != nil {
				steps = append(steps, Sample{Provider: fallbackProvider, Tokens: ev.StepUpdate.Usage.tokens()})
			}
		case "result":
			if ev.Result != nil && ev.Result.Usage != nil {
				result = &Sample{Provider: fallbackProvider, Tokens: ev.Result.Usage.tokens()}
			}
		}
	})
	if result != nil {
		result.Model = model
		return []Sample{*result}
	}
	for i := range steps {
		steps[i].Model = model
	}
	return steps
}
```

`opencode.go`:

```go
package usage

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"time"
)

type opencodeTokens struct {
	Input     int64 `json:"input"`
	Output    int64 `json:"output"`
	Reasoning int64 `json:"reasoning"`
	Cache     struct {
		Read  int64 `json:"read"`
		Write int64 `json:"write"`
	} `json:"cache"`
}

func (t opencodeTokens) tokens() Tokens {
	return Tokens{In: t.Input, CacheRead: t.Cache.Read, CacheWrite: t.Cache.Write, Out: t.Output + t.Reasoning}
}

type opencodeEvent struct {
	Type string `json:"type"`
	Part *struct {
		Type   string          `json:"type"`
		Cost   *float64        `json:"cost"`
		Tokens *opencodeTokens `json:"tokens"`
	} `json:"part"`
}

// opencodeStream reads a headless round's `run --format json` stream: one
// sample per step_finish part, dollars as opencode computed them. The
// stream names no model, so provider and model are the candidate's.
func opencodeStream(r io.Reader, provider, model string) []Sample {
	var out []Sample
	scanLines(r, func(line []byte) {
		var ev opencodeEvent
		if json.Unmarshal(line, &ev) != nil || ev.Type != "step_finish" || ev.Part == nil || ev.Part.Tokens == nil {
			return
		}
		s := Sample{Provider: provider, Model: model, Tokens: ev.Part.Tokens.tokens()}
		if ev.Part.Cost != nil {
			s.USD, s.HasCost = *ev.Part.Cost, true
		}
		out = append(out, s)
	})
	return out
}

// OpencodeQuery is the one statement relay runs against opencode.db. The
// worktree path is the only interpolated value and is quoted with '
// doubled; the window is epoch milliseconds, as message.time_created is.
func OpencodeQuery(worktree string, start, end time.Time) string {
	dir := strings.ReplaceAll(worktree, "'", "''")
	return fmt.Sprintf(
		"select m.data from message m join session s on s.id = m.session_id where s.directory = '%s' and m.time_created between %d and %d order by m.time_created",
		dir, start.UnixMilli(), end.UnixMilli())
}

// opencodeDB runs the query through sqlite3 and parses the rows. note is
// non-empty when nothing could be read and says why; a query that returns
// no rows is zero samples and an empty note.
func opencodeDB(ctx context.Context, exec Exec, dbPath, worktree string, start, end time.Time) ([]Sample, string) {
	if exec == nil {
		return nil, "sqlite3 not on PATH"
	}
	if _, err := os.Stat(dbPath); err != nil {
		return nil, "no opencode store"
	}
	out, err := exec.Run(ctx, "sqlite3", "-readonly", "-json", dbPath, OpencodeQuery(worktree, start, end))
	if err != nil {
		first, _, _ := strings.Cut(strings.TrimSpace(err.Error()), "\n")
		return nil, "sqlite3: " + first
	}
	return opencodeRows(out), ""
}

type opencodeRow struct {
	Data string `json:"data"`
}

type opencodeMessage struct {
	Role       string          `json:"role"`
	Cost       *float64        `json:"cost"`
	Tokens     *opencodeTokens `json:"tokens"`
	ModelID    string          `json:"modelID"`
	ProviderID string          `json:"providerID"`
}

// opencodeRows parses `sqlite3 -json` output: a JSON array of {"data": "<json>"}.
// Empty output (no rows) is nil, not an error.
func opencodeRows(out []byte) []Sample {
	if len(strings.TrimSpace(string(out))) == 0 {
		return nil
	}
	var rows []opencodeRow
	if err := json.Unmarshal(out, &rows); err != nil {
		return nil
	}
	var samples []Sample
	for _, r := range rows {
		var m opencodeMessage
		if json.Unmarshal([]byte(r.Data), &m) != nil || m.Role != "assistant" || m.Tokens == nil {
			continue
		}
		s := Sample{Provider: m.ProviderID, Model: m.ModelID, Tokens: m.Tokens.tokens()}
		if m.Cost != nil {
			s.USD, s.HasCost = *m.Cost, true
		}
		samples = append(samples, s)
	}
	return samples
}
```

- [ ] **Step 5: Run to verify they pass.**

Run: `go test -race -count=1 ./internal/usage/`
Expected: PASS.

- [ ] **Step 6: Mutation check.** In `OpencodeQuery`, drop the
`ReplaceAll`. Run `go test ./internal/usage/ -run TestOpencodeQueryShape`.
Expected: FAIL. Revert.

- [ ] **Step 7: Commit.**

```bash
git add internal/usage/
git commit -m "feat(usage): agy and opencode readers; opencode pane via sqlite3 (#142)"
```

---

### Task 4: `Source`, `Reader`, `New` -- dispatch by harness and mode

**Files:**
- Create: `internal/usage/source.go`
- Test: `internal/usage/source_test.go`

**Interfaces:**
- Consumes: every unexported reader (Tasks 2-3), `Exec`.
- Produces (what `internal/relay` calls):

```go
type Mode string
const (
	ModePane     Mode = "pane"
	ModeHeadless Mode = "headless"
)

type Source struct {
	Harness    string // "claude" | "agy" | "opencode"
	Mode       Mode
	Provider   string // candidate's; "" for an adopted builder
	Model      string // candidate's; "" for an adopted builder
	Plan       bool
	StreamPath string // headless: NNN-builder.jsonl
	Worktree   string // pane: the binding's worktree; "" for a --cwd binding
	Start, End time.Time
}

// Reader turns a Source into samples. Never errors: an unreadable source
// is zero samples and a note saying why.
type Reader interface {
	Read(ctx context.Context, src Source) (samples []Sample, note string)
}

// New returns the production reader. exec may be nil (sqlite3 absent).
// home is the user's home directory: claude's project root is
// <home>/.claude/projects and opencode's store is
// <home>/.local/share/opencode/opencode.db.
func New(exec Exec, home string) Reader
```

- [ ] **Step 1: Write the failing tests.**

`internal/usage/source_test.go`:

```go
package usage

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func copyFixture(t *testing.T, src, dst string) {
	t.Helper()
	raw, err := os.ReadFile(src)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dst, raw, 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestReadHeadlessDispatch(t *testing.T) {
	dir := t.TempDir()
	r := New(nil, dir)
	cases := []struct {
		harness, fixture string
		samples          int
	}{
		{"claude", "testdata/claude-stream.jsonl", 1},
		{"agy", "testdata/agy-stream.jsonl", 1},
		{"opencode", "testdata/opencode-stream.jsonl", 2},
	}
	for _, c := range cases {
		path := filepath.Join(dir, c.harness+".jsonl")
		copyFixture(t, c.fixture, path)
		got, note := r.Read(context.Background(), Source{Harness: c.harness, Mode: ModeHeadless, Provider: "p", Model: "m", StreamPath: path})
		if note != "" || len(got) != c.samples {
			t.Errorf("%s: %d samples, note %q; want %d", c.harness, len(got), note, c.samples)
		}
	}
}

func TestReadHeadlessNotes(t *testing.T) {
	r := New(nil, t.TempDir())
	if _, note := r.Read(context.Background(), Source{Harness: "claude", Mode: ModeHeadless, StreamPath: "/nonexistent"}); note != "no stream" {
		t.Errorf("missing stream: note = %q", note)
	}
	_, empty := mustTemp(t, "relay-exit:0\n")
	if _, note := r.Read(context.Background(), Source{Harness: "claude", Mode: ModeHeadless, StreamPath: empty}); note != "no usage events" {
		t.Errorf("no events: note = %q", note)
	}
	if _, note := r.Read(context.Background(), Source{Harness: "unknown-harness", Mode: ModeHeadless, StreamPath: empty}); note != "no reader for unknown-harness" {
		t.Errorf("unknown harness: note = %q", note)
	}
}

func TestReadPaneClaudeUsesProjectSlug(t *testing.T) {
	home := t.TempDir()
	slug := filepath.Join(home, ".claude", "projects", ProjectSlug("/wt"))
	copyFixture(t, "testdata/claude-project/sess-a.jsonl", filepath.Join(slug, "sess-a.jsonl"))
	copyFixture(t, "testdata/claude-project/sess-a/subagents/agent-x.jsonl", filepath.Join(slug, "sess-a", "subagents", "agent-x.jsonl"))
	start := time.Date(2026, 9, 18, 7, 0, 0, 0, time.UTC)
	end := time.Date(2026, 9, 18, 8, 0, 0, 0, time.UTC)
	got, note := New(nil, home).Read(context.Background(), Source{Harness: "claude", Mode: ModePane, Provider: "anthropic", Worktree: "/wt", Start: start, End: end})
	if note != "" || len(got) != 3 {
		t.Errorf("%d samples, note %q; want 3", len(got), note)
	}
	if _, note := New(nil, home).Read(context.Background(), Source{Harness: "claude", Mode: ModePane, Worktree: "/never-seen", Start: start, End: end}); note != "no claude project dir" {
		t.Errorf("missing slug dir: note = %q", note)
	}
}

func TestReadPaneNotes(t *testing.T) {
	r := New(nil, t.TempDir())
	if _, note := r.Read(context.Background(), Source{Harness: "agy", Mode: ModePane, Worktree: "/wt"}); note != "agy keeps no usage record" {
		t.Errorf("agy pane: note = %q", note)
	}
	if _, note := r.Read(context.Background(), Source{Harness: "claude", Mode: ModePane, Worktree: ""}); note != "shared cwd" {
		t.Errorf("--cwd binding: note = %q", note)
	}
	if _, note := r.Read(context.Background(), Source{Harness: "opencode", Mode: ModePane, Worktree: "/wt"}); note != "sqlite3 not on PATH" {
		t.Errorf("opencode pane, nil exec: note = %q", note)
	}
}

func TestReadPaneOpencodeUsesHomeStore(t *testing.T) {
	home := t.TempDir()
	db := filepath.Join(home, ".local", "share", "opencode", "opencode.db")
	copyFixture(t, "testdata/opencode-db.json", db) // any existing file; the fake never opens it
	raw, _ := os.ReadFile("testdata/opencode-db.json")
	fe := &fakeExec{out: raw}
	got, note := New(fe, home).Read(context.Background(), Source{Harness: "opencode", Mode: ModePane, Worktree: "/wt", Start: time.UnixMilli(0), End: time.UnixMilli(1)})
	if note != "" || len(got) != 2 {
		t.Errorf("%d samples, note %q", len(got), note)
	}
	if len(fe.args) < 3 || fe.args[2] != db {
		t.Errorf("args = %v, want the store under home", fe.args)
	}
}
```

- [ ] **Step 2: Run to verify they fail.**

Run: `go test ./internal/usage/ -run TestRead`
Expected: build failure.

- [ ] **Step 3: Write `source.go`.**

```go
package usage

import (
	"context"
	"os"
	"path/filepath"
	"time"
)

// Mode is how the builder ran.
type Mode string

const (
	ModePane     Mode = "pane"
	ModeHeadless Mode = "headless"
)

// Source is everything a reader needs to find one round's record. It is
// built by internal/relay from a binding; this package never sees one.
type Source struct {
	Harness    string // "claude" | "agy" | "opencode"
	Mode       Mode
	Provider   string // the candidate's; "" for an adopted builder
	Model      string // the candidate's; "" for an adopted builder
	Plan       bool   // the candidate's subscription flag
	StreamPath string // headless: the round's NNN-builder.jsonl
	Worktree   string // pane: the binding's worktree; "" for a --cwd binding
	Start, End time.Time
}

// Reader turns a Source into samples. It never errors: an unreadable
// source is zero samples and a note saying why, and the round closes
// regardless.
type Reader interface {
	Read(ctx context.Context, src Source) (samples []Sample, note string)
}

type reader struct {
	exec Exec
	home string
}

// New returns the production reader. exec may be nil (sqlite3 absent);
// home is the user's home directory.
func New(exec Exec, home string) Reader { return reader{exec: exec, home: home} }

func (r reader) Read(ctx context.Context, src Source) ([]Sample, string) {
	switch src.Mode {
	case ModeHeadless:
		return r.readStream(src)
	case ModePane:
		return r.readPane(ctx, src)
	}
	return nil, "no reader for mode " + string(src.Mode)
}

func (r reader) readStream(src Source) ([]Sample, string) {
	f, err := os.Open(src.StreamPath)
	if err != nil {
		return nil, "no stream"
	}
	defer f.Close()
	var samples []Sample
	switch src.Harness {
	case "claude":
		samples = claudeStream(f, src.Provider)
	case "agy":
		samples = agyStream(f, src.Provider, src.Model)
	case "opencode":
		samples = opencodeStream(f, src.Provider, src.Model)
	default:
		return nil, "no reader for " + src.Harness
	}
	if len(samples) == 0 {
		return nil, "no usage events"
	}
	return samples, ""
}

func (r reader) readPane(ctx context.Context, src Source) ([]Sample, string) {
	switch src.Harness {
	case "agy":
		return nil, "agy keeps no usage record"
	case "claude", "opencode":
	default:
		return nil, "no reader for " + src.Harness
	}
	if src.Worktree == "" {
		return nil, "shared cwd"
	}
	switch src.Harness {
	case "claude":
		dir := filepath.Join(r.home, ".claude", "projects", ProjectSlug(src.Worktree))
		if info, err := os.Stat(dir); err != nil || !info.IsDir() {
			return nil, "no claude project dir"
		}
		samples := claudeProject(os.DirFS(dir), src.Worktree, src.Start, src.End, src.Provider)
		if len(samples) == 0 {
			return nil, "no usage events"
		}
		return samples, ""
	default: // opencode
		db := filepath.Join(r.home, ".local", "share", "opencode", "opencode.db")
		return opencodeDB(ctx, r.exec, db, src.Worktree, src.Start, src.End)
	}
}
```

- [ ] **Step 4: Run to verify they pass.**

Run: `go test -race -count=1 ./internal/usage/`
Expected: PASS.

- [ ] **Step 5: Commit.**

```bash
git add internal/usage/
git commit -m "feat(usage): Source and Reader dispatch by harness and mode (#142)"
```

---

### Task 5: the record -- `LogEntry.Usage`, `Candidate.Plan`

**Files:**
- Modify: `internal/store/log.go` (the `LogEntry` struct, after `Tree`),
  `internal/candidate/candidate.go:50-58` (the `Candidate` struct)
- Test: `internal/store/log_test.go`, `internal/candidate/candidate_test.go`

**Interfaces:**
- Consumes: `usage.Usage` (Task 1).
- Produces:

```go
// store.LogEntry gains
Usage *usage.Usage `json:"usage,omitempty"`

// candidate.Candidate gains
Plan bool `json:"plan,omitempty"`
```

- [ ] **Step 1: Write the failing tests.**

Append to `internal/store/log_test.go`:

```go
func TestLogEntryUsageRoundTrip(t *testing.T) {
	s := New(t.TempDir())
	with := LogEntry{
		TS: time.Unix(1, 0).UTC(), Round: 3, Direction: DirToPlanner, Kind: KindReport, Payload: "p",
		Usage: &usage.Usage{Harness: "claude", Provider: "anthropic", Model: "claude-sonnet-5", DurationMS: 4200,
			Tokens: usage.Tokens{In: 1, CacheRead: 2, CacheWrite: 3, Out: 4},
			Cost:   usage.Cost{USD: 0.5, Basis: usage.Measured}, Samples: 1},
	}
	without := LogEntry{TS: time.Unix(2, 0).UTC(), Round: 3, Direction: DirToPlanner, Kind: KindReport, Payload: "q"}
	if err := s.AppendLog("b", with); err != nil {
		t.Fatal(err)
	}
	if err := s.AppendLog("b", without); err != nil {
		t.Fatal(err)
	}
	got, err := s.ReadLog("b")
	if err != nil {
		t.Fatal(err)
	}
	if got[0].Usage == nil || *got[0].Usage != *with.Usage {
		t.Errorf("usage round trip: %+v", got[0].Usage)
	}
	if got[1].Usage != nil {
		t.Errorf("an entry without usage must read back nil, got %+v", got[1].Usage)
	}
	raw, _ := json.Marshal(without)
	if strings.Contains(string(raw), "usage") {
		t.Errorf("an entry without usage must marshal byte-identically to before: %s", raw)
	}
}
```

(Add `encoding/json`, `strings`, `time` and
`"github.com/fuad-daoud/relay/internal/usage"` to the imports as needed.)

Append to `internal/candidate/candidate_test.go`:

```go
func TestCandidatePlanFlag(t *testing.T) {
	path := filepath.Join(t.TempDir(), "candidates.json")
	body := `[{"harness":"claude","provider":"anthropic","model":"sonnet","roles":["builder"],"plan":true},
	          {"harness":"agy","provider":"google","model":"g","roles":["builder"]}]`
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	set, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	c, err := set.Lookup(Ref{Harness: "claude", Provider: "anthropic", Model: "sonnet"})
	if err != nil || !c.Plan {
		t.Errorf("plan flag not loaded: %+v, %v", c, err)
	}
	c, _ = set.Lookup(Ref{Harness: "agy", Provider: "google", Model: "g"})
	if c.Plan {
		t.Error("plan defaults to false")
	}
}
```

(`candidate_test.go` already imports `os` and `path/filepath` and writes
`candidates.json` into a temp dir the same way -- follow it.)

- [ ] **Step 2: Run to verify they fail.**

Run: `go test ./internal/store/ ./internal/candidate/`
Expected: build failure on the unknown fields.

- [ ] **Step 3: Add the fields.**

In `internal/store/log.go`, after the `Tree` field of `LogEntry`:

```go
	// Usage is what the round consumed, on report entries, and what a
	// consult consumed, on findings entries (#142). Nil on every other
	// kind and on entries written before the field existed. Its Cost.Basis
	// says whether the dollars were measured, estimated or unknown; a
	// reader that treats nil as "free" is wrong -- nil is unknown.
	Usage *usage.Usage `json:"usage,omitempty"`
```

and import `"github.com/fuad-daoud/relay/internal/usage"`.

In `internal/candidate/candidate.go`, in `Candidate` after `LimitPatterns`:

```go
	// Plan marks a subscription lane (#142): the round's cost is a quota
	// draw, and printers say "plan", never "$0" and never "free".
	Plan bool `json:"plan,omitempty"`
```

- [ ] **Step 4: Run to verify they pass.**

Run: `go test -race -count=1 ./internal/store/ ./internal/candidate/`
Expected: PASS.

- [ ] **Step 5: Commit.**

```bash
git add internal/store/ internal/candidate/
git commit -m "feat(store): usage on report and findings entries; plan flag on candidates (#142)"
```

---

### Task 6: record usage at round close and consult close

**Files:**
- Create: `internal/relay/usage.go`
- Modify: `internal/relay/herdr.go:48-75` (`Runtime`),
  `internal/relay/reconcile.go:568-600` (`queueReport`),
  `internal/relay/consult.go` (`finishConsult`)
- Test: `internal/relay/usage_test.go`, `internal/relay/fake_test.go` (a fake reader)

**Interfaces:**
- Consumes: `usage.Reader`, `usage.Source`, `usage.Fold`, `usage.Prices`
  (Tasks 1, 4); `Candidate.Plan` (Task 5).
- Produces:

```go
// Runtime gains
Usage  usage.Reader // nil -> every round records Basis unknown, note "no reader"
Prices usage.Prices // zero value -> every estimate is unknown

// usageDeadline bounds one read; the round closes regardless.
const usageDeadline = 5 * time.Second

// roundSource builds the Source for the binding's current round.
func roundSource(rt Runtime, b store.Binding, start, end time.Time) usage.Source

// consultSource builds the Source for one consult.
func consultSource(rt Runtime, b store.Binding, c store.Consult, end time.Time) usage.Source

// recordUsage reads and folds. Never nil, never errors.
func recordUsage(ctx context.Context, rt Runtime, src usage.Source) *usage.Usage
```

- [ ] **Step 1: Add a fake reader to `fake_test.go`.**

```go
// fakeUsage scripts what the usage reader returns and records the Source
// it was asked for.
type fakeUsage struct {
	samples []usage.Sample
	note    string
	sources []usage.Source
	block   bool // when true, Read waits for ctx and returns nothing
}

func (f *fakeUsage) Read(ctx context.Context, src usage.Source) ([]usage.Sample, string) {
	f.sources = append(f.sources, src)
	if f.block {
		<-ctx.Done()
		return nil, "blocked"
	}
	return f.samples, f.note
}
```

- [ ] **Step 2: Write the failing tests.**

`internal/relay/usage_test.go`:

```go
package relay

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/fuad-daoud/relay/internal/git"
	"github.com/fuad-daoud/relay/internal/herdr"
	"github.com/fuad-daoud/relay/internal/store"
	"github.com/fuad-daoud/relay/internal/usage"
)

// closeRound drives the ordinary close: report file, done marker, idle
// builder, one reconcile. Returns the report entry.
func closeRound(t *testing.T, rt Runtime, b store.Binding) store.LogEntry {
	t.Helper()
	if err := os.WriteFile(rt.Store.ReportPath(b.Name, b.Round), []byte("report"), 0o644); err != nil {
		t.Fatal(err)
	}
	touch(t, rt.Store.DonePath(b.Name, b.Round))
	agents := []herdr.Agent{plannerWith(herdr.StatusWorking, false), builderAgent(herdr.StatusIdle)}
	if _, err := reconcile(t, rt, b, agents); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	entries, err := rt.Store.ReadLog(b.Name)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if e.Kind == store.KindReport && e.Round == b.Round {
			return e
		}
	}
	t.Fatal("no report entry")
	return store.LogEntry{}
}

func TestRoundCloseRecordsUsage(t *testing.T) {
	f := &fakeHerdr{}
	rt, b := sentBinding(t, f)
	rt.Git = &fakeGit{snapshotTreeID: "t", diffResult: git.Diff{}}
	fu := &fakeUsage{samples: []usage.Sample{{Provider: "test", Model: "m", Tokens: usage.Tokens{In: 10, Out: 2}, USD: 0.5, HasCost: true}}}
	rt.Usage = fu
	// The round was sent at baseTime; close it 90 s later.
	rt.Now = func() time.Time { return baseTime.Add(90 * time.Second) }

	e := closeRound(t, rt, b)
	if e.Usage == nil {
		t.Fatal("report entry has no usage")
	}
	if e.Usage.Cost.Basis != usage.Measured || e.Usage.Cost.USD != 0.5 || e.Usage.Tokens != (usage.Tokens{In: 10, Out: 2}) {
		t.Errorf("usage = %+v", e.Usage)
	}
	if e.Usage.DurationMS != 90_000 {
		t.Errorf("DurationMS = %d, want 90000 (RoundStartedAt to close)", e.Usage.DurationMS)
	}
	if e.Usage.Harness != "agy" {
		t.Errorf("Harness = %q, want the builder's kind", e.Usage.Harness)
	}
	if len(fu.sources) != 1 {
		t.Fatalf("reader called %d times, want 1", len(fu.sources))
	}
	src := fu.sources[0]
	if src.Harness != "agy" || src.Mode != usage.ModePane || src.Provider != "test" || src.Model != "m" {
		t.Errorf("source = %+v", src)
	}
	if !src.Start.Equal(baseTime) || !src.End.Equal(baseTime.Add(90*time.Second)) {
		t.Errorf("window = %v..%v", src.Start, src.End)
	}
}

func TestRoundCloseWithNoReaderIsUnknownAndStillCloses(t *testing.T) {
	f := &fakeHerdr{}
	rt, b := sentBinding(t, f)
	rt.Usage = nil
	e := closeRound(t, rt, b)
	if e.Usage == nil || e.Usage.Cost.Basis != usage.Unknown || e.Usage.Note != "no reader" {
		t.Errorf("usage = %+v, want unknown/no reader", e.Usage)
	}
	got, _ := rt.Store.Load(b.Name)
	if got.Round != b.Round+1 || !got.RoundStartedAt.IsZero() {
		t.Errorf("round did not close normally: %+v", got)
	}
}

func TestRoundCloseReaderNoteIsUnknown(t *testing.T) {
	f := &fakeHerdr{}
	rt, b := sentBinding(t, f)
	rt.Usage = &fakeUsage{note: "no stream"}
	e := closeRound(t, rt, b)
	if e.Usage.Cost.Basis != usage.Unknown || e.Usage.Note != "no stream" {
		t.Errorf("usage = %+v", e.Usage)
	}
}

func TestRoundCloseReaderTimeoutStillCloses(t *testing.T) {
	f := &fakeHerdr{}
	rt, b := sentBinding(t, f)
	rt.Usage = &fakeUsage{block: true}
	done := make(chan store.LogEntry, 1)
	go func() { done <- closeRound(t, rt, b) }()
	select {
	case e := <-done:
		if e.Usage == nil || e.Usage.Cost.Basis != usage.Unknown {
			t.Errorf("usage = %+v", e.Usage)
		}
	case <-time.After(usageDeadline + 5*time.Second):
		t.Fatal("round close hung on the reader")
	}
}

func TestRoundSourceHeadlessAndPlan(t *testing.T) {
	f := &fakeHerdr{}
	rt, b := sentBinding(t, f)
	b.Builder.Mode = store.ModeHeadless
	b.Worktree = "/wt"
	src := roundSource(rt, b, baseTime, baseTime.Add(time.Minute))
	if src.Mode != usage.ModeHeadless || src.StreamPath != rt.Store.BuilderStreamPath(b.Name, b.Round) {
		t.Errorf("headless source = %+v", src)
	}
	if src.Worktree != "/wt" {
		t.Errorf("Worktree = %q", src.Worktree)
	}
	if src.Plan {
		t.Error("test candidates carry no plan flag")
	}
	b.BuilderCandidate = "" // adopted builder
	src = roundSource(rt, b, baseTime, baseTime)
	if src.Provider != "" || src.Model != "" {
		t.Errorf("adopted builder must have no candidate provider/model: %+v", src)
	}
}

func TestRecordUsageFoldsWithRuntimePrices(t *testing.T) {
	rt := Runtime{Now: func() time.Time { return baseTime }}
	rt.Usage = &fakeUsage{samples: []usage.Sample{{Provider: "test", Model: "m", Tokens: usage.Tokens{In: 1_000_000}}}}
	rt.Prices = usage.Prices{Models: map[string]usage.ModelPrice{"test/m": {In: 2}}}
	u := recordUsage(context.Background(), rt, usage.Source{Harness: "claude", Mode: usage.ModeHeadless, Start: baseTime, End: baseTime.Add(time.Second)})
	if u.Cost.Basis != usage.Estimated || u.Cost.USD != 2 {
		t.Errorf("usage = %+v, want estimated $2", u)
	}
	if u.DurationMS != 1000 || u.Harness != "claude" {
		t.Errorf("duration/harness = %d/%q", u.DurationMS, u.Harness)
	}
}
```

- [ ] **Step 3: Run to verify they fail.**

Run: `go test ./internal/relay/ -run 'TestRoundClose|TestRoundSource|TestRecordUsage'`
Expected: build failure -- `Runtime` has no `Usage`, `roundSource` undefined.

- [ ] **Step 4: Add the `Runtime` fields** in `internal/relay/herdr.go`,
after `Policy`:

```go
	// Usage reads what a round consumed from the harness's own record
	// (#142). Nil means every round records Basis unknown, note
	// "no reader"; tests that do not set it behave as a machine with no
	// reader, and rounds close exactly as before.
	Usage usage.Reader
	// Prices is ~/.config/relay/prices.json over the embedded default. The
	// zero value prices nothing, so every estimate is unknown.
	Prices usage.Prices
```

Import `"github.com/fuad-daoud/relay/internal/usage"`.

- [ ] **Step 5: Write `internal/relay/usage.go`.**

```go
package relay

import (
	"context"
	"time"

	"github.com/fuad-daoud/relay/internal/candidate"
	"github.com/fuad-daoud/relay/internal/store"
	"github.com/fuad-daoud/relay/internal/usage"
)

// usageDeadline bounds one usage read at round close. The round closes
// whatever the reader does; a slow read is a note, not a stall.
const usageDeadline = 5 * time.Second

// roundSource is everything the usage reader needs for the binding's
// current round: the builder that closed it, its candidate's provider and
// model when it was spawned from one, the round's stream file when
// headless, the worktree when pane, and the window.
func roundSource(rt Runtime, b store.Binding, start, end time.Time) usage.Source {
	src := usage.Source{
		Harness:  b.Builder.Kind,
		Mode:     usage.ModePane,
		Worktree: b.Worktree,
		Start:    start,
		End:      end,
	}
	if b.Builder.Headless() {
		src.Mode = usage.ModeHeadless
		src.StreamPath = rt.Store.BuilderStreamPath(b.Name, b.Round)
	}
	fillCandidate(&src, rt, b.BuilderCandidate)
	return src
}

// consultSource is roundSource for one consult: its own pane, the
// binding's worktree, its spawn as the window's start.
func consultSource(rt Runtime, b store.Binding, c store.Consult, end time.Time) usage.Source {
	src := usage.Source{
		Harness:  c.Endpoint.Kind,
		Mode:     usage.ModePane,
		Worktree: b.Worktree,
		Start:    c.SpawnedAt,
		End:      end,
	}
	if c.Endpoint.Headless() {
		src.Mode = usage.ModeHeadless
		src.StreamPath = c.Endpoint.LogPath // consults have no stream file today; the reader notes "no stream"
	}
	return src
}

func fillCandidate(src *usage.Source, rt Runtime, token string) {
	if token == "" || rt.Candidates == nil {
		return
	}
	ref, err := candidate.ParseRef(token)
	if err != nil {
		return
	}
	c, err := rt.Candidates.Lookup(ref)
	if err != nil {
		src.Provider, src.Model = ref.Provider, ref.Model
		return
	}
	src.Provider, src.Model, src.Plan = c.Provider, c.Model, c.Plan
}

// recordUsage reads src and folds it with the runtime's prices. It never
// returns nil and never errors: no reader, a reader that finds nothing,
// and a reader that overruns usageDeadline are all Basis unknown with a
// note.
func recordUsage(ctx context.Context, rt Runtime, src usage.Source) *usage.Usage {
	var u usage.Usage
	if rt.Usage == nil {
		u = usage.Fold(nil, rt.Prices, src.Plan, "no reader")
	} else {
		rctx, cancel := context.WithTimeout(ctx, usageDeadline)
		samples, note := rt.Usage.Read(rctx, src)
		timedOut := rctx.Err() != nil
		cancel()
		u = usage.Fold(samples, rt.Prices, src.Plan, note)
		if timedOut {
			if u.Note != "" {
				u.Note += "; "
			}
			u.Note += "timed out"
		}
	}
	u.Harness = src.Harness
	if u.Provider == "" {
		u.Provider = src.Provider
	}
	if !src.Start.IsZero() && src.End.After(src.Start) {
		u.DurationMS = src.End.Sub(src.Start).Milliseconds()
	}
	return &u
}
```

- [ ] **Step 6: Hook `queueReport`.** In `internal/relay/reconcile.go`,
at the top of `queueReport`, before the diff block, capture the window:

```go
	now := rt.Now().UTC()
	roundStart := b.RoundStartedAt
```

and where the report `entry` is built, add the field:

```go
	entry := store.LogEntry{
		TS: now, Round: b.Round,
		Direction: store.DirToPlanner, Kind: store.KindReport,
		Path: path, Payload: payload, Note: note,
		Usage: recordUsage(ctx, rt, roundSource(rt, b, roundStart, now)),
	}
```

Leave every reset below it exactly as it is -- `RoundStartedAt` is read
before it is cleared, which is the point of capturing it first.

- [ ] **Step 7: Hook `finishConsult`.** In `internal/relay/consult.go`,
where the findings `entry` is built:

```go
	entry := store.LogEntry{
		TS:        rt.Now().UTC(),
		Round:     c.Round,
		Direction: store.DirToPlanner,
		Kind:      store.KindFindings,
		Note:      note,
		Usage:     recordUsage(ctx, rt, consultSource(rt, b, c, rt.Now().UTC())),
	}
```

- [ ] **Step 8: Run the whole relay suite.**

Run: `go test -race -count=1 ./internal/relay/`
Expected: PASS, including every pre-existing test (none set `rt.Usage`, so
they record `unknown` / `no reader` and nothing else changes).

- [ ] **Step 9: Mutation check.** In `queueReport`, move the
`roundStart := b.RoundStartedAt` line to after `b.RoundStartedAt = time.Time{}`
(and use it there). Run `go test ./internal/relay/ -run TestRoundCloseRecordsUsage`.
Expected: FAIL on `DurationMS`. Revert.

- [ ] **Step 10: Commit.**

```bash
git add internal/relay/
git commit -m "feat(relay): record usage on the report entry at round close and on findings (#142)"
```

---

### Task 7: wiring, doctor, README

**Files:**
- Modify: `cmd/relay/main.go` (`newRuntime`, ~line 270-300),
  `cmd/relay/doctor.go:204-210`,
  `internal/doctor/doctor.go` (`runConfig`, `Run`),
  `README.md` (new `### Round usage` under `## Candidates`, and one row in
  the config-files list if there is one)
- Create: `cmd/relay/exec.go`
- Test: `internal/doctor/doctor_test.go`

**Interfaces:**
- Consumes: `usage.New`, `usage.LoadPrices`, `usage.Exec` (Tasks 1, 3, 4);
  `Runtime.Usage`, `Runtime.Prices` (Task 6).
- Produces:

```go
// internal/doctor
func WithUsage(pricesPath string, opencodeConfigured bool) RunOption
// three checks in Group "" : Name "sqlite3" (only when opencodeConfigured),
// Name "prices" (parse), Name "prices" (age)
```

- [ ] **Step 1: Write the failing doctor tests.**

Append to `internal/doctor/doctor_test.go` (adapt the `fakeEnv` literal to
its actual fields; `lookPaths`, `existingFiles`, `fileContents` exist):

```go
func findCheck(rep Report, name, detailSub string) (Check, bool) {
	for _, c := range rep.Checks {
		if c.Name == name && strings.Contains(c.Detail, detailSub) {
			return c, true
		}
	}
	return Check{}, false
}

func TestUsageChecks(t *testing.T) {
	t.Run("sqlite3 missing with opencode configured", func(t *testing.T) {
		env := &fakeEnv{lookPaths: map[string]string{}, existingFiles: map[string]bool{}}
		rep := Run(context.Background(), env, nil, WithUsage("/cfg/prices.json", true))
		c, ok := findCheck(rep, "sqlite3", "")
		if !ok || c.Severity != SevWarn {
			t.Errorf("want a warn row for sqlite3: %+v", rep.Checks)
		}
		if !strings.Contains(c.Detail, "unknown") {
			t.Errorf("Detail = %q, want it to say opencode pane rounds record unknown", c.Detail)
		}
	})
	t.Run("sqlite3 not needed without opencode", func(t *testing.T) {
		env := &fakeEnv{lookPaths: map[string]string{}, existingFiles: map[string]bool{}}
		rep := Run(context.Background(), env, nil, WithUsage("/cfg/prices.json", false))
		if _, ok := findCheck(rep, "sqlite3", ""); ok {
			t.Error("no opencode candidate: no sqlite3 row")
		}
	})
	t.Run("prices absent is ok, stale is warn, malformed is warn", func(t *testing.T) {
		env := &fakeEnv{lookPaths: map[string]string{}, existingFiles: map[string]bool{}, fileContents: map[string]string{}}
		rep := Run(context.Background(), env, nil, WithUsage("/cfg/prices.json", false))
		if c, ok := findCheck(rep, "prices", "default"); !ok || c.Severity != SevOK {
			t.Errorf("absent prices.json: want an OK row naming the default: %+v", rep.Checks)
		}
		env.existingFiles["/cfg/prices.json"] = true
		env.fileContents["/cfg/prices.json"] = `{"as_of":"2020-01-01","models":{}}`
		rep = Run(context.Background(), env, nil, WithUsage("/cfg/prices.json", false))
		if c, ok := findCheck(rep, "prices", "2020-01-01"); !ok || c.Severity != SevWarn {
			t.Errorf("stale as_of: want a warn row: %+v", rep.Checks)
		}
		env.fileContents["/cfg/prices.json"] = `{"models": 5}`
		rep = Run(context.Background(), env, nil, WithUsage("/cfg/prices.json", false))
		if c, ok := findCheck(rep, "prices", "does not validate"); !ok || c.Severity != SevWarn {
			t.Errorf("malformed: want a warn row: %+v", rep.Checks)
		}
	})
}
```

The staleness rule: `as_of` older than 90 days from `time.Now()` warns.
If `Run` has an injectable clock use it; if not, `2020-01-01` is stale
under any clock this decade and the test does not need one.

- [ ] **Step 2: Run to verify they fail.**

Run: `go test ./internal/doctor/ -run TestUsageChecks`
Expected: build failure, `WithUsage` undefined.

- [ ] **Step 3: Add the option and the checks** in `internal/doctor/doctor.go`.

In `runConfig` add `usagePrices string` and `usageOpencode bool`, plus:

```go
// WithUsage enables the round-usage checks (#142): sqlite3 on PATH when an
// opencode candidate is configured (its pane rounds read opencode.db
// through it), and prices.json parsing and age.
func WithUsage(pricesPath string, opencodeConfigured bool) RunOption {
	return func(cfg *runConfig) {
		cfg.usagePrices = pricesPath
		cfg.usageOpencode = opencodeConfigured
	}
}
```

At the end of `Run`, before the report is assembled, when
`cfg.usagePrices != ""`:

```go
	if cfg.usagePrices != "" {
		checks = append(checks, usageChecks(env, cfg)...)
	}
```

and:

```go
// pricesMaxAge is how old prices.json's as_of may be before doctor warns.
const pricesMaxAge = 90 * 24 * time.Hour

func usageChecks(env Env, cfg runConfig) []Check {
	var out []Check
	if cfg.usageOpencode {
		if _, err := env.LookPath("sqlite3"); err != nil {
			out = append(out, Check{
				Name: "sqlite3", Severity: SevWarn,
				Detail: "not on PATH; opencode pane rounds record usage as unknown",
				Fix:    "install sqlite3 (the CLI), e.g. pacman -S sqlite / apt install sqlite3",
			})
		} else {
			out = append(out, Check{Name: "sqlite3", Severity: SevOK, Detail: "on PATH; opencode pane usage readable"})
		}
	}
	if err := env.Stat(cfg.usagePrices); err != nil {
		d := usage.DefaultPrices()
		out = append(out, Check{Name: "prices", Severity: SevOK,
			Detail: fmt.Sprintf("no %s; using the embedded default (as_of %s, %d models)", cfg.usagePrices, d.AsOf, len(d.Models))})
		return out
	}
	raw, err := env.ReadFile(cfg.usagePrices)
	if err != nil {
		out = append(out, Check{Name: "prices", Severity: SevWarn, Detail: cfg.usagePrices + ": " + err.Error(), ProbeFailed: true})
		return out
	}
	var p usage.Prices
	if err := json.Unmarshal(raw, &p); err != nil {
		out = append(out, Check{Name: "prices", Severity: SevWarn,
			Detail: fmt.Sprintf("%s does not validate: %v; rounds estimate from the embedded default", cfg.usagePrices, err),
			Fix:    "fix the JSON or delete the file"})
		return out
	}
	asOf, err := time.Parse("2006-01-02", p.AsOf)
	switch {
	case err != nil:
		out = append(out, Check{Name: "prices", Severity: SevWarn,
			Detail: fmt.Sprintf("%s: as_of %q is not YYYY-MM-DD", cfg.usagePrices, p.AsOf), Fix: "set as_of to the date the prices were checked"})
	case time.Since(asOf) > pricesMaxAge:
		out = append(out, Check{Name: "prices", Severity: SevWarn,
			Detail: fmt.Sprintf("%s: as_of %s is older than %d days; estimates may be stale", cfg.usagePrices, p.AsOf, int(pricesMaxAge.Hours()/24)),
			Fix:    "check the providers' pricing pages and update as_of"})
	default:
		out = append(out, Check{Name: "prices", Severity: SevOK, Detail: fmt.Sprintf("%s: as_of %s, %d models", cfg.usagePrices, p.AsOf, len(p.Models))})
	}
	return out
}
```

Check `env.ReadFile`'s exact signature in `internal/doctor/env.go` and
match it. Import `encoding/json`, `time`, `fmt` and
`"github.com/fuad-daoud/relay/internal/usage"` as needed.

- [ ] **Step 4: Run to verify they pass.**

Run: `go test -race -count=1 ./internal/doctor/`
Expected: PASS.

- [ ] **Step 5: Wire `cmd/relay`.**

Create `cmd/relay/exec.go`:

```go
package main

import (
	"context"
	"errors"
	"os/exec"
	"strings"
)

// binExec is usage.Exec over the real PATH. Stderr rides on the error so
// the reader can quote its first line.
type binExec struct{}

func (binExec) Run(ctx context.Context, bin string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, bin, args...)
	var stderr strings.Builder
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		if s := strings.TrimSpace(stderr.String()); s != "" {
			return nil, errors.New(s)
		}
		return nil, err
	}
	return out, nil
}
```

In `newRuntime` (`cmd/relay/main.go`), after `pol` is loaded:

```go
	prices, err := usage.LoadPrices(filepath.Join(configDir, "relay", "prices.json"))
	if err != nil {
		// A bad price file must never stop a round from closing: say so
		// once, on stderr, and run on the embedded default.
		fmt.Fprintf(os.Stderr, "relay: %v (using built-in prices)\n", err)
	}
	var sqlite usage.Exec
	if _, err := exec.LookPath("sqlite3"); err == nil {
		sqlite = binExec{}
	}
	home, _ := os.UserHomeDir()
	reader := usage.New(sqlite, home)
```

and in the returned `relay.Runtime{...}` literal add `Usage: reader,
Prices: prices,`. Import `os/exec` and
`"github.com/fuad-daoud/relay/internal/usage"`.

In `cmd/relay/doctor.go` at line ~210, add the option:

```go
	opencodeConfigured := false
	for _, k := range kinds {
		if k == "opencode" {
			opencodeConfigured = true
		}
	}
	pricesPath := filepath.Join(configDir, "relay", "prices.json") // configDir: the same userConfigRoot() result newRuntime uses; obtain it the way that function does
	rep := doctor.Run(context.Background(), env, kinds,
		doctor.WithDefinitions(assembleDefinitions(rt.Candidates, kinds)),
		doctor.WithUsage(pricesPath, opencodeConfigured))
```

Compose the path through `userConfigRoot()` -- never by hand (CLAUDE.md,
#42). The bind-time preflight at line ~356 does **not** get `WithUsage`:
a missing sqlite3 must not warn on every bind.

- [ ] **Step 6: Build and vet.**

Run: `go build ./... && go vet ./...`
Expected: clean. No test under `cmd/relay`.

- [ ] **Step 7: README.** Under `## Candidates`, after `### Availability`,
add:

````markdown
### Round usage

At every round close relay records what the round consumed on the
round's `report` entry in `log.jsonl` (and a consult's on its `findings`
entry): harness, provider, model, duration, tokens (`in`, `cache_read`,
`cache_write`, `out` -- `out` includes thinking), and a cost with its
provenance:

| `cost.basis` | meaning |
|---|---|
| `measured` | the harness reported dollars itself (claude's `total_cost_usd`, opencode's `cost`) |
| `estimated` | relay multiplied the harness's token counts by `~/.config/relay/prices.json` |
| `unknown` | no record, no price row, or no way to read; `note` says which |

`unknown` is an answer, not a failure. Where each figure comes from:

| harness | headless | pane |
|---|---|---|
| claude | the round's stream (`measured`) | `~/.claude/projects/<cwd>/` transcripts inside the round's window (`estimated`) |
| agy | the round's stream (`estimated`) | agy keeps no usage record (`unknown`) |
| opencode | the round's stream (`measured`) | `opencode.db` through `sqlite3` (`measured`); `relay doctor` says if `sqlite3` is missing |

A binding on `--cwd` shares the planner's directory, so its pane rounds
are `unknown` (`shared cwd`) rather than counting the planner's spend.

`prices.json` is `{"as_of": "YYYY-MM-DD", "source": "...", "models":
{"<provider>/<model>": {"in": …, "cache_read": …, "cache_write": …,
"out": …}}}` in USD per million tokens, overlaid on the table relay ships;
a model with no row is `unknown`, never `$0`. Mark a subscription lane
with `"plan": true` on its candidate: its rounds record `cost.plan` and
are shown as a quota draw, never as free.

Nothing prints these yet; they are in the log for `relay log`, `status
--json` and a `relay tab` summary to pick up (#142).
````

- [ ] **Step 8: Full check.**

```bash
test -z "$(gofmt -l .)" || gofmt -l .
go vet ./...
go test -race -count=1 ./...
cp go.mod /tmp/gm; cp go.sum /tmp/gs; go mod tidy; cmp go.mod /tmp/gm && cmp go.sum /tmp/gs
```

Expected: all clean; `go.mod`/`go.sum` unchanged (no new dependency).

- [ ] **Step 9: Commit.**

```bash
git add cmd/relay/ internal/doctor/ README.md
git commit -m "feat(doctor): usage checks; wire the usage reader and prices.json (#142)"
```

---

## Report

End your report with:

- the output of the four check commands (verbatim last lines);
- `git log --oneline main..HEAD` (seven commits expected);
- `git diff --stat main..HEAD`;
- whether `prices_default.json` was populated from pricing pages (with
  the URLs in its `source`) or shipped empty;
- anything you stopped on.

## After merge (not for the builder)

Real checks, by the human: one headless claude round, compare `usage` in
`log.jsonl` against the stream's `result` event; one opencode pane round,
compare against `sqlite3 -json ~/.local/share/opencode/opencode.db "<the
query from OpencodeQuery>"`. Then file slice 2 (surfaces) against #142.
