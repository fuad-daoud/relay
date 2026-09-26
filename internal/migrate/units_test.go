package migrate

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/fuad-daoud/relevo/dist"
	"github.com/fuad-daoud/relevo/internal/legacy"
)

// recServices records every call a unit step made; it is the only Services CI ever sees.
type recServices struct {
	calls           []string
	active, enabled bool
	stateErr        error
	err             error
}

func (r *recServices) record(parts ...string) { r.calls = append(r.calls, strings.Join(parts, " ")) }

func (r *recServices) Stop(_ context.Context, u Unit) error  { r.record("stop", u.Name); return r.err }
func (r *recServices) Start(_ context.Context, u Unit) error { r.record("start", u.Name); return r.err }
func (r *recServices) Enable(_ context.Context, u Unit) error {
	r.record("enable", u.Name)
	return r.err
}
func (r *recServices) Disable(_ context.Context, u Unit) error {
	r.record("disable", u.Name)
	return r.err
}
func (r *recServices) Reload(context.Context) error { r.record("reload"); return r.err }

func (r *recServices) State(_ context.Context, u Unit) (bool, bool, error) {
	r.record("state", u.Name)
	return r.active, r.enabled, r.stateErr
}

func linuxUnitOptions(t *testing.T, configHome string, svc *recServices) (UnitOptions, *bytes.Buffer) {
	t.Helper()
	out := &bytes.Buffer{}
	return UnitOptions{
		Units:          DefaultClientUnits("linux", configHome, configHome),
		Services:       svc,
		ClientUnitText: "UNIT-TEXT\n",
		Exe:            "/usr/local/bin/relevo",
		Home:           configHome,
		Out:            out,
	}, out
}

// TestDefaultClientUnits pins the four unit paths to the old names. // name-guard: legacy
func TestDefaultClientUnits(t *testing.T) {
	configHome := "/cfg"
	home := "/home/u"

	linux := DefaultClientUnits("linux", configHome, home)
	if linux.Platform != "linux" {
		t.Errorf("linux platform = %q", linux.Platform)
	}
	if want := filepath.Join(configHome, "systemd", "user", legacy.ClientUnit); linux.Old.Path != want {
		t.Errorf("linux old path = %q, want %q", linux.Old.Path, want)
	}
	if linux.Old.Name != legacy.ClientUnit {
		t.Errorf("linux old name = %q, want %q", linux.Old.Name, legacy.ClientUnit)
	}
	if want := filepath.Join(configHome, "systemd", "user", "relevo.service"); linux.New.Path != want {
		t.Errorf("linux new path = %q, want %q", linux.New.Path, want)
	}

	darwin := DefaultClientUnits("darwin", configHome, home)
	if want := filepath.Join(home, "Library", "LaunchAgents", legacy.LaunchdLabel+".plist"); darwin.Old.Path != want {
		t.Errorf("darwin old path = %q, want %q", darwin.Old.Path, want)
	}
	if want := filepath.Join(home, "Library", "LaunchAgents", "com.github.fuad-daoud.relevo.plist"); darwin.New.Path != want {
		t.Errorf("darwin new path = %q, want %q", darwin.New.Path, want)
	}

	other := DefaultClientUnits("windows", configHome, home)
	if other.Old.Path != "" || other.New.Path != "" {
		t.Errorf("windows units = %+v, want empty", other)
	}
}

// TestStopOldAndSwapClientLinuxHappyPath pins the ordinary cutover end to end.
func TestStopOldAndSwapClientLinuxHappyPath(t *testing.T) {
	configHome := t.TempDir()
	svc := &recServices{active: true, enabled: true}
	o, _ := linuxUnitOptions(t, configHome, svc)

	mustWrite(t, o.Units.Old.Path, "OLD UNIT\n")

	step, old, err := StopOld(context.Background(), o)
	if err != nil {
		t.Fatalf("StopOld: %v", err)
	}
	if step.Skipped {
		t.Errorf("stop step = %+v, want not skipped", step)
	}
	if old != (OldUnit{Installed: true, WasActive: true, WasEnabled: true}) {
		t.Errorf("old = %+v", old)
	}

	if _, err := SwapClient(context.Background(), o, old); err != nil {
		t.Fatalf("SwapClient: %v", err)
	}

	got, err := os.ReadFile(o.Units.New.Path)
	if err != nil {
		t.Fatalf("read new unit: %v", err)
	}
	if string(got) != o.ClientUnitText {
		t.Errorf("new unit = %q, want %q", got, o.ClientUnitText)
	}
	if _, err := os.Stat(o.Units.Old.Path); !os.IsNotExist(err) {
		t.Errorf("old unit still exists: %v", err)
	}

	want := []string{
		"state " + legacy.ClientUnit,
		"stop " + legacy.ClientUnit,
		"reload",
		"enable relevo.service",
		"disable " + legacy.ClientUnit,
		"reload",
	}
	if !reflect.DeepEqual(svc.calls, want) {
		t.Errorf("calls = %v, want %v", svc.calls, want)
	}
}

