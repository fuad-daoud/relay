package doctor

import (
	"fmt"
	"strings"

	"github.com/fuad-daoud/relevo/internal/legacy"
)

// RenameCheck is the one global `rename` row (#292 §3): what relay-era state
// this machine still has, and what to do about it. It is pure -- the caller
// supplies the roots and their status -- and it never errors, because a probe
// relevo cannot complete is the caller's row to build (#292 §6).
//
// Three states, in this order:
//
//	Unmigrated  SevFail  an old root with no new one: relevo cannot run safely beside it
//	Stale       SevWarn  an old root beside its new one: something relay-era recreated it
//	neither     SevOK    no relay-era state
//
// Unmigrated is tested first: an install can be unmigrated in one root and
// stale in the other at the same time, and it is the unmigrated root that must
// stop relevo, so its failure wins over the warning.
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
		c.Detail = fmt.Sprintf("relay-era state at %s has not been migrated", strings.Join(unmigrated, ", "))
		c.Fix = "relevo migrate --dry-run && relevo migrate"
		return c
	}

	// The first stale pair, state before config: one row names one pair, and
	// the state root is the one whose contents matter.
	var old, new string
	switch {
	case s.OldState && s.NewState:
		old, new = r.OldState, r.NewState
	case s.OldConfig && s.NewConfig:
		old, new = r.OldConfig, r.NewConfig
	}
	if old != "" {
		c.Severity = SevWarn
		c.Detail = fmt.Sprintf("%s exists beside %s: an old relay binary or plugin recreated it", old, new)
		c.Fix = "ls -la " + old
		return c
	}

	c.Severity = SevOK
	c.Detail = "no relay-era state"
	return c
}
