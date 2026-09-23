package relay

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/fuad-daoud/relay/internal/candidate"
	"github.com/fuad-daoud/relay/internal/harness"
	"github.com/fuad-daoud/relay/internal/latency"
	"github.com/fuad-daoud/relay/internal/store"
)

// probeBase is the instant every probe test starts its fake clock at.
var probeBase = time.Date(2026, 9, 23, 12, 0, 0, 0, time.UTC)

// fakeLine is one scripted stdout line at a fixed offset from the probe
// start. The fake moves the clock to that offset before delivering it, so
// ProbeCandidate's measurements are exact.
type fakeLine struct {
	at   time.Duration
	line string
}

// fakeExec is the test LineExec: it records argv and dir, feeds one script
// per call, and drives a clock the test installs as rt.Now. Call n is
// scripted by scripts[n]; total is the clock offset when Run returns.
type fakeExec struct {
	scripts [][]fakeLine
	total   time.Duration
	runErr  error

	calls int
	dirs  []string
	argvs [][]string
	now   *time.Time
}

func (f *fakeExec) Run(_ context.Context, dir string, argv []string, onLine func(line []byte)) error {
	f.dirs = append(f.dirs, dir)
	f.argvs = append(f.argvs, argv)

	var script []fakeLine
	if f.calls < len(f.scripts) {
		script = f.scripts[f.calls]
	}
	f.calls++

	for _, l := range script {
		*f.now = probeBase.Add(l.at)
		if onLine != nil {
			onLine([]byte(l.line))
		}
	}
	*f.now = probeBase.Add(f.total)

	return f.runErr
}

// probeRuntime builds a Runtime with a real store and a fake clock the
// caller advances by writing *now. LatencyPath lives under its own temp dir,
// so the recording path in Probe is exercised against a real file.
func probeRuntime(t *testing.T, setBody string) (Runtime, *time.Time) {
	t.Helper()
	now := probeBase
	rt := Runtime{
		Store:       store.New(t.TempDir()),
		Candidates:  candidateSet(t, setBody),
		LatencyPath: filepath.Join(t.TempDir(), "latency.json"),
		Now:         func() time.Time { return now },
	}
	return rt, &now
}

func TestProbeTierPerKind(t *testing.T) {
	cases := []struct {
		kind string
		want harness.Tier
	}{
		{"claude", harness.TierRead},
		{"agy", harness.TierRead},
		{"codex", harness.TierEdit},
		{"opencode", harness.TierHarness},
	}
	for _, tc := range cases {
		got, err := probeTier(tc.kind)
		if err != nil {
			t.Errorf("probeTier(%q) error = %v, want nil", tc.kind, err)
			continue
		}
		if got != tc.want {
			t.Errorf("probeTier(%q) = %q, want %q", tc.kind, got, tc.want)
		}
	}

	if _, err := probeTier("nope"); err == nil {
		t.Errorf("probeTier(%q) error = nil, want an error", "nope")
	}
}

func TestProbeCandidateMeasuresFirstOutput(t *testing.T) {
	set := candidateSet(t, testCandidatesJSON)
	c, err := set.Lookup(candidate.Ref{Harness: "opencode", Provider: "test", Model: "m"})
	if err != nil {
		t.Fatalf("lookup opencode candidate: %v", err)
	}

	rt, now := probeRuntime(t, testCandidatesJSON)
	fake := &fakeExec{
		now:   now,
		total: 900 * time.Millisecond,
		scripts: [][]fakeLine{{
			{at: 100 * time.Millisecond, line: `{"type":"step_start","part":{}}`},
			{at: 600 * time.Millisecond, line: `{"type":"text","part":{"text":"ok"}}`},
			{at: 800 * time.Millisecond, line: `{"type":"text","part":{"text":"more"}}`},
			{at: 850 * time.Millisecond, line: `{"type":"step_finish","part":{}}`},
		}},
	}

	got := ProbeCandidate(context.Background(), rt, fake, c, "box")

	if got.TTFTMS != 600 {
		t.Errorf("TTFTMS = %d, want 600", got.TTFTMS)
	}
	if got.TotalMS != 900 {
		t.Errorf("TotalMS = %d, want 900", got.TotalMS)
	}
	if got.Err != "" {
		t.Errorf("Err = %q, want empty", got.Err)
	}
	if got.Token != "opencode/test/m" || got.Host != "box" {
		t.Errorf("Token/Host = %q/%q, want opencode/test/m/box", got.Token, got.Host)
	}
	if !got.At.Equal(probeBase) {
		t.Errorf("At = %v, want %v", got.At, probeBase)
	}

	argv := strings.Join(fake.argvs[0], " ")
	if !strings.Contains(argv, "--agent") || !strings.Contains(argv, "plan-executor") {
		t.Errorf("argv = %q, want --agent plan-executor", argv)
	}
	if !strings.Contains(argv, probePrompt) {
		t.Errorf("argv = %q, want the probe prompt", argv)
	}
	if strings.Contains(argv, "--auto") {
		t.Errorf("argv = %q, must not carry opencode's yolo flag --auto", argv)
	}
	if fake.dirs[0] == "" {
		t.Errorf("argv ran with an empty dir")
	}
	if _, err := os.Stat(fake.dirs[0]); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("dir %s still exists after return (err=%v), want removed", fake.dirs[0], err)
	}
}

