package relay

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// ClaimTTL is how stale a claim's SeenAt may be before it is treated as dead.
const ClaimTTL = 10 * time.Second

// Claim is one relay mcp process's hold on a planner pane's mailbox (spec
// docs/specs/2026-09-21-planner-channel-design.md §3.2).
type Claim struct {
	Pane      string    `json:"pane"`       // required; the herdr pane id, e.g. "wG:pQ"
	PID       int       `json:"pid"`        // required; > 0
	StartedAt time.Time `json:"started_at"` // required
	SeenAt    time.Time `json:"seen_at"`    // required; refreshed each poll
	CWD       string    `json:"cwd"`        // informational
	Version   string    `json:"version"`    // relay version that wrote it; informational
}

// ErrClaimHeld reports that a different live claim already exists for a pane.
var ErrClaimHeld = errors.New("pane already has a live channel")

// ClaimStore is what the daemon and relay mcp share. A nil ClaimStore on a
// Runtime means "no claims exist"; DeliverPending's guard treats it that way.
type ClaimStore interface {
	// Live returns the claim for pane if it is live: parses, pid alive,
	// now - SeenAt <= ClaimTTL. A file that exists but is not live is
	// removed and (nil, nil) returned. Missing file -> (nil, nil).
	Live(pane string, now time.Time) (*Claim, error)
	// Write persists c (atomic: temp file + rename). Returns ErrClaimHeld
	// when a different live claim (other PID) exists for c.Pane.
	Write(c Claim, now time.Time) error
	// Remove deletes the claim file for pane if its PID equals pid. No
	// error when absent.
	Remove(pane string, pid int) error
}

// FileClaims is the on-disk ClaimStore: one JSON file per pane under Root.
type FileClaims struct {
	Root string
	// Alive reports whether pid names a live process. Nil uses the
	// default: syscall.Kill(pid, 0) == nil || the error is EPERM (a
	// process we cannot signal is still alive).
	Alive func(pid int) bool
}

// ClaimFileName turns a pane id into the claim file's base name: ":"
// becomes "_", plus the ".json" extension.
func ClaimFileName(pane string) string {
	return strings.ReplaceAll(pane, ":", "_") + ".json"
}

func (f *FileClaims) alive(pid int) bool {
	if f.Alive != nil {
		return f.Alive(pid)
	}
	return defaultClaimAlive(pid)
}

func (f *FileClaims) path(pane string) string {
	return filepath.Join(f.Root, ClaimFileName(pane))
}

// Live implements ClaimStore.
func (f *FileClaims) Live(pane string, now time.Time) (*Claim, error) {
	if pane == "" {
		return nil, errors.New("empty pane")
	}

	path := f.path(pane)
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
	if c.Pane == "" {
		return errors.New("empty pane")
	}

	existing, err := f.Live(c.Pane, now)
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
	if err := os.Rename(tmpName, f.path(c.Pane)); err != nil {
		os.Remove(tmpName)
		return err
	}

	return nil
}

// Remove implements ClaimStore.
func (f *FileClaims) Remove(pane string, pid int) error {
	if pane == "" {
		return errors.New("empty pane")
	}

	path := f.path(pane)
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
