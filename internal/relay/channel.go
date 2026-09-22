package relay

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/fuad-daoud/relay/internal/planner"
)

// ClaimTTL is how stale a claim's SeenAt may be before it is treated as dead.
const ClaimTTL = 10 * time.Second

// Claim is one relay mcp process's hold on a planner's mailbox (spec
// docs/specs/2026-09-21-planner-channel-design.md §3.2, re-keyed by planner id
// in docs/specs/2026-09-22-drop-herdr-design.md §3.3).
type Claim struct {
	Planner       string    `json:"planner"`         // required; the relay planner id, e.g. "pl_abc…"
	PID           int       `json:"pid"`             // required; > 0
	HostPID       int       `json:"host_pid"`        // the harness process `relay mcp` runs under; 0 when unknown
	HostStartedAt int64     `json:"host_started_at"` // start time of HostPID in Unix seconds; 0 when unknown
	StartedAt     time.Time `json:"started_at"`      // required
	SeenAt        time.Time `json:"seen_at"`         // required; refreshed each poll
	CWD           string    `json:"cwd"`             // informational
	Version       string    `json:"version"`         // relay version that wrote it; informational
}

// ErrClaimHeld reports that a different live claim already exists for a
// planner.
var ErrClaimHeld = errors.New("planner already has a live channel")

// ClaimStore is what the daemon and relay mcp share. A nil ClaimStore on a
// Runtime means "no claims exist"; DeliverPending's guard treats it that way.
type ClaimStore interface {
	// Live returns the claim for planner if it is live: parses, pid alive,
	// now - SeenAt <= ClaimTTL. A file that exists but is not live is
	// removed and (nil, nil) returned. Missing file -> (nil, nil). A name
	// that is not a planner id is not a claim this version made: it is
	// ignored (nil, nil) and left on disk for SweepPaneKeyed.
	Live(planner string, now time.Time) (*Claim, error)
	// Write persists c (atomic: temp file + rename). Returns ErrClaimHeld
	// when a different live claim (other PID) exists for c.Planner.
	Write(c Claim, now time.Time) error
	// Remove deletes the claim file for planner if its PID equals pid. No
	// error when absent.
	Remove(planner string, pid int) error
	// SweepPaneKeyed removes the pre-#303 pane-keyed claim files whose
	// writer is dead, and returns how many it removed. A live pane-keyed
	// claim -- another planner session still running an older relay mcp --
	// is left alone.
	SweepPaneKeyed() int
}

// FileClaims is the on-disk ClaimStore: one JSON file per planner under Root.
type FileClaims struct {
	Root string
	// Alive reports whether pid names a live process. Nil uses the
	// default: syscall.Kill(pid, 0) == nil || the error is EPERM (a
	// process we cannot signal is still alive).
	Alive func(pid int) bool
}

// ClaimFileName turns a planner id into the claim file's base name: the id
// plus the ".json" extension. A planner id is path-safe (planner.ValidID), so
// an id can never become a path.
func ClaimFileName(plannerID string) string {
	return plannerID + ".json"
}

func (f *FileClaims) alive(pid int) bool {
	if f.Alive != nil {
		return f.Alive(pid)
	}
	return defaultClaimAlive(pid)
}

func (f *FileClaims) path(plannerID string) string {
	return filepath.Join(f.Root, ClaimFileName(plannerID))
}

// Live implements ClaimStore.
func (f *FileClaims) Live(plannerID string, now time.Time) (*Claim, error) {
	if plannerID == "" {
		return nil, errors.New("empty planner")
	}
	if planner.ValidID(plannerID) != nil {
		// Not a planner id: a pre-#303 pane-keyed claim. This version did
		// not write it and must not rewrite it; only SweepPaneKeyed may
		// remove it, and only once its writer is dead.
		return nil, nil
	}

	path := f.path(plannerID)
	raw, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}

	var c Claim
	live := true
	if err := json.Unmarshal(raw, &c); err != nil {
		live = false
	} else if c.PID <= 0 || !f.alive(c.PID) || now.Sub(c.SeenAt) > ClaimTTL {
		live = false
	}
	if live {
		return &c, nil
	}

	// Stale: whoever reads it cleans it up. Removal failures are not worth
	// failing the read over -- the caller already has its answer, "not live".
	_ = os.Remove(path)
	return nil, nil
}