// TestSwapClientInstalledButInactive pins: not started, but still installed and retired.
func TestSwapClientInstalledButInactive(t *testing.T) {
	configHome := t.TempDir()
	svc := &recServices{}
	o, _ := linuxUnitOptions(t, configHome, svc)

	mustWrite(t, o.Units.Old.Path, "OLD UNIT\n")

	step, old, err := StopOld(context.Background(), o)
	if err != nil {
		t.Fatalf("StopOld: %v", err)
	}
	if !step.Skipped || !strings.Contains(step.Detail, "not running") {
		t.Errorf("stop step = %+v, want skipped not running", step)
	}
	if old != (OldUnit{Installed: true}) {
		t.Errorf("old = %+v, want only Installed", old)
	}

	if _, err := SwapClient(context.Background(), o, old); err != nil {
		t.Fatalf("SwapClient: %v", err)
	}

	if _, err := os.Stat(o.Units.New.Path); err != nil {
		t.Errorf("new unit not written: %v", err)
	}
	if _, err := os.Stat(o.Units.Old.Path); !os.IsNotExist(err) {
		t.Errorf("old unit still exists: %v", err)
	}

	for _, call := range svc.calls {
		if strings.HasPrefix(call, "stop ") {
			t.Errorf("StopOld called %q, want no stop", call)
		}
		if strings.HasPrefix(call, "enable ") {
			t.Errorf("SwapClient called %q, want no enable", call)
		}
	}
	want := []string{"state " + legacy.ClientUnit, "reload", "disable " + legacy.ClientUnit, "reload"}
	if !reflect.DeepEqual(svc.calls, want) {
		t.Errorf("calls = %v, want %v", svc.calls, want)
	}
}

// TestSwapClientDarwinTemplate pins that no @BIN@/@HOME@ placeholder survives rendering.
func TestSwapClientDarwinTemplate(t *testing.T) {
	home := t.TempDir()
	svc := &recServices{active: true, enabled: true}
	out := &bytes.Buffer{}
	o := UnitOptions{
		Units:         DefaultClientUnits("darwin", home, home),
		Services:      svc,
		PlistTemplate: dist.LaunchdPlist,
		Exe:           "/usr/local/bin/relevo",
		Home:          home,
		Out:           out,
	}

	mustWrite(t, o.Units.Old.Path, "OLD PLIST\n")

	_, old, err := StopOld(context.Background(), o)
	if err != nil {
		t.Fatalf("StopOld: %v", err)
	}
	if _, err := SwapClient(context.Background(), o, old); err != nil {
		t.Fatalf("SwapClient: %v", err)
	}

	got, err := os.ReadFile(o.Units.New.Path)
	if err != nil {
		t.Fatalf("read plist: %v", err)
	}
	body := string(got)
	if strings.Contains(body, "@BIN@") || strings.Contains(body, "@HOME@") {
		t.Errorf("plist still holds a placeholder:\n%s", body)
	}
	if !strings.Contains(body, o.Exe) {
		t.Errorf("plist does not contain %q", o.Exe)
	}
	if _, err := os.Stat(o.Units.Old.Path); !os.IsNotExist(err) {
		t.Errorf("old plist still exists: %v", err)
	}
}

func TestSwapClientNoOldUnit(t *testing.T) {
	configHome := t.TempDir()
	svc := &recServices{}
	o, _ := linuxUnitOptions(t, configHome, svc)

	steps, err := SwapClient(context.Background(), o, OldUnit{})
	if err != nil {
		t.Fatalf("SwapClient: %v", err)
	}
	if len(steps) != 1 {
		t.Fatalf("steps = %v, want one", steps)
	}
	if !steps[0].Skipped || !strings.Contains(steps[0].Detail, "no client unit was installed") {
		t.Errorf("step = %+v", steps[0])
	}
	if len(svc.calls) != 0 {
		t.Errorf("calls = %v, want none", svc.calls)
	}
	if _, err := os.Stat(o.Units.New.Path); !os.IsNotExist(err) {
		t.Errorf("new unit written anyway: %v", err)
	}
}

// TestSwapClientIdempotent pins that a re-run writes and retires nothing.
func TestSwapClientIdempotent(t *testing.T) {
	configHome := t.TempDir()
	svc := &recServices{}
	o, _ := linuxUnitOptions(t, configHome, svc)

	mustWrite(t, o.Units.New.Path, o.ClientUnitText)
	old := OldUnit{Installed: true, WasActive: true, WasEnabled: true}

	steps, err := SwapClient(context.Background(), o, old)
	if err != nil {
		t.Fatalf("SwapClient: %v", err)
	}
	if _, err := os.Stat(o.Units.Old.Path); !os.IsNotExist(err) {
		t.Errorf("old unit exists: %v", err)
	}
	want := []string{"enable relevo.service"}
	if !reflect.DeepEqual(svc.calls, want) {
		t.Errorf("calls = %v, want %v", svc.calls, want)
	}
	for _, s := range steps {
		if s.Name == "retire" {
			t.Errorf("retire step on an absent old file: %+v", s)
		}
	}
}

