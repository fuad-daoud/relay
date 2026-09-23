package relevo

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/fuad-daoud/relevo/internal/candidate"
	"github.com/fuad-daoud/relevo/internal/classify"
	"github.com/fuad-daoud/relevo/internal/policy"
	"github.com/fuad-daoud/relevo/internal/roles"
)

func TestRefreshLoadsOnFirstCall(t *testing.T) {
	paths := ConfigPaths{
		Candidates: "/cand.json",
		Policy:     "/pol.json",
		ConfigDir:  "/cfg",
	}
	w := NewConfigWatcher(paths)

	candCalls := 0
	polCalls := 0
	resolveCalls := 0

	t0 := time.Date(2026, 9, 19, 12, 0, 0, 0, time.UTC)
	w.stat = func(path string) (fileStamp, error) {
		return fileStamp{mtime: t0, size: 100, exists: true}, nil
	}
	cands := &candidate.Set{}
	w.loadCandidates = func(path string) (*candidate.Set, []string, error) {
		candCalls++
		if path != paths.Candidates {
			t.Errorf("loadCandidates path = %s, want %s", path, paths.Candidates)
		}
		return cands, nil, nil
	}
	pol := policy.Policy{Order: map[string][]string{"builder": {"agy"}}}
	w.loadPolicy = func(path string) (policy.Policy, []string, error) {
		polCalls++
		if path != paths.Policy {
			t.Errorf("loadPolicy path = %s, want %s", path, paths.Policy)
		}
		return pol, nil, nil
	}
	cls := &classify.Fake{}
	w.resolve = func(cfg *policy.Classify, configDir string, getenv func(string) string) classify.Classifier {
		resolveCalls++
		return cls
	}

	rt := Runtime{}
	out := w.Refresh(rt)

	if candCalls != 1 {
		t.Errorf("loadCandidates calls = %d, want 1", candCalls)
	}
	if polCalls != 1 {
		t.Errorf("loadPolicy calls = %d, want 1", polCalls)
	}
	if resolveCalls != 1 {
		t.Errorf("resolve calls = %d, want 1", resolveCalls)
	}
	if out.Candidates != cands {
		t.Errorf("rt.Candidates was not replaced")
	}
	if len(out.Policy.Order["builder"]) != 1 || out.Policy.Order["builder"][0] != "agy" {
		t.Errorf("rt.Policy was not replaced")
	}
	if out.Classify != cls {
		t.Errorf("rt.Classify was not replaced from resolve seam")
	}
}

func TestRefreshSkipsWhenUnchanged(t *testing.T) {
	paths := ConfigPaths{
		Candidates: "/cand.json",
		Policy:     "/pol.json",
		ConfigDir:  "/cfg",
	}
	w := NewConfigWatcher(paths)

	candCalls := 0
	polCalls := 0
	resolveCalls := 0

	t0 := time.Date(2026, 9, 19, 12, 0, 0, 0, time.UTC)
	w.stat = func(path string) (fileStamp, error) {
		return fileStamp{mtime: t0, size: 100, exists: true}, nil
	}
	cands := &candidate.Set{}
	w.loadCandidates = func(path string) (*candidate.Set, []string, error) {
		candCalls++
		return cands, nil, nil
	}
	w.loadPolicy = func(path string) (policy.Policy, []string, error) {
		polCalls++
		return policy.Policy{}, nil, nil
	}
	w.resolve = func(cfg *policy.Classify, configDir string, getenv func(string) string) classify.Classifier {
		resolveCalls++
		return &classify.Fake{}
	}

	rt := Runtime{}
	out1 := w.Refresh(rt)
	if candCalls != 1 || polCalls != 1 || resolveCalls != 1 {
		t.Fatalf("first call failed to load")
	}

	out2 := w.Refresh(out1)
	if candCalls != 1 || polCalls != 1 || resolveCalls != 1 {
		t.Errorf("second call loaded despite unchanged stamps; candCalls=%d polCalls=%d resolveCalls=%d", candCalls, polCalls, resolveCalls)
	}
	if out2.Candidates != out1.Candidates {
		t.Errorf("Candidates pointer not equal")
	}
}