// Write implements ClaimStore.
func (f *FileClaims) Write(c Claim, now time.Time) error {
	if c.Planner == "" {
		return errors.New("empty planner")
	}
	if planner.ValidID(c.Planner) != nil {
		// A claim is keyed by a planner id; refusing anything else keeps a
		// new pane-keyed file from ever being written again.
		return errors.New("claim planner must be a planner id")
	}

	existing, err := f.Live(c.Planner, now)
	if err != nil {
		return err
	}
	if existing != nil && existing.PID != c.PID {
		return ErrClaimHeld
	}

	if err := os.MkdirAll(f.Root, 0o755); err != nil {
		return err
	}

	raw, err := json.Marshal(c)
	if err != nil {
		return err
	}

	tmp, err := os.CreateTemp(f.Root, ".tmp-claim-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	if _, err := tmp.Write(raw); err != nil {
		tmp.Close()
		os.Remove(tmpName)
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpName)
		return err
	}
	if err := os.Rename(tmpName, f.path(c.Planner)); err != nil {
		os.Remove(tmpName)
		return err
	}

	return nil
}

// Remove implements ClaimStore. A name that is not a planner id names no
// claim this version wrote, so it is absent, not an error.
func (f *FileClaims) Remove(plannerID string, pid int) error {
	if plannerID == "" {
		return errors.New("empty planner")
	}
	if planner.ValidID(plannerID) != nil {
		return nil
	}

	path := f.path(plannerID)
	raw, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}

	var c Claim
	if err := json.Unmarshal(raw, &c); err != nil {
		// Unparseable: not provably "present with pid == pid", so leave it
		// for a reader's Live to clean up.
		return nil
	}
	if c.PID != pid {
		return nil
	}

	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

// SweepPaneKeyed implements ClaimStore: it removes every file in Root whose
// name is not a valid planner id -- a claim written by a pre-#303 relay mcp,
// keyed by pane -- but only once that file's pid is provably dead. Other
// planner sessions may still be running an older relay mcp during the
// upgrade, and deleting a live claim would make them rewrite it every poll.
// A file with no parseable pid has no dead writer to prove, so it is left.
// It returns how many files it removed; a missing Root is nothing to sweep.
func (f *FileClaims) SweepPaneKeyed() int {
	entries, err := os.ReadDir(f.Root)
	if err != nil {
		return 0
	}

	removed := 0
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || filepath.Ext(name) != ".json" {
			continue
		}
		id := strings.TrimSuffix(name, ".json")
		if planner.ValidID(id) == nil {
			continue // a planner-keyed claim: Live's business, not this sweep's
		}
		raw, err := os.ReadFile(filepath.Join(f.Root, name))
		if err != nil {
			continue
		}
		if !paneKeyedClaimDead(raw, f.alive) {
			continue
		}
		if err := os.Remove(filepath.Join(f.Root, name)); err == nil {
			removed++
		}
	}
	return removed
}

// paneKeyedClaimDead is the sweep's rule, pure so it is tested directly: a
// claim file is removed only when it parses and carries a pid that is not
// alive. Anything else -- unparseable bytes, no pid, a live pid -- is left.
func paneKeyedClaimDead(raw []byte, alive func(pid int) bool) bool {
	var c struct {
		PID int `json:"pid"`
	}
	if err := json.Unmarshal(raw, &c); err != nil {
		return false
	}
	if c.PID <= 0 {
		return false
	}
	return !alive(c.PID)
}