func TestUnitDryRun(t *testing.T) {
	configHome := t.TempDir()
	svc := &recServices{active: true, enabled: true}
	o, _ := linuxUnitOptions(t, configHome, svc)
	o.DryRun = true

	mustWrite(t, o.Units.Old.Path, "OLD UNIT\n")
	before := treeSnapshot(t, configHome)

	_, old, err := StopOld(context.Background(), o)
	if err != nil {
		t.Fatalf("StopOld: %v", err)
	}
	if !old.Installed {
		t.Errorf("old = %+v, want Installed", old)
	}
	if _, err := SwapClient(context.Background(), o, old); err != nil {
		t.Fatalf("SwapClient: %v", err)
	}

	if len(svc.calls) != 0 {
		t.Errorf("dry run called Services: %v", svc.calls)
	}
	if after := treeSnapshot(t, configHome); !reflect.DeepEqual(before, after) {
		t.Errorf("dry run changed the tree:\nbefore: %v\nafter:  %v", before, after)
	}
}

// TestRemoveOldBinary covers removed, self-same, kept and absent.
func TestRemoveOldBinary(t *testing.T) {
	tests := []struct {
		name       string
		keep       bool
		link       bool // hard-link sibling to exe: "this is the running binary"
		noSibling  bool
		wantSkip   bool
		wantDetail string
	}{
		{name: "present"},
		{name: "same file", link: true, wantSkip: true, wantDetail: "is this binary"},
		{name: "keep", keep: true, wantSkip: true, wantDetail: "--keep-old-binary"},
		{name: "absent", noSibling: true, wantSkip: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			exe := filepath.Join(dir, "relevo")
			mustWrite(t, exe, "relevo")
			sibling := filepath.Join(dir, legacy.Binary)
			switch {
			case tt.noSibling:
			case tt.link:
				if err := os.Link(exe, sibling); err != nil {
					t.Fatalf("link: %v", err)
				}
			default:
				mustWrite(t, sibling, legacy.Binary)
			}

			step, err := RemoveOldBinary(exe, tt.keep, false)
			if err != nil {
				t.Fatalf("RemoveOldBinary: %v", err)
			}
			if step.Skipped != tt.wantSkip {
				t.Errorf("step = %+v, want Skipped=%v", step, tt.wantSkip)
			}
			if tt.wantDetail != "" && !strings.Contains(step.Detail, tt.wantDetail) {
				t.Errorf("detail = %q, want containing %q", step.Detail, tt.wantDetail)
			}
			if tt.noSibling {
				return
			}
			_, err = os.Stat(sibling)
			if tt.wantSkip && err != nil {
				t.Errorf("sibling removed: %v", err)
			}
			if !tt.wantSkip && !os.IsNotExist(err) {
				t.Errorf("sibling still exists: %v", err)
			}
		})
	}
}

type renameSliceCase struct {
	name       string
	before     string // "" means no policy.json file at all
	dryRun     bool
	wantSkip   bool
	wantDetail string
	want       string // expected content after; "" means unchanged from before
	checkMtime bool
}

// TestRenameSliceValue pins that only the quoted relay.slice value changes. // name-guard: legacy
func TestRenameSliceValue(t *testing.T) {
	tests := []renameSliceCase{
		{
			name:   "rewrites",
			before: `{"serve":{"scope":{"slice":"` + legacy.Slice + `","mem":"6G"}}}`,
			want:   `{"serve":{"scope":{"slice":"relevo.slice","mem":"6G"}}}`,
		},
		{
			name:       "untouched without the value",
			before:     `{"serve":{"scope":{"slice":"relay.slicer"}}}`, // name-guard: legacy
			wantSkip:   true,
			checkMtime: true,
		},
		{
			name:       "absent",
			wantSkip:   true,
			wantDetail: "no policy.json",
		},
		{
			name:   "dry run",
			before: `{"slice":"` + legacy.Slice + `"}`,
			dryRun: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) { runRenameSliceCase(t, tt) })
	}
}

func runRenameSliceCase(t *testing.T, tt renameSliceCase) {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "policy.json")
	old := time.Now().Add(-time.Hour).Truncate(time.Second)
	if tt.before != "" {
		mustWrite(t, path, tt.before)
		if err := os.Chtimes(path, old, old); err != nil {
			t.Fatalf("chtimes: %v", err)
		}
	}

	step, err := RenameSliceValue(dir, tt.dryRun)
	if err != nil {
		t.Fatalf("RenameSliceValue: %v", err)
	}
	if step.Skipped != tt.wantSkip {
		t.Errorf("step = %+v, want Skipped=%v", step, tt.wantSkip)
	}
	if tt.wantDetail != "" && !strings.Contains(step.Detail, tt.wantDetail) {
		t.Errorf("detail = %q, want containing %q", step.Detail, tt.wantDetail)
	}
	if tt.before == "" {
		return
	}

	want := tt.want
	if want == "" {
		want = tt.before
	}
	if got := readFixture(t, path); got != want {
		t.Errorf("policy.json = %q, want %q", got, want)
	}
	if !tt.checkMtime {
		return
	}
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if !fi.ModTime().Equal(old) {
		t.Errorf("mtime = %v, want untouched %v", fi.ModTime(), old)
	}
}
