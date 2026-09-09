package main

import (
	"bytes"
	"path/filepath"
	"strings"
	"testing"

	"github.com/fuad-daoud/relay/internal/alias"
	"github.com/fuad-daoud/relay/internal/doctor"
	"github.com/fuad-daoud/relay/internal/store"
)

func TestAssembleKinds(t *testing.T) {
	tempHome := t.TempDir()
	st := store.New(filepath.Join(tempHome, "store"))
	tbl := alias.DefaultTable()

	// Default table has agy, claude, opencode
	kinds := assembleKinds(tbl, st)
	expected := []string{"agy", "claude", "opencode"}
	if len(kinds) != len(expected) {
		t.Fatalf("kinds len = %d, want %d: %v", len(kinds), len(expected), kinds)
	}
	for i, k := range expected {
		if kinds[i] != k {
			t.Errorf("kinds[%d] = %q, want %q", i, kinds[i], k)
		}
	}

	// Add a binding with a custom builder kind
	b := store.Binding{
		Name: "custom",
		CWD:  tempHome,
		Builder: store.Endpoint{
			Kind: "custom-kind",
		},
	}
	if err := st.Save(b); err != nil {
		t.Fatalf("st.Save: %v", err)
	}

	kinds2 := assembleKinds(tbl, st)
	foundCustom := false
	for _, k := range kinds2 {
		if k == "custom-kind" {
			foundCustom = true
			break
		}
	}
	if !foundCustom {
		t.Errorf("custom-kind not found in assembled kinds: %v", kinds2)
	}
}

func TestRenderReportVerdict(t *testing.T) {
	// Success case
	repOk := doctor.Report{
		Checks: []doctor.Check{
			{Group: "", Name: "herdr", Severity: doctor.SevOK, Detail: "0.9.0 (floor 0.8.2)"},
			{Group: "", Name: "daemon", Severity: doctor.SevOK, Detail: "running"},
			{Group: "claude", Name: "binary", Severity: doctor.SevOK, Detail: "/usr/bin/claude"},
			{Group: "claude", Name: "integration", Severity: doctor.SevWarn, Detail: "outdated (v8 < v9)", Fix: "herdr integration install claude"},
		},
		UsableBuilder: true,
	}

	var buf bytes.Buffer
	renderReport(&buf, repOk)
	out := buf.String()

	if !strings.Contains(out, "1 warning, 0 failures -- relay can run.") {
		t.Errorf("expected success footer, got: %s", out)
	}
	if !strings.Contains(out, "    fix: herdr integration install claude") {
		t.Errorf("expected indented fix line, got: %s", out)
	}

	// Failure case
	repFail := doctor.Report{
		Checks: []doctor.Check{
			{Group: "", Name: "herdr", Severity: doctor.SevFail, Detail: "not found"},
		},
		UsableBuilder: false,
	}

	buf.Reset()
	renderReport(&buf, repFail)
	outFail := buf.String()

	if !strings.Contains(outFail, "1 failure, 0 warnings -- no usable builder. Fix the failure above.") {
		t.Errorf("expected failure footer, got: %s", outFail)
	}
}
