package harness

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"
	"sort"
)

// InstallEnv defines the filesystem and PATH seams required by Install.
type InstallEnv interface {
	// LookPath looks up binary on PATH. Any error means not on PATH.
	LookPath(binary string) (string, error)
	// HomePath joins rel under the user's home directory.
	// An error aborts the install because nothing can be written.
	HomePath(rel string) (string, error)
	// ReadFile reads the named file. When absent, it returns an error satisfying errors.Is(err, fs.ErrNotExist).
	// Any other error is treated as "present, differs".
	ReadFile(path string) ([]byte, error)
	// MkdirAll creates dir and parents, with permission 0755.
	MkdirAll(dir string) error
	// WriteFile writes whole file with permission 0644, truncating if it exists.
	WriteFile(path string, data []byte) error
}

// InstallOptions controls the behavior of Install.
type InstallOptions struct {
	Kind   string
	Role   string
	Force  bool
	DryRun bool
}

// InstallOutcome represents the outcome of an install action for a single role.
type InstallOutcome string

const (
	OutcomeWrote          InstallOutcome = "wrote"
	OutcomeOverwrote      InstallOutcome = "overwrote"
	OutcomeKeptIdentical  InstallOutcome = "kept (identical)"
	OutcomeKeptDiffers    InstallOutcome = "kept (differs; --force to overwrite)"
	OutcomeWouldWrite     InstallOutcome = "would write"
	OutcomeWouldOverwrite InstallOutcome = "would overwrite"
	OutcomeError          InstallOutcome = "error"
)

// InstallResult describes the outcome of installing one role definition.
type InstallResult struct {
	Kind    string
	Role    string
	Path    string
	Outcome InstallOutcome
	Err     string
}

// ErrUnknownKind is returned when the requested harness kind is unknown.
var ErrUnknownKind = errors.New("unknown harness kind")

// ErrUnknownRole is returned when the requested role is not shipped by any candidate harness.
var ErrUnknownRole = errors.New("unknown role")

// DocEqual reports whether shipped and installed definitions are equal after trimming trailing whitespace (" \t\r\n").
func DocEqual(shipped, installed []byte) bool {
	return bytes.Equal(bytes.TrimRight(shipped, " \t\r\n"), bytes.TrimRight(installed, " \t\r\n"))
}

// Install decides, per (kind, role), whether the shipped definition lands on disk, and lands it.
// Results are ordered by harness.All() (sorted by kind), and roles by Harness.Roles table order.
// Per-file failures are reported as error outcomes, never as returned errors.
func Install(env InstallEnv, opts InstallOptions) ([]InstallResult, error) {
	var kinds []Harness
	if opts.Kind != "" {
		h, ok := Lookup(opts.Kind)
		if !ok {
			return nil, fmt.Errorf("%w: %q", ErrUnknownKind, opts.Kind)
		}
		kinds = []Harness{h}
	} else {
		for _, h := range All() {
			if _, err := env.LookPath(h.Binary); err == nil {
				kinds = append(kinds, h)
			}
		}
	}

	if opts.Role != "" {
		candidates := kinds
		if len(candidates) == 0 {
			candidates = All()
		}
		found := false
		for _, c := range candidates {
			if _, ok := c.Role(opts.Role); ok {
				found = true
				break
			}
		}
		if !found {
			knownMap := make(map[string]struct{})
			for _, c := range candidates {
				for _, name := range c.RoleNames() {
					knownMap[name] = struct{}{}
				}
			}
			known := make([]string, 0, len(knownMap))
			for name := range knownMap {
				known = append(known, name)
			}
			sort.Strings(known)
			return nil, fmt.Errorf("%w: %q (known: %v)", ErrUnknownRole, opts.Role, known)
		}
	}

	var results []InstallResult
	for _, h := range kinds {
		for _, r := range h.Roles {
			if opts.Role != "" && r.Name != opts.Role {
				continue
			}
			res, err := installOne(env, opts, h.Kind, r)
			if err != nil {
				return nil, err
			}
			results = append(results, res)
		}
	}
	return results, nil
}

func installOne(env InstallEnv, opts InstallOptions, kind string, r Role) (InstallResult, error) {
	shipped, err := AgentDoc(r.Name, kind)
	if err != nil {
		return InstallResult{
			Kind:    kind,
			Role:    r.Name,
			Path:    r.Path,
			Outcome: OutcomeError,
			Err:     err.Error(),
		}, nil
	}
	full, err := env.HomePath(r.Path)
	if err != nil {
		return InstallResult{}, err
	}
	res := InstallResult{
		Kind: kind,
		Role: r.Name,
		Path: r.Path,
	}
	existing, rerr := env.ReadFile(full)
	switch {
	case errors.Is(rerr, fs.ErrNotExist):
		if opts.DryRun {
			res.Outcome = OutcomeWouldWrite
		} else {
			res = writeDoc(env, full, shipped, res, OutcomeWrote)
		}
	case rerr == nil && DocEqual(shipped, existing):
		res.Outcome = OutcomeKeptIdentical
	default:
		if !opts.Force {
			res.Outcome = OutcomeKeptDiffers
		} else {
			if opts.DryRun {
				res.Outcome = OutcomeWouldOverwrite
			} else {
				res = writeDoc(env, full, shipped, res, OutcomeOverwrote)
			}
		}
	}
	return res, nil
}

func writeDoc(env InstallEnv, full string, data []byte, res InstallResult, success InstallOutcome) InstallResult {
	if err := env.MkdirAll(filepath.Dir(full)); err != nil {
		res.Outcome = OutcomeError
		res.Err = err.Error()
		return res
	}
	if err := env.WriteFile(full, data); err != nil {
		res.Outcome = OutcomeError
		res.Err = err.Error()
		return res
	}
	res.Outcome = success
	return res
}