func TestRefreshReloadsOnPolicyChange(t *testing.T) {
	paths := ConfigPaths{
		Candidates: "/cand.json",
		Policy:     "/pol.json",
		ConfigDir:  "/cfg",
	}
	w := NewConfigWatcher(paths)

	candCalls := 0
	polCalls := 0
	resolveCalls := 0

	t0 := time.Date(2026, 9, 19, 12, 0, 0, 0, time.UTC)
	polStamp := fileStamp{mtime: t0, size: 100, exists: true}
	candStamp := fileStamp{mtime: t0, size: 50, exists: true}

	w.stat = func(path string) (fileStamp, error) {
		if path == paths.Policy {
			return polStamp, nil
		}
		return candStamp, nil
	}
	w.loadCandidates = func(path string) (*candidate.Set, []string, error) {
		candCalls++
		return &candidate.Set{}, nil, nil
	}
	curOrder := []string{"agy"}
	w.loadPolicy = func(path string) (policy.Policy, []string, error) {
		polCalls++
		return policy.Policy{Order: map[string][]string{"builder": curOrder}}, nil, nil
	}
	w.resolve = func(cfg *policy.Classify, configDir string, getenv func(string) string) classify.Classifier {
		resolveCalls++
		return &classify.Fake{}
	}

	rt := Runtime{}
	out1 := w.Refresh(rt)
	if candCalls != 1 || polCalls != 1 || resolveCalls != 1 {
		t.Fatalf("first load failed")
	}

	// Change only policy stamp
	polStamp.mtime = polStamp.mtime.Add(time.Second)
	curOrder = []string{"claude"}

	out2 := w.Refresh(out1)
	if candCalls != 2 {
		t.Errorf("loadCandidates calls = %d, want 2", candCalls)
	}
	if polCalls != 2 {
		t.Errorf("loadPolicy calls = %d, want 2", polCalls)
	}
	if resolveCalls != 2 {
		t.Errorf("resolve calls = %d, want 2", resolveCalls)
	}
	if len(out2.Policy.Order["builder"]) != 1 || out2.Policy.Order["builder"][0] != "claude" {
		t.Errorf("new Policy.Order not visible, got %v", out2.Policy.Order["builder"])
	}
}

func TestRefreshReloadsOnCandidatesChange(t *testing.T) {
	paths := ConfigPaths{
		Candidates: "/cand.json",
		Policy:     "/pol.json",
		ConfigDir:  "/cfg",
	}
	w := NewConfigWatcher(paths)

	candCalls := 0
	polCalls := 0
	resolveCalls := 0

	t0 := time.Date(2026, 9, 19, 12, 0, 0, 0, time.UTC)
	candStamp := fileStamp{mtime: t0, size: 50, exists: true}
	polStamp := fileStamp{mtime: t0, size: 100, exists: true}

	w.stat = func(path string) (fileStamp, error) {
		if path == paths.Candidates {
			return candStamp, nil
		}
		return polStamp, nil
	}
	w.loadCandidates = func(path string) (*candidate.Set, []string, error) {
		candCalls++
		return &candidate.Set{}, nil, nil
	}
	w.loadPolicy = func(path string) (policy.Policy, []string, error) {
		polCalls++
		return policy.Policy{}, nil, nil
	}
	w.resolve = func(cfg *policy.Classify, configDir string, getenv func(string) string) classify.Classifier {
		resolveCalls++
		return &classify.Fake{}
	}

	rt := Runtime{}
	out1 := w.Refresh(rt)
	if candCalls != 1 || polCalls != 1 || resolveCalls != 1 {
		t.Fatalf("first load failed")
	}

	// Change only candidates stamp
	candStamp.size += 10

	_ = w.Refresh(out1)
	if candCalls != 2 {
		t.Errorf("loadCandidates calls = %d, want 2", candCalls)
	}
	if polCalls != 2 {
		t.Errorf("loadPolicy calls = %d, want 2", polCalls)
	}
	if resolveCalls != 2 {
		t.Errorf("resolve calls = %d, want 2", resolveCalls)
	}
}

