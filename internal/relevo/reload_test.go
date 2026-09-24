package relevo

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/fuad-daoud/relevo/internal/candidate"
	"github.com/fuad-daoud/relevo/internal/classify"
	"github.com/fuad-daoud/relevo/internal/config"
	"github.com/fuad-daoud/relevo/internal/policy"
	"github.com/fuad-daoud/relevo/internal/roles"
)

// fakeSource is the ConfigSource test seam: it returns whatever version,
// loaded config and errors the test sets, and counts its calls.
type fakeSource struct {
	version   int64
	loaded    config.Loaded
	loadErr   error
	importErr error
	verErr    error

	importCalls   int
	loadCalls     int
	lastImportDir string
}

func (f *fakeSource) ImportFiles(dir string, _ time.Time) (config.ImportResult, error) {
	f.importCalls++
	f.lastImportDir = dir
	return config.ImportResult{}, f.importErr
}

func (f *fakeSource) Version() (int64, error) { return f.version, f.verErr }

func (f *fakeSource) Load() (config.Loaded, error) {
	f.loadCalls++
	if f.loadErr != nil {
		return config.Loaded{}, f.loadErr
	}
	return f.loaded, nil
}

// loaded builds a Loaded whose registry is the roles.Build derivation of its
// parts, the way config.Store.Load does.
func loaded(t *testing.T, pol policy.Policy, rf *roles.File, warnings []string) config.Loaded {
	t.Helper()
	set := &candidate.Set{}
	reg, err := roles.Build(rf, set, pol)
	if err != nil {
		t.Fatalf("roles.Build: %v", err)
	}
	return config.Loaded{
		Candidates: set,
		Policy:     pol,
		RolesFile:  rf,
		Registry:   reg,
		Warnings:   warnings,
	}
}

func TestRefreshLoadsOnFirstCall(t *testing.T) {
	pol := policy.Policy{Order: map[string][]string{"builder": {"agy"}}}
	src := &fakeSource{version: 1, loaded: loaded(t, pol, nil, nil)}
	w := NewConfigWatcher(src, "/cfg/relevo", nil)

	resolveCalls := 0
	cls := &classify.Fake{}
	w.resolve = func(cfg *policy.Classify, key string, getenv func(string) string) classify.Classifier {
		resolveCalls++
		return cls
	}

	out := w.Refresh(Runtime{})

	if src.importCalls != 1 {
		t.Errorf("ImportFiles calls = %d, want 1", src.importCalls)
	}
	if src.lastImportDir != "/cfg/relevo" {
		t.Errorf("ImportFiles dir = %q, want /cfg/relevo", src.lastImportDir)
	}
	if src.loadCalls != 1 {
		t.Errorf("Load calls = %d, want 1", src.loadCalls)
	}
	if resolveCalls != 1 {
		t.Errorf("resolve calls = %d, want 1", resolveCalls)
	}
	if out.Candidates != src.loaded.Candidates {
		t.Errorf("rt.Candidates was not replaced")
	}
	if len(out.Policy.Order["builder"]) != 1 || out.Policy.Order["builder"][0] != "agy" {
		t.Errorf("rt.Policy was not replaced")
	}
	if out.Classify != cls {
		t.Errorf("rt.Classify was not replaced from resolve seam")
	}
}

func TestRefreshSkipsWhenVersionUnchanged(t *testing.T) {
	src := &fakeSource{version: 1, loaded: loaded(t, policy.Policy{}, nil, nil)}
	w := NewConfigWatcher(src, "/cfg/relevo", nil)

	resolveCalls := 0
	w.resolve = func(cfg *policy.Classify, key string, getenv func(string) string) classify.Classifier {
		resolveCalls++
		return &classify.Fake{}
	}

	out1 := w.Refresh(Runtime{})
	if src.loadCalls != 1 || resolveCalls != 1 {
		t.Fatalf("first call failed to load")
	}

	out2 := w.Refresh(out1)
	if src.loadCalls != 1 || resolveCalls != 1 {
		t.Errorf("second call loaded despite an unchanged version; loadCalls=%d resolveCalls=%d", src.loadCalls, resolveCalls)
	}
	if out2.Candidates != out1.Candidates {
		t.Errorf("Candidates pointer not equal")
	}
	// The import runs on every tick: a file dropped in is picked up even when
	// the version has not changed yet.
	if src.importCalls != 2 {
		t.Errorf("ImportFiles calls = %d, want 2", src.importCalls)
	}
}

