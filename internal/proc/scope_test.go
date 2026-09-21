package proc

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/fuad-daoud/relay/internal/relay"
)

func TestScopeArgv(t *testing.T) {
	inner := []string{"/bin/sh", "-c", "script", "relay-supervisor", "bin"}
	cases := map[string]struct {
		spec relay.ScopeSpec
		want []string
	}{
		"full spec": {
			spec: relay.ScopeSpec{Unit: "relay-round-abc12345-foo-1", Slice: "relay.slice", CPUWeight: 200, MemoryMax: "2G", TasksMax: 50},
			want: []string{
				"systemd-run", "--user", "--scope", "--quiet", "--collect", "--unit=relay-round-abc12345-foo-1.scope",
				"--slice=relay.slice",
				"-p", "CPUWeight=200",
				"-p", "MemoryMax=2G",
				"-p", "TasksMax=50",
				"--", "/bin/sh", "-c", "script", "relay-supervisor", "bin",
			},
		},
		"no slice": {
			spec: relay.ScopeSpec{Unit: "relay-round-abc12345-foo-1", CPUWeight: 200, MemoryMax: "2G", TasksMax: 50},
			want: []string{
				"systemd-run", "--user", "--scope", "--quiet", "--collect", "--unit=relay-round-abc12345-foo-1.scope",
				"-p", "CPUWeight=200",
				"-p", "MemoryMax=2G",
				"-p", "TasksMax=50",
				"--", "/bin/sh", "-c", "script", "relay-supervisor", "bin",
			},
		},
		"no memory": {
			spec: relay.ScopeSpec{Unit: "relay-round-abc12345-foo-1", Slice: "relay.slice", CPUWeight: 200, TasksMax: 50},
			want: []string{
				"systemd-run", "--user", "--scope", "--quiet", "--collect", "--unit=relay-round-abc12345-foo-1.scope",
				"--slice=relay.slice",
				"-p", "CPUWeight=200",
				"-p", "TasksMax=50",
				"--", "/bin/sh", "-c", "script", "relay-supervisor", "bin",
			},
		},
		"no tasks": {
			spec: relay.ScopeSpec{Unit: "relay-round-abc12345-foo-1", Slice: "relay.slice", CPUWeight: 200, MemoryMax: "2G"},
			want: []string{
				"systemd-run", "--user", "--scope", "--quiet", "--collect", "--unit=relay-round-abc12345-foo-1.scope",
				"--slice=relay.slice",
				"-p", "CPUWeight=200",
				"-p", "MemoryMax=2G",
				"--", "/bin/sh", "-c", "script", "relay-supervisor", "bin",
			},
		},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			got := ScopeArgv(c.spec, inner)
			if !reflect.DeepEqual(got, c.want) {
				t.Errorf("ScopeArgv = %#v, want %#v", got, c.want)
			}
		})
	}
}

func TestParseRusageTrailer(t *testing.T) {
	cases := map[string]struct {
		line string
		want relay.ProcRusage
		ok   bool
	}{
		"both fields":      {"relay-rusage:cpu_usec=123456 mem_peak=891289600", relay.ProcRusage{CPUMS: 123, PeakMemBytes: 891289600}, true},
		"cpu only":         {"relay-rusage:cpu_usec=5000", relay.ProcRusage{CPUMS: 5}, true},
		"mem only":         {"relay-rusage:mem_peak=1024", relay.ProcRusage{PeakMemBytes: 1024}, true},
		"unknown key":      {"relay-rusage:cpu_usec=1000 foo=bar", relay.ProcRusage{CPUMS: 1}, true},
		"malformed number": {"relay-rusage:cpu_usec=notanumber", relay.ProcRusage{}, true},
		"wrong prefix":     {"something-else:cpu_usec=1000", relay.ProcRusage{}, false},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			got, ok := ParseRusageTrailer(c.line)
			if ok != c.ok || got != c.want {
				t.Errorf("ParseRusageTrailer(%q) = %+v, %v; want %+v, %v", c.line, got, ok, c.want, c.ok)
			}
		})
	}
}

func TestRusageReadsSecondToLastLine(t *testing.T) {
	r := New()
	dir := t.TempDir()

	withRusage := filepath.Join(dir, "with.jsonl")
	body := "builder output\n\n" + RusageTrailer + "cpu_usec=12345 mem_peak=1048576\n" + ExitTrailer + "0\n"
	if err := os.WriteFile(withRusage, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	got, ok := r.Rusage(context.Background(), relay.ProcHandle{}, withRusage)
	if !ok {
		t.Fatal("Rusage: want ok=true")
	}
	if want := (relay.ProcRusage{CPUMS: 12, PeakMemBytes: 1048576}); got != want {
		t.Errorf("Rusage = %+v, want %+v", got, want)
	}
	if code, ok := r.ExitCode(context.Background(), relay.ProcHandle{}, withRusage); !ok || code != 0 {
		t.Errorf("ExitCode = %d, %v; want 0, true", code, ok)
	}

	withoutRusage := filepath.Join(dir, "without.jsonl")
	body2 := "builder output\n\n" + ExitTrailer + "0\n"
	if err := os.WriteFile(withoutRusage, []byte(body2), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, ok := r.Rusage(context.Background(), relay.ProcHandle{}, withoutRusage); ok {
		t.Error("Rusage without a trailer line must be ok=false")
	}
	if code, ok := r.ExitCode(context.Background(), relay.ProcHandle{}, withoutRusage); !ok || code != 0 {
		t.Errorf("ExitCode = %d, %v; want 0, true", code, ok)
	}
}

// TestProbeScopesStub puts a fake systemd-run on PATH so the test never
// calls the real one (CI has no systemd). The success stub execs
// everything after "--", the way systemd-run --scope would; the failure
// stub reproduces the stderr line a refused scope actually prints.
func TestProbeScopesStub(t *testing.T) {
	t.Run("success", func(t *testing.T) {
		dir := t.TempDir()
		writeStub(t, dir, "#!/bin/sh\nwhile [ \"$1\" != \"--\" ]; do shift; done\nshift\nexec \"$@\"\n")
		t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
		if err := ProbeScopes(context.Background(), "relay.slice"); err != nil {
			t.Fatalf("ProbeScopes: %v", err)
		}
	})
	t.Run("failure", func(t *testing.T) {
		dir := t.TempDir()
		writeStub(t, dir, "#!/bin/sh\necho 'Failed to start transient scope unit: Permission denied' >&2\nexit 1\n")
		t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
		err := ProbeScopes(context.Background(), "relay.slice")
		if err == nil || !strings.Contains(err.Error(), "Failed to start transient scope unit: Permission denied") {
			t.Fatalf("ProbeScopes error = %v; want it to contain the stub's stderr line", err)
		}
	})
}

func writeStub(t *testing.T, dir, script string) {
	t.Helper()
	p := filepath.Join(dir, "systemd-run")
	if err := os.WriteFile(p, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
}