func TestRefreshKeepsLastGoodOnBadPolicy(t *testing.T) {
	paths := ConfigPaths{
		Candidates: "/cand.json",
		Policy:     "/pol.json",
		ConfigDir:  "/cfg",
	}
	w := NewConfigWatcher(paths)

	t0 := time.Date(2026, 9, 19, 12, 0, 0, 0, time.UTC)
	candStamp := fileStamp{mtime: t0, size: 50, exists: true}
	polStamp := fileStamp{mtime: t0, size: 100, exists: true}

	w.stat = func(path string) (fileStamp, error) {
		if path == paths.Candidates {
			return candStamp, nil
		}
		return polStamp, nil
	}

	goodCands := &candidate.Set{}
	w.loadCandidates = func(path string) (*candidate.Set, []string, error) {
		return goodCands, nil, nil
	}

	goodPolicy := policy.Policy{Order: map[string][]string{"builder": {"good"}}}
	polErr := error(nil)
	polCalls := 0
	w.loadPolicy = func(path string) (policy.Policy, []string, error) {
		polCalls++
		if polErr != nil {
			return policy.Policy{}, nil, polErr
		}
		return goodPolicy, nil, nil
	}

	goodCls := &classify.Fake{}
	w.resolve = func(cfg *policy.Classify, configDir string, getenv func(string) string) classify.Classifier {
		return goodCls
	}

	var warnMsgs []string
	w.warn = func(msg string, args ...any) {
		warnMsgs = append(warnMsgs, msg)
	}

	rt := Runtime{Now: func() time.Time { return t0 }}
	out1 := w.Refresh(rt)
	if polCalls != 1 {
		t.Fatalf("first load failed")
	}

	// Change policy stamp, and policy load fails with ErrBadPolicy
	polStamp.mtime = polStamp.mtime.Add(time.Second)
	polErr = policy.ErrBadPolicy

	out2 := w.Refresh(out1)

	// rt unchanged (same Policy, same Candidates, same Classify)
	if out2.Candidates != goodCands {
		t.Errorf("Candidates changed on bad load")
	}
	if len(out2.Policy.Order["builder"]) != 1 || out2.Policy.Order["builder"][0] != "good" {
		t.Errorf("Policy changed on bad load")
	}
	if out2.Classify != goodCls {
		t.Errorf("Classify changed on bad load")
	}

	// Stamps NOT advanced: w.pol should still be the old stamp
	if w.pol == polStamp {
		t.Errorf("stamps were advanced despite error")
	}

	if len(warnMsgs) != 1 {
		t.Fatalf("warn called %d times, want 1", len(warnMsgs))
	}
	if !strings.Contains(warnMsgs[0], "keeping the copy loaded at") {
		t.Errorf("warn text %q does not contain %q", warnMsgs[0], "keeping the copy loaded at")
	}

	// A third call with the same bad stamp tries the load again
	_ = w.Refresh(out2)
	if polCalls != 3 {
		t.Errorf("loadPolicy calls = %d, want 3", polCalls)
	}
	// Still 1 warning because error didn't change
	if len(warnMsgs) != 1 {
		t.Errorf("warn called %d times, want 1", len(warnMsgs))
	}
}

