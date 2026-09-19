package relay

import (
	"errors"
	"log/slog"
	"os"
	"time"

	"github.com/fuad-daoud/relay/internal/candidate"
	"github.com/fuad-daoud/relay/internal/classify"
	"github.com/fuad-daoud/relay/internal/policy"
)

// ConfigPaths names the files the daemon reloads and what Classify
// resolution needs. Composed by cmd/relay from userConfigRoot(); nothing in
// internal/relay builds a config path itself (CLAUDE.md).
type ConfigPaths struct {
	Candidates string              // .../relay/candidates.json
	Policy     string              // .../relay/policy.json
	ConfigDir  string              // the dir classify.Resolve takes
	Getenv     func(string) string // os.Getenv in production
}

// fileStamp is what changes when a file is edited: mtime and size. A
// missing file has a zero stamp and exists == false.
type fileStamp struct {
	mtime  time.Time
	size   int64
	exists bool
}

// ConfigWatcher reloads candidates.json and policy.json when they change.
type ConfigWatcher struct {
	paths    ConfigPaths
	cand     fileStamp // stamps at the last SUCCESSFUL load
	pol      fileStamp
	lastErr  string // last warning logged; "" after a good load
	loaded   bool   // false until the first successful load through Refresh
	loadedAt time.Time

	// seams; production values set by NewConfigWatcher, tests replace them
	stat           func(path string) (fileStamp, error)
	loadCandidates func(path string) (*candidate.Set, error)
	loadPolicy     func(path string) (policy.Policy, error)
	resolve        func(cfg *policy.Classify, configDir string, getenv func(string) string) classify.Classifier
	warn           func(msg string, args ...any) // slog.Warn
}

// NewConfigWatcher returns a watcher with production seams: os.Stat,
// candidate.Load, policy.Load, classify.Resolve (first return only),
// slog.Warn. It does not read anything yet.
func NewConfigWatcher(paths ConfigPaths) *ConfigWatcher {
	return &ConfigWatcher{
		paths:          paths,
		stat:           defaultStat,
		loadCandidates: candidate.Load,
		loadPolicy:     policy.Load,
		resolve: func(cfg *policy.Classify, configDir string, getenv func(string) string) classify.Classifier {
			cls, _ := classify.Resolve(cfg, configDir, getenv)
			return cls
		},
		warn: slog.Warn,
	}
}

func defaultStat(path string) (fileStamp, error) {
	fi, err := os.Stat(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return fileStamp{}, nil
		}
		return fileStamp{}, err
	}
	return fileStamp{
		mtime:  fi.ModTime(),
		size:   fi.Size(),
		exists: true,
	}, nil
}

// Refresh returns rt with Candidates, Policy and Classify replaced from disk
// when either file's stamp differs from the stamp at the last successful
// load, or when no load has happened yet through this watcher. Unchanged
// stamps -> rt returned as given (no load). Both files are always loaded
// together: policy validation is independent of candidates, but a switch
// that sees a new order with old candidates is the bug this fixes.
//
// Failure: a stat error other than not-exist, or a load error, keeps rt's
// current values, does NOT update the stamps (so the next tick retries),
// and calls warn once per distinct error string: "config reload: <err>
// (keeping the copy loaded at HH:MM:SS)" -- the time is rt.Now() of the
// last good load, or "startup" when this watcher never loaded. A missing
// file is not an error: candidate.Load / policy.Load already treat absence
// as empty, and the stamp records exists == false.
//
// Postconditions on success: stamps updated, lastErr = "", loaded = true.
func (w *ConfigWatcher) Refresh(rt Runtime) Runtime {
	cs, errC := w.stat(w.paths.Candidates)
	ps, errP := w.stat(w.paths.Policy)
	if errC != nil {
		return w.fail(rt, errC)
	}
	if errP != nil {
		return w.fail(rt, errP)
	}
	if w.loaded && cs == w.cand && ps == w.pol {
		return rt
	}
	cands, err := w.loadCandidates(w.paths.Candidates)
	if err != nil {
		return w.fail(rt, err)
	}
	pol, err := w.loadPolicy(w.paths.Policy)
	if err != nil {
		return w.fail(rt, err)
	}
	rt.Candidates = cands
	rt.Policy = pol
	if w.resolve != nil {
		rt.Classify = w.resolve(pol.Classify, w.paths.ConfigDir, w.paths.Getenv)
	}
	w.cand = cs
	w.pol = ps
	w.loaded = true
	w.lastErr = ""
	if rt.Now != nil {
		w.loadedAt = rt.Now()
	} else {
		w.loadedAt = time.Now()
	}
	return rt
}

func (w *ConfigWatcher) fail(rt Runtime, err error) Runtime {
	msg := err.Error()
	if msg != w.lastErr {
		when := "startup"
		if w.loaded {
			when = w.loadedAt.Local().Format("15:04:05")
		}
		if w.warn != nil {
			w.warn("config reload failed; keeping the copy loaded at "+when, "err", err)
		}
		w.lastErr = msg
	}
	return rt
}
