package relevo

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"

	"github.com/fuad-daoud/relevo/internal/remote"
	"github.com/fuad-daoud/relevo/internal/store"
)

// remoteFetch is what the fetch half read from the server for one binding,
// without the state lock, for the apply half to act on under it.
type remoteFetch struct {
	Name   string             // b.Name at snapshot
	Server string             // b.Builder.Server at snapshot
	Round  int                // b.Round at snapshot
	View   remote.BindingView // GetBinding's result; zero when Err != nil
	Err    error              // GetBinding's error, classified by the apply half exactly as today
	Log    *logMirror         // nil: nothing fetched to write (not running, legacy log, error, no growth)
	Legacy bool               // the round's log is the legacy file form: apply runs the legacy mirror inline
	Drift  []byte             // nil: nothing fetched
}

// logMirror is one fetched update to the round's mirrored builder log row.
type logMirror struct {
	Path string // rt.Store.BuilderLogPath(name, round)
	Base int64  // local length the range read started from; the apply half writes only if the
	// row still has exactly this length. -1 means Body is a full replacement.
	Body []byte // bytes to append (Base >= 0) or the whole log (Base == -1); never empty when Log != nil
}

// fetchRemote reads everything the next apply needs from the server for one
// remote binding, without touching the state lock. A failed read leaves that
// field empty and logs exactly as the inline mirror paths did, so the apply
// half decides on a best-effort snapshot and never fails the tick on it.
func fetchRemote(ctx context.Context, rt Runtime, b store.Binding) remoteFetch {
	f := remoteFetch{Name: b.Name, Server: b.Builder.Server, Round: b.Round}
	if rt.Remote == nil {
		return f
	}

	view, err := rt.Remote.GetBinding(ctx, f.Server, f.Name)
	if err != nil {
		f.Err = err
		return f
	}
	f.View = view
	if view.RoundState != remote.RoundRunning {
		return f
	}

	f.Log, f.Legacy = fetchLogMirror(ctx, rt, f.Server, f.Name, f.Round)
	f.Drift = fetchDrift(ctx, rt, f.Server, f.Name, f.Round)
	return f
}

// fetchLogMirror is the network half of the DB-branch builder-log mirror: it
// reads the row's current length, range-reads the bytes the server holds past
// it, and returns the update the apply half should write. legacy reports that
// the round keeps its log in the old on-disk form, which the apply half mirrors
// inline instead.
func fetchLogMirror(ctx context.Context, rt Runtime, server, name string, round int) (*logMirror, bool) {
	path := rt.Store.BuilderLogPath(name, round)
	if legacyLog(rt, name, round) {
		return nil, true
	}

	cur, err := rt.Store.ReadFile(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			cur = nil
		} else {
			slog.Warn("mirror builder log failed", "server", server, "name", name, "round", round, "err", err)
			return nil, false
		}
	}
	local := int64(len(cur))

	rc, fr, err := rt.Remote.RoundFileFrom(ctx, server, name, round, "log", local)
	if err != nil || rc == nil {
		if err != nil {
			slog.Warn("mirror builder log failed", "server", server, "name", name, "round", round, "err", err)
		}
		return nil, false
	}

	readBounded := func(r io.Reader) ([]byte, bool) {
		body, err := io.ReadAll(io.LimitReader(r, maxMirrorBytes+1))
		if err != nil {
			slog.Warn("mirror builder log failed", "server", server, "name", name, "round", round, "err", err)
			return nil, false
		}
		if int64(len(body)) > maxMirrorBytes {
			slog.Warn("mirror builder log over cap; not stored", "server", server, "name", name, "round", round)
			return nil, false
		}
		return body, true
	}

	switch {
	case fr.Honored && fr.From == local && fr.Size >= local:
		defer rc.Close()
		body, ok := readBounded(rc)
		// An empty body means the row did not grow: rewriting it with the
		// same bytes would put the whole blob back for no change.
		if !ok || len(body) == 0 {
			return nil, false
		}
		if int64(len(cur))+int64(len(body)) > maxMirrorBytes {
			slog.Warn("mirror builder log over cap; not stored", "server", server, "name", name, "round", round)
			return nil, false
		}
		return &logMirror{Path: path, Base: local, Body: body}, false
	case fr.Honored && fr.Size < local:
		_ = rc.Close()
		rc2, _, err := rt.Remote.RoundFileFrom(ctx, server, name, round, "log", 0)
		if err != nil || rc2 == nil {
			if err != nil {
				slog.Warn("mirror builder log failed", "server", server, "name", name, "round", round, "err", err)
			}
			return nil, false
		}
		defer rc2.Close()
		body, ok := readBounded(rc2)
		if !ok {
			return nil, false
		}
		return &logMirror{Path: path, Base: -1, Body: body}, false
	default:
		defer rc.Close()
		body, ok := readBounded(rc)
		if !ok {
			return nil, false
		}
		return &logMirror{Path: path, Base: -1, Body: body}, false
	}
}