func TestRefreshWarnsOncePerDistinctError(t *testing.T) {
	paths := ConfigPaths{
		Candidates: "/cand.json",
		Policy:     "/pol.json",
		ConfigDir:  "/cfg",
	}
	w := NewConfigWatcher(paths)

	t0 := time.Date(2026, 9, 19, 12, 0, 0, 0, time.UTC)
	candStamp := fileStamp{mtime: t0, size: 50, exists: true}
	polStamp := fileStamp{mtime: t0, size: 100, exists: true}

	w.stat = func(path string) (fileStamp, error) {
		if path == paths.Candidates {
			return candStamp, nil
		}
		return polStamp, nil
	}
	w.loadCandidates = func(path string) (*candidate.Set, []string, error) {
		return &candidate.Set{}, nil, nil
	}

	var polErr error = errors.New("error one")
	w.loadPolicy = func(path string) (policy.Policy, []string, error) {
		if polErr != nil {
			return policy.Policy{}, nil, polErr
		}
		return policy.Policy{}, nil, nil
	}
	w.resolve = func(cfg *policy.Classify, configDir string, getenv func(string) string) classify.Classifier {
		return &classify.Fake{}
	}

	var warnMsgs []string
	w.warn = func(msg string, args ...any) {
		warnMsgs = append(warnMsgs, msg)
	}

	rt := Runtime{}

	// Three ticks with same error -> one warn
	w.Refresh(rt)
	w.Refresh(rt)
	w.Refresh(rt)
	if len(warnMsgs) != 1 {
		t.Fatalf("warn calls = %d, want 1 after 3 identical errors", len(warnMsgs))
	}

	// Error text changes -> second warn
	polErr = errors.New("error two")
	w.Refresh(rt)
	if len(warnMsgs) != 2 {
		t.Fatalf("warn calls = %d, want 2 after error change", len(warnMsgs))
	}

	// A good load
	polErr = nil
	polStamp.mtime = polStamp.mtime.Add(time.Second)
	w.Refresh(rt)
	if len(warnMsgs) != 2 {
		t.Fatalf("warn calls = %d, want 2 after successful load", len(warnMsgs))
	}

	// The same error again -> a third warn
	polStamp.mtime = polStamp.mtime.Add(time.Second)
	polErr = errors.New("error two")
	w.Refresh(rt)
	if len(warnMsgs) != 3 {
		t.Fatalf("warn calls = %d, want 3 after repeated error following good load", len(warnMsgs))
	}
}

func TestRefreshMissingFileIsEmptyNotError(t *testing.T) {
	paths := ConfigPaths{
		Candidates: "/cand.json",
		Policy:     "/pol.json",
		ConfigDir:  "/cfg",
	}
	w := NewConfigWatcher(paths)

	t0 := time.Date(2026, 9, 19, 12, 0, 0, 0, time.UTC)
	candStamp := fileStamp{mtime: t0, size: 50, exists: true}
	polStamp := fileStamp{exists: false} // missing file

	w.stat = func(path string) (fileStamp, error) {
		if path == paths.Policy {
			return polStamp, nil
		}
		return candStamp, nil
	}
	w.loadCandidates = func(path string) (*candidate.Set, []string, error) {
		return &candidate.Set{}, nil, nil
	}
	polCalls := 0
	w.loadPolicy = func(path string) (policy.Policy, []string, error) {
		polCalls++
		return policy.Policy{}, nil, nil
	}
	w.resolve = func(cfg *policy.Classify, configDir string, getenv func(string) string) classify.Classifier {
		return &classify.Fake{}
	}
	warnCalls := 0
	w.warn = func(msg string, args ...any) {
		warnCalls++
	}

	rt := Runtime{}
	_ = w.Refresh(rt)

	if polCalls != 1 {
		t.Errorf("loadPolicy calls = %d, want 1", polCalls)
	}
	if warnCalls != 0 {
		t.Errorf("warn calls = %d, want 0", warnCalls)
	}
}