func TestRefreshReloadsOnVersionChange(t *testing.T) {
	good := loaded(t, policy.Policy{Order: map[string][]string{"builder": {"good"}}}, nil, nil)
	src := &fakeSource{version: 1, loaded: good}
	w := NewConfigWatcher(src, "/cfg/relevo", nil)

	resolveCalls := 0
	w.resolve = func(cfg *policy.Classify, key string, getenv func(string) string) classify.Classifier {
		resolveCalls++
		return &classify.Fake{}
	}

	out1 := w.Refresh(Runtime{})
	if src.loadCalls != 1 || resolveCalls != 1 {
		t.Fatalf("first load failed")
	}

	// A new config version and a new policy.
	src.version = 2
	src.loaded = loaded(t, policy.Policy{Order: map[string][]string{"builder": {"claude"}}}, nil, nil)

	out2 := w.Refresh(out1)
	if src.loadCalls != 2 {
		t.Errorf("Load calls = %d, want 2", src.loadCalls)
	}
	if resolveCalls != 2 {
		t.Errorf("resolve calls = %d, want 2", resolveCalls)
	}
	if len(out2.Policy.Order["builder"]) != 1 || out2.Policy.Order["builder"][0] != "claude" {
		t.Errorf("new Policy.Order not visible, got %v", out2.Policy.Order["builder"])
	}
}

func TestRefreshKeepsLastGoodOnBadLoad(t *testing.T) {
	good := loaded(t, policy.Policy{Order: map[string][]string{"builder": {"good"}}}, nil, nil)
	src := &fakeSource{version: 1, loaded: good}
	w := NewConfigWatcher(src, "/cfg/relevo", nil)

	goodCls := &classify.Fake{}
	w.resolve = func(cfg *policy.Classify, key string, getenv func(string) string) classify.Classifier {
		return goodCls
	}
	var warnMsgs []string
	w.warn = func(msg string, args ...any) { warnMsgs = append(warnMsgs, msg) }

	t0 := time.Date(2026, 9, 19, 12, 0, 0, 0, time.UTC)
	rt := Runtime{Now: func() time.Time { return t0 }}
	out1 := w.Refresh(rt)
	if src.loadCalls != 1 {
		t.Fatalf("first load failed")
	}

	// A new version whose load fails.
	src.version = 2
	src.loadErr = policy.ErrBadPolicy
	out2 := w.Refresh(out1)

	if out2.Candidates != src.loaded.Candidates {
		t.Errorf("Candidates changed on a bad load")
	}
	if len(out2.Policy.Order["builder"]) != 1 || out2.Policy.Order["builder"][0] != "good" {
		t.Errorf("Policy changed on a bad load")
	}
	if out2.Classify != goodCls {
		t.Errorf("Classify changed on a bad load")
	}
	if w.version != 1 {
		t.Errorf("version advanced to %d despite the error, want it to stay at 1", w.version)
	}

	if len(warnMsgs) != 1 {
		t.Fatalf("warn called %d times, want 1", len(warnMsgs))
	}
	if !strings.Contains(warnMsgs[0], "keeping the copy loaded at") {
		t.Errorf("warn text %q does not contain %q", warnMsgs[0], "keeping the copy loaded at")
	}

	// A third call with the same bad version tries the load again.
	_ = w.Refresh(out2)
	if src.loadCalls != 3 {
		t.Errorf("Load calls = %d, want 3", src.loadCalls)
	}
	if len(warnMsgs) != 1 {
		t.Errorf("warn called %d times, want 1", len(warnMsgs))
	}
}