// fetchDrift is the network half of the drift mirror: it returns the round's
// drift bytes, or nil when there is nothing to fetch. Its guards match the
// inline version -- an already stored drift is not re-read, and only the first
// attempt of a round reaches the server.
func fetchDrift(ctx context.Context, rt Runtime, server, name string, round int) []byte {
	driftPath := rt.Store.DriftPath(name, round)
	if _, err := rt.Store.ReadFile(driftPath); err == nil {
		return nil
	}
	key := fmt.Sprintf("%s/%d", name, round)
	if _, loaded := mirrorDriftAttempts.LoadOrStore(key, struct{}{}); loaded {
		return nil
	}
	rc, err := rt.Remote.RoundFile(ctx, server, name, round, "drift")
	if err != nil || rc == nil {
		if err != nil {
			slog.Debug("mirror drift not available", "server", server, "name", name, "round", round, "err", err)
		}
		return nil
	}
	defer rc.Close()
	data, err := io.ReadAll(rc)
	if err != nil {
		slog.Warn("mirror drift read failed", "server", server, "name", name, "round", round, "err", err)
		return nil
	}
	return data
}

// matches reports whether b under the lock is still the binding f was fetched
// for: same identity and round, still remote, and in a known live state. A
// false answer means the fetch is stale and the apply must change nothing.
func (f remoteFetch) matches(b store.Binding) bool {
	return b.Name == f.Name &&
		b.Builder.Remote() &&
		b.Builder.Server == f.Server &&
		b.Round == f.Round &&
		b.State != store.StateDone &&
		b.State != store.StatePaused &&
		store.KnownState(b.State)
}

// applyLogMirror writes one fetched log update under tx. A Base of -1 is a full
// replacement; otherwise the row must still hold exactly Base bytes, or the row
// moved since the fetch and the next tick re-reads it.
func applyLogMirror(rt Runtime, tx *store.Tx, name string, round int, m *logMirror) {
	if m == nil {
		return
	}
	if m.Base < 0 {
		if err := tx.PutRoundFile(name, round, m.Path, m.Body); err != nil {
			slog.Warn("mirror builder log failed", "name", name, "round", round, "err", err)
		}
		return
	}

	cur, err := rt.Store.ReadFile(m.Path)
	if err != nil {
		if !errors.Is(err, fs.ErrNotExist) {
			slog.Warn("mirror builder log failed", "name", name, "round", round, "err", err)
			return
		}
		cur = nil
	}
	if int64(len(cur)) != m.Base {
		slog.Debug("mirror builder log row moved since fetch; skipping", "name", name, "round", round)
		return
	}
	full := append(cur, m.Body...)
	if int64(len(full)) > maxMirrorBytes {
		slog.Warn("mirror builder log over cap; not stored", "name", name, "round", round)
		return
	}
	if err := tx.PutRoundFile(name, round, m.Path, full); err != nil {
		slog.Warn("mirror builder log failed", "name", name, "round", round, "err", err)
	}
}

// applyDrift writes one fetched drift under tx; a nil data means the fetch
// found nothing to store.
func applyDrift(rt Runtime, tx *store.Tx, name string, round int, data []byte) {
	if data == nil {
		return
	}
	driftPath := rt.Store.DriftPath(name, round)
	if err := tx.PutRoundFile(name, round, driftPath, data); err != nil {
		slog.Warn("write drift round file failed", "path", driftPath, "err", err)
	}
}