func TestRefreshStartupFailureSaysStartup(t *testing.T) {
	paths := ConfigPaths{
		Candidates: "/cand.json",
		Policy:     "/pol.json",
		ConfigDir:  "/cfg",
	}
	w := NewConfigWatcher(paths)

	t0 := time.Date(2026, 9, 19, 12, 0, 0, 0, time.UTC)
	w.stat = func(path string) (fileStamp, error) {
		return fileStamp{mtime: t0, size: 100, exists: true}, nil
	}
	w.loadCandidates = func(path string) (*candidate.Set, []string, error) {
		return &candidate.Set{}, nil, nil
	}
	w.loadPolicy = func(path string) (policy.Policy, []string, error) {
		return policy.Policy{}, nil, errors.New("parse error")
	}

	var warnMsgs []string
	w.warn = func(msg string, args ...any) {
		warnMsgs = append(warnMsgs, msg)
	}

	rt := Runtime{}
	_ = w.Refresh(rt)

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
	paths := ConfigPaths{Candidates: "/cand.json", Policy: "/pol.json", ConfigDir: "/cfg"}
	w := NewConfigWatcher(paths)

	t0 := time.Date(2026, 9, 19, 12, 0, 0, 0, time.UTC)
	polStamp := fileStamp{mtime: t0, size: 1, exists: true}
	candStamp := fileStamp{mtime: t0, size: 1, exists: true}
	w.stat = func(path string) (fileStamp, error) {
		if path == paths.Policy {
			return polStamp, nil
		}
		return candStamp, nil
	}
	w.loadCandidates = func(path string) (*candidate.Set, []string, error) {
		return &candidate.Set{}, []string{"candidates.json: cand warning"}, nil
	}
	w.loadPolicy = func(path string) (policy.Policy, []string, error) {
		return policy.Policy{}, []string{"policy.json: policy warning"}, nil
	}
	w.resolve = func(cfg *policy.Classify, configDir string, getenv func(string) string) classify.Classifier {
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
	polStamp.mtime = polStamp.mtime.Add(time.Second)
	out2 := w.Refresh(out)
	if len(out2.ConfigWarnings) != 2 {
		t.Fatalf("ConfigWarnings after reload = %v, want 2", out2.ConfigWarnings)
	}
	if len(warns) != 2 {
		t.Errorf("warn calls after an unchanged reload = %v, want still 2", warns)
	}
}

// TestRefreshReloadsOnRolesChange pins §5.1: a changed roles.json stamp alone
// reloads the file and replaces the runtime's registry.
func TestRefreshReloadsOnRolesChange(t *testing.T) {
	paths := ConfigPaths{
		Candidates: "/cand.json",
		Policy:     "/pol.json",
		Roles:      "/roles.json",
		ConfigDir:  "/cfg",
	}
	w := NewConfigWatcher(paths)

	t0 := time.Date(2026, 9, 19, 12, 0, 0, 0, time.UTC)
	candStamp := fileStamp{mtime: t0, size: 50, exists: true}
	polStamp := fileStamp{mtime: t0, size: 100, exists: true}
	roleStamp := fileStamp{mtime: t0, size: 10, exists: true}
	w.stat = func(path string) (fileStamp, error) {
		switch path {
		case paths.Candidates:
			return candStamp, nil
		case paths.Policy:
			return polStamp, nil
		case paths.Roles:
			return roleStamp, nil
		}
		return fileStamp{}, errors.New("unexpected path " + path)
	}
	w.loadCandidates = func(path string) (*candidate.Set, []string, error) {
		return &candidate.Set{}, nil, nil
	}
	w.loadPolicy = func(path string) (policy.Policy, []string, error) {
		return policy.Policy{}, nil, nil
	}
	roleCalls := 0
	w.loadRoles = func(path string) (*roles.File, []string, error) {
		roleCalls++
		return &roles.File{Rows: map[string]roles.Row{
			"builder": {Candidates: []string{"claude/test/m"}},
		}}, nil, nil
	}
	w.resolve = func(cfg *policy.Classify, configDir string, getenv func(string) string) classify.Classifier {
		return &classify.Fake{}
	}

	out1 := w.Refresh(Runtime{})
	if roleCalls != 1 {
		t.Fatalf("loadRoles calls = %d, want 1", roleCalls)
	}
	if out1.Registry == nil || out1.Registry.Source() != roles.SourceFile {
		t.Fatalf("Registry = %+v, want a roles.json registry", out1.Registry)
	}

	// Change only the roles stamp.
	roleStamp.mtime = roleStamp.mtime.Add(time.Second)
	w.loadRoles = func(path string) (*roles.File, []string, error) {
		roleCalls++
		return &roles.File{Rows: map[string]roles.Row{
			"builder": {Candidates: []string{"agy/test/m"}},
		}}, nil, nil
	}

	out2 := w.Refresh(out1)
	if roleCalls != 2 {
		t.Errorf("loadRoles calls = %d, want 2", roleCalls)
	}
	if out2.Registry == out1.Registry {
		t.Error("Registry was not replaced after a roles.json change")
	}
	builder, ok := out2.Registry.Role("builder")
	if !ok {
		t.Fatal("Role(\"builder\") not found")
	}
	if len(builder.Candidates) != 1 || builder.Candidates[0] != "agy/test/m" {
		t.Errorf("builder.Candidates = %v, want the reloaded row", builder.Candidates)
	}
}

// TestRefreshKeepsLastGoodRegistryOnBadRoles pins §6: a bad roles.json keeps
// the previous registry and warns, exactly as a bad policy.json does.
func TestRefreshKeepsLastGoodRegistryOnBadRoles(t *testing.T) {
	paths := ConfigPaths{
		Candidates: "/cand.json",
		Policy:     "/pol.json",
		Roles:      "/roles.json",
		ConfigDir:  "/cfg",
	}
	w := NewConfigWatcher(paths)

	t0 := time.Date(2026, 9, 19, 12, 0, 0, 0, time.UTC)
	roleStamp := fileStamp{mtime: t0, size: 10, exists: true}
	w.stat = func(path string) (fileStamp, error) {
		if path == paths.Roles {
			return roleStamp, nil
		}
		return fileStamp{mtime: t0, size: 10, exists: true}, nil
	}
	w.loadCandidates = func(path string) (*candidate.Set, []string, error) {
		return &candidate.Set{}, nil, nil
	}
	w.loadPolicy = func(path string) (policy.Policy, []string, error) {
		return policy.Policy{}, nil, nil
	}
	roleErr := error(nil)
	w.loadRoles = func(path string) (*roles.File, []string, error) {
		if roleErr != nil {
			return nil, nil, roleErr
		}
		return &roles.File{Rows: map[string]roles.Row{
			"builder": {Candidates: []string{"claude/test/m"}},
		}}, nil, nil
	}
	w.resolve = func(cfg *policy.Classify, configDir string, getenv func(string) string) classify.Classifier {
		return &classify.Fake{}
	}
	var warnMsgs []string
	w.warn = func(msg string, args ...any) {
		warnMsgs = append(warnMsgs, msg)
	}

	rt := Runtime{Now: func() time.Time { return t0 }}
	out1 := w.Refresh(rt)
	if out1.Registry == nil || out1.Registry.Source() != roles.SourceFile {
		t.Fatalf("Registry = %+v, want the roles.json registry", out1.Registry)
	}

	// The roles file changes and now fails to load.
	roleErr = roles.ErrBadRoles
	roleStamp.mtime = roleStamp.mtime.Add(time.Second)
	out2 := w.Refresh(out1)

	if out2.Registry != out1.Registry {
		t.Error("Registry changed on a bad roles.json")
	}
	if len(warnMsgs) != 1 {
		t.Fatalf("warn called %d times, want 1", len(warnMsgs))
	}
	if !strings.Contains(warnMsgs[0], "keeping the copy loaded at") {
		t.Errorf("warn text %q does not contain %q", warnMsgs[0], "keeping the copy loaded at")
	}
}

// TestRefreshLoadsLegacyRegistryWhenRolesPathEmpty pins §5.1: a ConfigPaths
// with no Roles still loads, and the registry it builds is the legacy
// derivation.
func TestRefreshLoadsLegacyRegistryWhenRolesPathEmpty(t *testing.T) {
	paths := ConfigPaths{
		Candidates: "/cand.json",
		Policy:     "/pol.json",
		ConfigDir:  "/cfg",
	}
	w := NewConfigWatcher(paths)

	t0 := time.Date(2026, 9, 19, 12, 0, 0, 0, time.UTC)
	w.stat = func(path string) (fileStamp, error) {
		if path == "" {
			t.Error("stat called for an empty Roles path")
		}
		return fileStamp{mtime: t0, size: 10, exists: true}, nil
	}
	w.loadCandidates = func(path string) (*candidate.Set, []string, error) {
		return &candidate.Set{}, nil, nil
	}
	w.loadPolicy = func(path string) (policy.Policy, []string, error) {
		return policy.Policy{}, nil, nil
	}
	w.loadRoles = func(path string) (*roles.File, []string, error) {
		t.Error("loadRoles called with an empty Roles path")
		return nil, nil, nil
	}
	w.resolve = func(cfg *policy.Classify, configDir string, getenv func(string) string) classify.Classifier {
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