func TestRefreshKeepsLastGoodOnImportError(t *testing.T) {
	good := loaded(t, policy.Policy{Order: map[string][]string{"builder": {"good"}}}, nil, nil)
	src := &fakeSource{version: 1, loaded: good}
	w := NewConfigWatcher(src, "/cfg/relevo", nil)
	w.resolve = func(cfg *policy.Classify, key string, getenv func(string) string) classify.Classifier {
		return &classify.Fake{}
	}
	var warnMsgs []string
	w.warn = func(msg string, args ...any) { warnMsgs = append(warnMsgs, msg) }

	out1 := w.Refresh(Runtime{})
	if out1.Candidates != good.Candidates {
		t.Fatalf("first load did not replace Candidates")
	}

	// The next tick's import fails: the old copy is kept and warned about.
	src.importErr = errors.New("policy.json: bad policy")
	out2 := w.Refresh(out1)
	if out2.Candidates != good.Candidates {
		t.Errorf("Candidates changed on an import error")
	}
	if len(warnMsgs) != 1 {
		t.Fatalf("warn called %d times, want 1", len(warnMsgs))
	}
	if !strings.Contains(warnMsgs[0], "keeping the copy loaded at") {
		t.Errorf("warn text %q does not contain %q", warnMsgs[0], "keeping the copy loaded at")
	}
	if src.loadCalls != 1 {
		t.Errorf("Load calls = %d, want 1: an import error must not load", src.loadCalls)
	}
}

func TestRefreshWarnsOncePerDistinctError(t *testing.T) {
	src := &fakeSource{version: 1}
	w := NewConfigWatcher(src, "/cfg/relevo", nil)
	w.resolve = func(cfg *policy.Classify, key string, getenv func(string) string) classify.Classifier {
		return &classify.Fake{}
	}

	var warnMsgs []string
	w.warn = func(msg string, args ...any) { warnMsgs = append(warnMsgs, msg) }

	rt := Runtime{}

	// Three ticks with the same error -> one warn.
	src.loadErr = errors.New("error one")
	w.Refresh(rt)
	w.Refresh(rt)
	w.Refresh(rt)
	if len(warnMsgs) != 1 {
		t.Fatalf("warn calls = %d, want 1 after 3 identical errors", len(warnMsgs))
	}

	// A different error text -> a second warn.
	src.loadErr = errors.New("error two")
	w.Refresh(rt)
	if len(warnMsgs) != 2 {
		t.Fatalf("warn calls = %d, want 2 after the error changed", len(warnMsgs))
	}

	// A good load at a new version.
	src.loadErr = nil
	src.version = 2
	src.loaded = loaded(t, policy.Policy{}, nil, nil)
	w.Refresh(rt)
	if len(warnMsgs) != 2 {
		t.Fatalf("warn calls = %d, want 2 after a successful load", len(warnMsgs))
	}

	// The same error again -> a third warn.
	src.version = 3
	src.loadErr = errors.New("error two")
	w.Refresh(rt)
	if len(warnMsgs) != 3 {
		t.Fatalf("warn calls = %d, want 3 after a repeated error following a good load", len(warnMsgs))
	}
}

// TestRefreshEmptyConfigIsNotAnError pins the "absent section = missing file"
// invariant: a Loaded with every section empty replaces the runtime quietly.
func TestRefreshEmptyConfigIsNotAnError(t *testing.T) {
	src := &fakeSource{version: 1, loaded: loaded(t, policy.Policy{}, nil, nil)}
	w := NewConfigWatcher(src, "/cfg/relevo", nil)
	w.resolve = func(cfg *policy.Classify, key string, getenv func(string) string) classify.Classifier {
		return &classify.Fake{}
	}
	warnCalls := 0
	w.warn = func(msg string, args ...any) { warnCalls++ }

	out := w.Refresh(Runtime{})
	if src.loadCalls != 1 {
		t.Errorf("Load calls = %d, want 1", src.loadCalls)
	}
	if warnCalls != 0 {
		t.Errorf("warn calls = %d, want 0", warnCalls)
	}
	if out.Candidates == nil {
		t.Errorf("Candidates = nil, want the empty set")
	}
}

func TestRefreshStartupFailureSaysStartup(t *testing.T) {
	src := &fakeSource{version: 1, loadErr: errors.New("parse error")}
	w := NewConfigWatcher(src, "/cfg/relevo", nil)

	var warnMsgs []string
	w.warn = func(msg string, args ...any) { warnMsgs = append(warnMsgs, msg) }

	_ = w.Refresh(Runtime{})

	if len(warnMsgs) != 1 {
		t.Fatalf("warn calls = %d, want 1", len(warnMsgs))
	}
	if !strings.Contains(warnMsgs[0], "startup") {
		t.Errorf("warn text %q does not contain 'startup'", warnMsgs[0])
	}
}

