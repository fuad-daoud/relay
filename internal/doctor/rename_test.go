package doctor

import (
	"testing"

	"github.com/fuad-daoud/relevo/internal/legacy"
)

func TestRenameCheck(t *testing.T) {
	roots := legacy.Roots{
		OldState:  "/old/state",
		NewState:  "/new/state",
		OldConfig: "/old/config",
		NewConfig: "/new/config",
	}

	tests := []struct {
		name       string
		st         legacy.Status
		wantSev    Severity
		wantDetail string
		wantFix    string
	}{
		{
			name:       "no relay-era state", // name-guard: legacy
			st:         legacy.Status{},
			wantSev:    SevOK,
			wantDetail: "no relay-era state", // name-guard: legacy
			wantFix:    "",
		},
		{
			name:       "unmigrated state",
			st:         legacy.Status{OldState: true},
			wantSev:    SevFail,
			wantDetail: "relay-era state at /old/state has not been migrated", // name-guard: legacy
			wantFix:    "relevo migrate --dry-run && relevo migrate",
		},
		{
			name:       "unmigrated config",
			st:         legacy.Status{OldConfig: true},
			wantSev:    SevFail,
			wantDetail: "relay-era state at /old/config has not been migrated", // name-guard: legacy
			wantFix:    "relevo migrate --dry-run && relevo migrate",
		},
		{
			name:       "unmigrated both, state first",
			st:         legacy.Status{OldState: true, OldConfig: true},
			wantSev:    SevFail,
			wantDetail: "relay-era state at /old/state, /old/config has not been migrated", // name-guard: legacy
			wantFix:    "relevo migrate --dry-run && relevo migrate",
		},
		{
			name:       "stale state",
			st:         legacy.Status{OldState: true, NewState: true},
			wantSev:    SevWarn,
			wantDetail: "/old/state exists beside /new/state: an old relay binary or plugin recreated it", // name-guard: legacy
			wantFix:    "ls -la /old/state",
		},
		{
			name:       "stale config",
			st:         legacy.Status{OldConfig: true, NewConfig: true},
			wantSev:    SevWarn,
			wantDetail: "/old/config exists beside /new/config: an old relay binary or plugin recreated it", // name-guard: legacy
			wantFix:    "ls -la /old/config",
		},
		{
			name:       "stale both, state pair reported",
			st:         legacy.Status{OldState: true, NewState: true, OldConfig: true, NewConfig: true},
			wantSev:    SevWarn,
			wantDetail: "/old/state exists beside /new/state: an old relay binary or plugin recreated it", // name-guard: legacy
			wantFix:    "ls -la /old/state",
		},
		{
			// The ordering rule: unmigrated wins over stale, so the state
			// root that has no new one is what the row names and fails on,
			// even though the config pair is stale.
			name:       "unmigrated wins over stale",
			st:         legacy.Status{OldState: true, NewState: false, OldConfig: true, NewConfig: true},
			wantSev:    SevFail,
			wantDetail: "relay-era state at /old/state has not been migrated", // name-guard: legacy
			wantFix:    "relevo migrate --dry-run && relevo migrate",
		},
		{
			name:       "unmigrated config wins over stale state",
			st:         legacy.Status{OldState: true, NewState: true, OldConfig: true, NewConfig: false},
			wantSev:    SevFail,
			wantDetail: "relay-era state at /old/config has not been migrated", // name-guard: legacy
			wantFix:    "relevo migrate --dry-run && relevo migrate",
		},
		{
			// New roots alone are not relay-era state: nothing to report. // name-guard: legacy
			name:       "new roots only",
			st:         legacy.Status{NewState: true, NewConfig: true},
			wantSev:    SevOK,
			wantDetail: "no relay-era state", // name-guard: legacy
			wantFix:    "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := RenameCheck(roots, tt.st)
			if got.Name != "rename" || got.Group != "" {
				t.Errorf("RenameCheck name/group = %q/%q, want \"rename\"/\"\"", got.Name, got.Group)
			}
			if got.Severity != tt.wantSev {
				t.Errorf("Severity = %v, want %v", got.Severity, tt.wantSev)
			}
			if got.Detail != tt.wantDetail {
				t.Errorf("Detail = %q, want %q", got.Detail, tt.wantDetail)
			}
			if got.Fix != tt.wantFix {
				t.Errorf("Fix = %q, want %q", got.Fix, tt.wantFix)
			}
		})
	}
}
