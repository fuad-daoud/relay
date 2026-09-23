package proc

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/fuad-daoud/relevo/internal/relevo"
)

func TestScopeArgv(t *testing.T) {
	inner := []string{"/bin/sh", "-c", "script", "relevo-supervisor", "bin"}
	cases := map[string]struct {
		spec relevo.ScopeSpec
		want []string
	}{
		"full spec": {
			spec: relevo.ScopeSpec{Unit: "relevo-round-abc12345-foo-1", Slice: "relevo.slice", CPUWeight: 200, MemoryMax: "2G", TasksMax: 50},
			want: []string{
				"systemd-run", "--user", "--scope", "--quiet", "--collect", "--unit=relevo-round-abc12345-foo-1.scope",
				"--slice=relevo.slice",
				"-p", "CPUWeight=200",
				"-p", "MemoryMax=2G",
				"-p", "TasksMax=50",
				"--", "/bin/sh", "-c", "script", "relevo-supervisor", "bin",
			},
		},
		"no slice": {
			spec: relevo.ScopeSpec{Unit: "relevo-round-abc12345-foo-1", CPUWeight: 200, MemoryMax: "2G", TasksMax: 50},
			want: []string{
				"systemd-run", "--user", "--scope", "--quiet", "--collect", "--unit=relevo-round-abc12345-foo-1.scope",
				"-p", "CPUWeight=200",
				"-p", "MemoryMax=2G",
				"-p", "TasksMax=50",
				"--", "/bin/sh", "-c", "script", "relevo-supervisor", "bin",
			},
		},
		"no memory": {
			spec: relevo.ScopeSpec{Unit: "relevo-round-abc12345-foo-1", Slice: "relevo.slice", CPUWeight: 200, TasksMax: 50},
			want: []string{
				"systemd-run", "--user", "--scope", "--quiet", "--collect", "--unit=relevo-round-abc12345-foo-1.scope",
				"--slice=relevo.slice",
				"-p", "CPUWeight=200",
				"-p", "TasksMax=50",
				"--", "/bin/sh", "-c", "script", "relevo-supervisor", "bin",
			},
		},
		"no tasks": {
			spec: relevo.ScopeSpec{Unit: "relevo-round-abc12345-foo-1", Slice: "relevo.slice", CPUWeight: 200, MemoryMax: "2G"},
			want: []string{
				"systemd-run", "--user", "--scope", "--quiet", "--collect", "--unit=relevo-round-abc12345-foo-1.scope",
				"--slice=relevo.slice",
				"-p", "CPUWeight=200",
				"-p", "MemoryMax=2G",
				"--", "/bin/sh", "-c", "script", "relevo-supervisor", "bin",
			},
		},
		// The quota sits between the weight and the memory pairs (#295).
		"with quota": {
			spec: relevo.ScopeSpec{Unit: "relevo-round-abc12345-foo-1", Slice: "relevo.slice", CPUWeight: 200, CPUQuota: "200%", MemoryMax: "2G", TasksMax: 50},
			want: []string{
				"systemd-run", "--user", "--scope", "--quiet", "--collect", "--unit=relevo-round-abc12345-foo-1.scope",
				"--slice=relevo.slice",
				"-p", "CPUWeight=200",
				"-p", "CPUQuota=200%",
				"-p", "MemoryMax=2G",
				"-p", "TasksMax=50",
				"--", "/bin/sh", "-c", "script", "relevo-supervisor", "bin",
			},
		},
		// The pin sits between the quota and the memory pairs (#314).
		"with allowed cpus": {
			spec: relevo.ScopeSpec{Unit: "relevo-round-abc12345-foo-1", Slice: "relevo.slice", CPUWeight: 200, CPUQuota: "200%", AllowedCPUs: "2", MemoryMax: "2G", TasksMax: 50},
			want: []string{
				"systemd-run", "--user", "--scope", "--quiet", "--collect", "--unit=relevo-round-abc12345-foo-1.scope",
				"--slice=relevo.slice",
				"-p", "CPUWeight=200",
				"-p", "CPUQuota=200%",
				"-p", "AllowedCPUs=2",
				"-p", "MemoryMax=2G",
				"-p", "TasksMax=50",
				"--", "/bin/sh", "-c", "script", "relevo-supervisor", "bin",
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
		want relevo.ProcRusage
		ok   bool
	}{
		"both fields":      {"relevo-rusage:cpu_usec=123456 mem_peak=891289600", relevo.ProcRusage{CPUMS: 123, PeakMemBytes: 891289600}, true},
		"cpu only":         {"relevo-rusage:cpu_usec=5000", relevo.ProcRusage{CPUMS: 5}, true},
		"mem only":         {"relevo-rusage:mem_peak=1024", relevo.ProcRusage{PeakMemBytes: 1024}, true},
		"unknown key":      {"relevo-rusage:cpu_usec=1000 foo=bar", relevo.ProcRusage{CPUMS: 1}, true},
		"malformed number": {"relevo-rusage:cpu_usec=notanumber", relevo.ProcRusage{}, true},
		"legacy prefix":    {"relay-rusage:cpu_usec=12345 mem_peak=1048576", relevo.ProcRusage{CPUMS: 12, PeakMemBytes: 1048576}, true},
		"legacy cpu only":  {"relay-rusage:cpu_usec=5000", relevo.ProcRusage{CPUMS: 5}, true},
		"wrong prefix":     {"something-else:cpu_usec=1000", relevo.ProcRusage{}, false},
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
	got, ok := r.Rusage(context.Background(), relevo.ProcHandle{}, withRusage)
	if !ok {
		t.Fatal("Rusage: want ok=true")
	}
	if want := (relevo.ProcRusage{CPUMS: 12, PeakMemBytes: 1048576}); got != want {
		t.Errorf("Rusage = %+v, want %+v", got, want)
	}
	if code, ok := r.ExitCode(context.Background(), relevo.ProcHandle{}, withRusage); !ok || code != 0 {
		t.Errorf("ExitCode = %d, %v; want 0, true", code, ok)
	}

	withoutRusage := filepath.Join(dir, "without.jsonl")
	body2 := "builder output\n\n" + ExitTrailer + "0\n"
	if err := os.WriteFile(withoutRusage, []byte(body2), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, ok := r.Rusage(context.Background(), relevo.ProcHandle{}, withoutRusage); ok {
		t.Error("Rusage without a trailer line must be ok=false")
	}
	if code, ok := r.ExitCode(context.Background(), relevo.ProcHandle{}, withoutRusage); !ok || code != 0 {
		t.Errorf("ExitCode = %d, %v; want 0, true", code, ok)
	}
}

// TestRusageRealSupervisorLayout is the regression that would have caught
// #216: supervisorScript's printf leaves a blank line before each trailer
// (rule "\nrelevo-rusage:...\n" then "\nrelevo-exit:...\n"), so the rusage
// trailer is not reliably lastLines(path, 2)[0]. Built with the exact
// printf semantics the script uses.
func TestRusageRealSupervisorLayout(t *testing.T) {
	r := New()
	dir := t.TempDir()

	path := filepath.Join(dir, "real.jsonl")
	body := "builder said hi\n" +
		"\n" + RusageTrailer + "cpu_usec=19071588 mem_peak=403206144\n" +
		"\n" + ExitTrailer + "0\n"
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}

	got, ok := r.Rusage(context.Background(), relevo.ProcHandle{}, path)
	if !ok {
		t.Fatal("Rusage: want ok=true")
	}
	if want := (relevo.ProcRusage{CPUMS: 19071, PeakMemBytes: 403206144}); got != want {
		t.Errorf("Rusage = %+v, want %+v", got, want)
	}
	if code, ok := r.ExitCode(context.Background(), relevo.ProcHandle{}, path); !ok || code != 0 {
		t.Errorf("ExitCode = %d, %v; want 0, true", code, ok)
	}
}

// TestRusageIgnoresLaterOutput checks that a scan still finds the trailer
// when builder output follows the exit trailer in the stream.
func TestRusageIgnoresLaterOutput(t *testing.T) {
	r := New()
	dir := t.TempDir()

	path := filepath.Join(dir, "trailing.jsonl")
	body := "builder said hi\n" +
		"\n" + RusageTrailer + "cpu_usec=19071588 mem_peak=403206144\n" +
		"\n" + ExitTrailer + "0\n" +
		"stray output after exit\n"
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}

	got, ok := r.Rusage(context.Background(), relevo.ProcHandle{}, path)
	if !ok {
		t.Fatal("Rusage: want ok=true")
	}
	if want := (relevo.ProcRusage{CPUMS: 19071, PeakMemBytes: 403206144}); got != want {
		t.Errorf("Rusage = %+v, want %+v", got, want)
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
		if err := ProbeScopes(context.Background(), "relevo.slice"); err != nil {
			t.Fatalf("ProbeScopes: %v", err)
		}
	})
	t.Run("failure", func(t *testing.T) {
		dir := t.TempDir()
		writeStub(t, dir, "#!/bin/sh\necho 'Failed to start transient scope unit: Permission denied' >&2\nexit 1\n")
		t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
		err := ProbeScopes(context.Background(), "relevo.slice")
		if err == nil || !strings.Contains(err.Error(), "Failed to start transient scope unit: Permission denied") {
			t.Fatalf("ProbeScopes error = %v; want it to contain the stub's stderr line", err)
		}
	})
}

// TestProbeAllowedCPUsStub puts a fake systemd-run on PATH so the test never
// calls the real one (#314). The success stub execs everything after "--";
// the failure stub reproduces the stderr line a refused AllowedCPUs prints.
func TestProbeAllowedCPUsStub(t *testing.T) {
	t.Run("success", func(t *testing.T) {
		dir := t.TempDir()
		writeStub(t, dir, "#!/bin/sh\nwhile [ \"$1\" != \"--\" ]; do shift; done\nshift\nexec \"$@\"\n")
		t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
		if err := ProbeAllowedCPUs(context.Background(), "relevo.slice", "2"); err != nil {
			t.Fatalf("ProbeAllowedCPUs: %v", err)
		}
	})
	t.Run("failure", func(t *testing.T) {
		dir := t.TempDir()
		writeStub(t, dir, "#!/bin/sh\necho 'Failed to set AllowedCPUs: Permission denied' >&2\nexit 1\n")
		t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
		err := ProbeAllowedCPUs(context.Background(), "relevo.slice", "2")
		if err == nil || !strings.Contains(err.Error(), "Failed to set AllowedCPUs: Permission denied") {
			t.Fatalf("ProbeAllowedCPUs error = %v; want it to contain the stub's stderr line", err)
		}
		if !strings.Contains(err.Error(), "AllowedCPUs=2") {
			t.Errorf("ProbeAllowedCPUs error = %v; want it to name AllowedCPUs=2", err)
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
