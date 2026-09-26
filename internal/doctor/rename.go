package doctor

import (
	"fmt"
	"strings"

	"github.com/fuad-daoud/relevo/internal/legacy"
)

// RenameCheck is the one global `rename` row: what relay-era state // name-guard: legacy
// this machine still has, and what to do about it. Pure, and never errors.
//
//	Unmigrated  SevFail  an old root with no new one
//	Stale       SevWarn  an old root beside its new one // name-guard: legacy
//	neither     SevOK    no relay-era state // name-guard: legacy
//
// Unmigrated is tested first: it is the root that must stop relevo, so its
// failure wins over a stale warning elsewhere.
func RenameCheck(r legacy.Roots, s legacy.Status) Check {
	c := Check{Name: "rename", Group: ""}

	var unmigrated []string
	if s.OldState && !s.NewState {
		unmigrated = append(unmigrated, r.OldState)
	}
	if s.OldConfig && !s.NewConfig {
		unmigrated = append(unmigrated, r.OldConfig)
	}
	if len(unmigrated) > 0 {
		c.Severity = SevFail
		c.Detail = fmt.Sprintf("relay-era state at %s has not been migrated", strings.Join(unmigrated, ", ")) // name-guard: legacy
		c.Fix = "relevo migrate --dry-run && relevo migrate"
		return c
	}

	var old, new string // the first stale pair, state before config
	switch {
	case s.OldState && s.NewState:
		old, new = r.OldState, r.NewState
	case s.OldConfig && s.NewConfig:
		old, new = r.OldConfig, r.NewConfig
	}
	if old != "" {
		c.Severity = SevWarn
		c.Detail = fmt.Sprintf("%s exists beside %s: an old relay binary or plugin recreated it", old, new) // name-guard: legacy
		c.Fix = "ls -la " + old
		return c
	}

	c.Severity = SevOK
	c.Detail = "no relay-era state" // name-guard: legacy
	return c
}
