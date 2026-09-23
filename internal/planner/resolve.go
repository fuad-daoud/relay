package planner

import (
	"errors"
	"time"
)

// Resolution says which step of Resolve's order produced a record (§4.3), for
// the debug line "planner=<id> via=<resolution>" (§6.2).
type Resolution string

const (
	ResolutionFlag    Resolution = "flag"
	ResolutionEnv     Resolution = "env"
	ResolutionHost    Resolution = "host"
	ResolutionSession Resolution = "session"
)

// ResolveInput is everything Resolve may look at: the flag the caller was
// given, the environment, the caller's parent pid, and how to read a process's
// start time. Nothing is read from the process itself, so the order is
// table-tested.
type ResolveInput struct {
	// Flag is --planner's value, empty when it was not given.
	Flag string
	// Env is os.Getenv in production. Nil reads as "no environment".
	Env func(string) string
	// PPID is os.Getppid(), Detect's fallback host pid.
	PPID int
	// ProcStart reads a process's start time in Unix seconds (spec §4.2's
	// ProcStart). A nil ProcStart, or an error from it, means the host step
	// cannot run -- which is not an error, just a fall-through.
	ProcStart func(pid int) (int64, error)
	// Now stamps the seen_at of whatever Resolve hits.
	Now time.Time
}

// Resolve is how every verb except `init` gets its planner: flag > env > host >
// session, first hit wins (§4.3).
//
// It never creates a record. Only `relevo planner init` does, so a missing hook
// is loud -- ErrNoPlanner with the fix in its text -- rather than a silently
// unnamed planner.
func Resolve(reg Registry, in ResolveInput) (Record, Resolution, error) {
	env := in.Env
	if env == nil {
		env = func(string) string { return "" }
	}

	if in.Flag != "" {
		rec, err := lookupRef(reg, in.Flag)
		if err != nil {
			return Record{}, "", err
		}
		return hit(reg, rec, ResolutionFlag, in.Now)
	}

	if v := env("RELEVO_PLANNER"); v != "" {
		rec, err := lookupRef(reg, v)
		if err != nil {
			return Record{}, "", err
		}
		return hit(reg, rec, ResolutionEnv, in.Now)
	}

	ident, detected := Detect(env, in.PPID)

	// The host step covers `relevo mcp` and a session whose hook could not
	// export RELEVO_PLANNER. A ProcStart error means "no host match" -- the ps
	// read failed, so there is nothing to compare -- and falls through to the
	// session step rather than failing the caller.
	if detected && ident.HostPID > 0 && in.ProcStart != nil {
		if startedAt, err := in.ProcStart(ident.HostPID); err == nil {
			rec, err := reg.ByHost(ident.HostPID, startedAt)
			switch {
			case err == nil:
				return hit(reg, rec, ResolutionHost, in.Now)
			case errors.Is(err, ErrNotFound):
			default:
				return Record{}, "", err
			}
		}
	}

	if detected {
		rec, err := reg.BySession(ident.Kind, ident.SessionID)
		switch {
		case err == nil:
			return hit(reg, rec, ResolutionSession, in.Now)
		case errors.Is(err, ErrNotFound):
		default:
			return Record{}, "", err
		}
	}

	return Record{}, "", ErrNoPlanner
}

// lookupRef resolves one flag or environment value: as an id when it has the
// id shape, else as a name (§4.3). A value matching no record is
// ErrUnknownPlanner naming it, which is what tells the reader the value is
// wrong rather than the session unregistered.
func lookupRef(reg Registry, ref string) (Record, error) {
	lookup := reg.ByName
	if ValidID(ref) == nil {
		lookup = reg.Get
	}

	rec, err := lookup(ref)
	if err == nil {
		return rec, nil
	}
	if !errors.Is(err, ErrNotFound) {
		return Record{}, err
	}
	return Record{}, ErrUnknownPlanner{Ref: ref}
}

// hit is the shared tail of every resolved step: refresh seen_at and report
// how the record was found. Touch is best effort by contract -- a resolution
// must not fail because a timestamp could not be written -- so its error is
// deliberately dropped.
func hit(reg Registry, rec Record, res Resolution, now time.Time) (Record, Resolution, error) {
	_ = reg.Touch(rec.ID, now)
	return rec, res, nil
}