// TestRefreshCarriesAndLogsConfigWarnings pins #372 §4.4: the watcher carries
// the config warnings on the Runtime, logs each distinct text once, and does
// not repeat an unchanged warning on a later reload.
func TestRefreshCarriesAndLogsConfigWarnings(t *testing.T) {
	src := &fakeSource{
		version: 1,
		loaded: loaded(t, policy.Policy{}, nil,
			[]string{"candidates.json: cand warning", "policy.json: policy warning"}),
	}
	w := NewConfigWatcher(src, "/cfg/relevo", nil)
	w.resolve = func(cfg *policy.Classify, key string, getenv func(string) string) classify.Classifier {
		return &classify.Fake{}
	}

	var warns []string
	w.warn = func(msg string, args ...any) { warns = append(warns, msg) }

	out := w.Refresh(Runtime{})
	want := []string{"candidates.json: cand warning", "policy.json: policy warning"}
	if len(out.ConfigWarnings) != 2 || out.ConfigWarnings[0] != want[0] || out.ConfigWarnings[1] != want[1] {
		t.Fatalf("ConfigWarnings = %v, want %v", out.ConfigWarnings, want)
	}
	if len(warns) != 2 {
		t.Fatalf("warn calls = %v, want both warnings logged", warns)
	}

	// A reload that produces the same warnings must not log them again.
	src.version = 2
	out2 := w.Refresh(out)
	if len(out2.ConfigWarnings) != 2 {
		t.Fatalf("ConfigWarnings after reload = %v, want 2", out2.ConfigWarnings)
	}
	if len(warns) != 2 {
		t.Errorf("warn calls after an unchanged reload = %v, want still 2", warns)
	}
}

// TestRefreshReloadsRegistry pins §5.1: a new version's roles section replaces
// the runtime's registry.
func TestRefreshReloadsRegistry(t *testing.T) {
	first := &roles.File{Rows: map[string]roles.Row{
		"builder": {Candidates: []string{"claude/test/m"}},
	}}
	src := &fakeSource{version: 1, loaded: loaded(t, policy.Policy{}, first, nil)}
	w := NewConfigWatcher(src, "/cfg/relevo", nil)
	w.resolve = func(cfg *policy.Classify, key string, getenv func(string) string) classify.Classifier {
		return &classify.Fake{}
	}

	out1 := w.Refresh(Runtime{})
	if out1.Registry == nil || out1.Registry.Source() != roles.SourceFile {
		t.Fatalf("Registry = %+v, want a roles.json registry", out1.Registry)
	}

	second := &roles.File{Rows: map[string]roles.Row{
		"builder": {Candidates: []string{"agy/test/m"}},
	}}
	src.version = 2
	src.loaded = loaded(t, policy.Policy{}, second, nil)

	out2 := w.Refresh(out1)
	if out2.Registry == out1.Registry {
		t.Error("Registry was not replaced after a roles change")
	}
	builder, ok := out2.Registry.Role("builder")
	if !ok {
		t.Fatal(`Role("builder") not found`)
	}
	if len(builder.Candidates) != 1 || builder.Candidates[0] != "agy/test/m" {
		t.Errorf("builder.Candidates = %v, want the reloaded row", builder.Candidates)
	}
}

// TestRefreshLoadsLegacyRegistryWhenNoRolesSection pins §5.1: a Loaded with no
// roles file still has a registry, and it is the legacy derivation.
func TestRefreshLoadsLegacyRegistryWhenNoRolesSection(t *testing.T) {
	src := &fakeSource{version: 1, loaded: loaded(t, policy.Policy{}, nil, nil)}
	w := NewConfigWatcher(src, "/cfg/relevo", nil)
	w.resolve = func(cfg *policy.Classify, key string, getenv func(string) string) classify.Classifier {
		return &classify.Fake{}
	}

	out := w.Refresh(Runtime{})
	if out.Registry == nil {
		t.Fatal("Registry = nil, want the legacy derivation")
	}
	if out.Registry.Source() != roles.SourceLegacy {
		t.Errorf("Source() = %q, want %q", out.Registry.Source(), roles.SourceLegacy)
	}
}