func TestProbeCandidateNoOutput(t *testing.T) {
	set := candidateSet(t, testCandidatesJSON)
	c, _ := set.Lookup(candidate.Ref{Harness: "opencode", Provider: "test", Model: "m"})

	rt, now := probeRuntime(t, testCandidatesJSON)
	fake := &fakeExec{
		now:     now,
		total:   300 * time.Millisecond,
		scripts: [][]fakeLine{{{at: 100 * time.Millisecond, line: `{"type":"step_start","part":{}}`}}},
	}

	got := ProbeCandidate(context.Background(), rt, fake, c, "box")

	if got.Err != "no model output" {
		t.Errorf("Err = %q, want %q", got.Err, "no model output")
	}
	if got.TTFTMS != 0 {
		t.Errorf("TTFTMS = %d, want 0", got.TTFTMS)
	}
}

func TestProbeCandidateRunErrorKeepsTTFT(t *testing.T) {
	set := candidateSet(t, testCandidatesJSON)
	c, _ := set.Lookup(candidate.Ref{Harness: "opencode", Provider: "test", Model: "m"})

	rt, now := probeRuntime(t, testCandidatesJSON)
	fake := &fakeExec{
		now:    now,
		total:  900 * time.Millisecond,
		runErr: errors.New("exit status 1: harness exploded"),
		scripts: [][]fakeLine{{
			{at: 200 * time.Millisecond, line: `{"type":"text","part":{"text":"ok"}}`},
		}},
	}

	got := ProbeCandidate(context.Background(), rt, fake, c, "box")

	if got.TTFTMS != 200 {
		t.Errorf("TTFTMS = %d, want 200", got.TTFTMS)
	}
	if !strings.Contains(got.Err, "harness exploded") {
		t.Errorf("Err = %q, want it to carry the run error", got.Err)
	}
}

func TestProbeCandidateNoKnownRole(t *testing.T) {
	rt, now := probeRuntime(t, testCandidatesJSON)
	fake := &fakeExec{now: now}

	c := candidate.Candidate{Harness: "opencode", Provider: "test", Model: "m", Roles: []string{"nope"}}
	got := ProbeCandidate(context.Background(), rt, fake, c, "box")

	if got.Err != "no known role" {
		t.Errorf("Err = %q, want %q", got.Err, "no known role")
	}
	if fake.calls != 0 {
		t.Errorf("fake was called %d times, want 0", fake.calls)
	}
}

func TestProbeUnknownTokenRunsNothing(t *testing.T) {
	rt, now := probeRuntime(t, testCandidatesJSON)
	fake := &fakeExec{now: now}

	if _, err := Probe(context.Background(), rt, fake, []string{"nope/test/m"}, "box", nil); err == nil {
		t.Fatalf("Probe(unknown token) error = nil, want an error")
	}
	if fake.calls != 0 {
		t.Errorf("fake was called %d times, want 0", fake.calls)
	}
}

func TestProbeRecordsHistory(t *testing.T) {
	const twoCandidates = `[
  {"harness":"opencode","provider":"test","model":"m","roles":["builder"]},
  {"harness":"claude","provider":"test","model":"m","roles":["builder"]}
]`
	rt, now := probeRuntime(t, twoCandidates)
	fake := &fakeExec{
		now:   now,
		total: 500 * time.Millisecond,
		scripts: [][]fakeLine{
			{{at: 100 * time.Millisecond, line: `{"type":"text","part":{"text":"a"}}`}},
			{{at: 200 * time.Millisecond, line: `{"type":"assistant","message":{}}`}},
		},
	}

	var seen []string
	results, err := Probe(context.Background(), rt, fake, nil, "box", func(r ProbeResult) {
		seen = append(seen, r.Token)
	})
	if err != nil {
		t.Fatalf("Probe() error = %v", err)
	}

	wantOrder := []string{"claude/test/m", "opencode/test/m"}
	if len(results) != 2 {
		t.Fatalf("Probe() returned %d results, want 2", len(results))
	}
	for i, want := range wantOrder {
		if results[i].Token != want {
			t.Errorf("results[%d].Token = %q, want %q", i, results[i].Token, want)
		}
	}
	if len(seen) != 2 || seen[0] != wantOrder[0] || seen[1] != wantOrder[1] {
		t.Errorf("each saw %v, want %v in order", seen, wantOrder)
	}

	h, err := latency.Load(rt.LatencyPath)
	if err != nil {
		t.Fatalf("latency.Load() error = %v", err)
	}
	if len(h.Samples) != 2 {
		t.Fatalf("recorded %d samples, want 2", len(h.Samples))
	}
	for i, want := range wantOrder {
		if h.Samples[i].Token != want {
			t.Errorf("samples[%d].Token = %q, want %q", i, h.Samples[i].Token, want)
		}
		if h.Samples[i].Host != "box" {
			t.Errorf("samples[%d].Host = %q, want box", i, h.Samples[i].Host)
		}
	}
}

func TestFormatProbe(t *testing.T) {
	success := ProbeResult{Token: "opencode/test/m", TTFTMS: 640, TotalMS: 1500}
	if got, want := FormatProbe(success, 15), "opencode/test/m  ttft 640ms  total 1.5s"; got != want {
		t.Errorf("FormatProbe(success) = %q, want %q", got, want)
	}

	failure := ProbeResult{Token: "opencode/test/m", TTFTMS: 200, TotalMS: 900, Err: "exit status 1"}
	if got, want := FormatProbe(failure, 15), "opencode/test/m  error: exit status 1  (ttft 200ms)"; got != want {
		t.Errorf("FormatProbe(failure) = %q, want %q", got, want)
	}
}
