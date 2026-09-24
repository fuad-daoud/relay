package doctor

import (
	"strings"
	"testing"
	"time"

	"github.com/fuad-daoud/relevo/internal/hooks"
)

func TestHooksCheck(t *testing.T) {
	now := time.Date(2026, 9, 24, 12, 0, 0, 0, time.UTC)

	t.Run("no failures is OK", func(t *testing.T) {
		c := HooksCheck(HooksCheckInput{Now: now, Runs: []hooks.HookRun{
			{At: now.Add(-time.Minute), Event: "state_changed"},
			{At: now.Add(-2 * time.Minute), Event: "round_started"},
		}})
		if c.Name != "hooks" {
			t.Errorf("name = %q, want hooks", c.Name)
		}
		if c.Severity != SevOK {
			t.Errorf("severity = %v, want SevOK", c.Severity)
		}
		if want := "hooks: 2 runs, 0 failed in the last 24h"; c.Detail != want {
			t.Errorf("detail = %q, want %q", c.Detail, want)
		}
	})

	t.Run("a recent failure warns and is named", func(t *testing.T) {
		c := HooksCheck(HooksCheckInput{Now: now, Runs: []hooks.HookRun{
			{At: now.Add(-time.Hour), Event: "state_changed", ExitCode: 2, Error: "exit status 2"},
		}})
		if c.Severity != SevWarn {
			t.Errorf("severity = %v, want SevWarn", c.Severity)
		}
		want := "hooks: 1 runs, 1 failed in the last 24h (last: state_changed: exit status 2)"
		if c.Detail != want {
			t.Errorf("detail = %q, want %q", c.Detail, want)
		}
	})

	t.Run("a failure outside the window does not warn but is named on one line", func(t *testing.T) {
		c := HooksCheck(HooksCheckInput{Now: now, Runs: []hooks.HookRun{
			{At: now.Add(-48 * time.Hour), Event: "round_started", ExitCode: 1, Error: "exit status 1\nsecond line"},
		}})
		if c.Severity != SevOK {
			t.Errorf("severity = %v, want SevOK: the failure is outside the window", c.Severity)
		}
		if want := "hooks: 1 runs, 0 failed in the last 24h (last: round_started: exit status 1)"; c.Detail != want {
			t.Errorf("detail = %q, want %q", c.Detail, want)
		}
		if strings.Contains(c.Detail, "\n") {
			t.Errorf("detail spans lines: %q", c.Detail)
		}
	})
}
