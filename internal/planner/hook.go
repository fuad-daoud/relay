package planner

import (
	"crypto/rand"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/fuad-daoud/relevo/internal/db"
)

// HookInput is the Claude Code SessionStart payload relevo reads from stdin
// (§3.4). Unknown fields are ignored: the hook's payload is Claude Code's to
// extend, and a field relevo does not know must not fail a session.
type HookInput struct {
	HookEventName  string `json:"hook_event_name"`
	Source         string `json:"source"`
	SessionID      string `json:"session_id"`
	TranscriptPath string `json:"transcript_path"`
	CWD            string `json:"cwd"`
}

// The SessionStart sources Claude Code reports (§3.4). `compact` is included
// because the payload can carry it; every other value reads as startup.
const (
	SourceStartup = "startup"
	SourceResume  = "resume"
	SourceClear   = "clear"
	SourceCompact = "compact"
)

// ParseHookInput decodes one SessionStart payload. session_id and cwd are
// required -- registration cannot happen without either -- and an unknown
// source reads as "startup" (§3.4).
func ParseHookInput(r io.Reader) (HookInput, error) {
	var in HookInput
	if err := json.NewDecoder(r).Decode(&in); err != nil {
		return HookInput{}, fmt.Errorf("planner: read hook payload: %w", err)
	}
	if in.SessionID == "" {
		return HookInput{}, fmt.Errorf("planner: hook payload: session_id is required: %w", ErrInvalid)
	}
	if in.CWD == "" {
		return HookInput{}, fmt.Errorf("planner: hook payload: cwd is required: %w", ErrInvalid)
	}

	switch in.Source {
	case SourceStartup, SourceResume, SourceClear, SourceCompact:
	default:
		in.Source = SourceStartup
	}
	return in, nil
}

// hookSpecificOutput is the envelope both hook answers use (§3.4).
type hookSpecificOutput struct {
	HookEventName     string `json:"hookEventName"`
	AdditionalContext string `json:"additionalContext"`
}

type hookEnvelope struct {
	HookSpecificOutput hookSpecificOutput `json:"hookSpecificOutput"`
}

// hookEventName is the event both answers answer.
const hookEventName = "SessionStart"

// hookContext is the §3.4 sentence naming the planner: the text both hook
// answers carry.
func hookContext(r Record) string {
	return fmt.Sprintf(
		"You are relevo planner %s (%s). RELEVO_PLANNER is set in your shell; pass --planner %s only to act as another planner.",
		r.Name, r.ID, r.Name)
}

// noEnvNote is §3.4's note for a hook that could not export RELEVO_PLANNER.
const noEnvNote = "RELEVO_PLANNER could not be exported ($CLAUDE_ENV_FILE is unset); relevo resolves this session through its host process."

// handoffRules is the "Handing off" section of the shipped architect
// definition (#374), embedded verbatim so one source feeds both the planner
// hook and the architect's own copies. handoff_test.go pins every shipped
// copy to these bytes.
//
//go:embed handoff.md
var handoffRules string

// HookOutput is what `relevo planner init --hook claude` prints on success: the
// JSON envelope that tells the model which planner it is (§3.4), followed by
// the relevo handoff rules (#374), so a planner running any agent -- not only
// one running the shipped `architect` -- receives them. The context text is
// written verbatim from the spec, and the trailing newline because this is
// stdout for a shell to read.
func HookOutput(r Record) []byte {
	return encodeHookContext(hookContext(r) + "\n\n" + handoffRules)
}

// HookOutputNoEnv is HookOutput when $CLAUDE_ENV_FILE is unset (§3.4): the
// same envelope and the same sentence, followed by one space and the note that
// RELEVO_PLANNER could not be exported -- so the model knows relevo falls back
// to resolving this session through its host process -- and then the relevo
// handoff rules (#374), appended exactly as HookOutput appends them.
func HookOutputNoEnv(r Record) []byte {
	return encodeHookContext(hookContext(r) + " " + noEnvNote + "\n\n" + handoffRules)
}

// HookNote is the same envelope carrying a failure note. A hook must never
// block a session, so a failed init answers on stdout exactly as a successful
// one does, with the error in the context instead of the planner's name.
func HookNote(msg string) []byte { return encodeHookContext(msg) }

func encodeHookContext(context string) []byte {
	raw, err := json.Marshal(hookEnvelope{HookSpecificOutput: hookSpecificOutput{
		HookEventName:     hookEventName,
		AdditionalContext: context,
	}})
	if err != nil {
		// The envelope is two strings; Marshal cannot fail on it. A panic
		// here would be worse than an empty answer, so return the bare
		// fallback rather than pretending a note was delivered.
		return []byte("{}\n")
	}
	return append(raw, '\n')
}

// EnvLine is the line `init --hook` appends to $CLAUDE_ENV_FILE: it is what
// puts RELEVO_PLANNER in every later Bash call of the session (§3.4).
func EnvLine(id string) string { return "export RELEVO_PLANNER=" + id + "\n" }

// InitResult is which of §5.1's three outcomes happened.
type InitResult string

const (
	// InitCreated registered a record that did not exist.
	InitCreated InitResult = "created"
	// InitReattached found the caller's record and left it on the same
	// session (a new process for a `--resume`, or a host that moved).
	InitReattached InitResult = "reattached"
	// InitMoved found the caller's record and moved it to a new session
	// (/clear, or a `--resume` that changed the session id).
	InitMoved InitResult = "moved"
)

// InitInput is everything `relevo planner init` knows when it registers. Every
// field is an argument, so the rules are testable without a process, a
// terminal or a database (§5.1).
type InitInput struct {
	Kind           string
	SessionID      string
	TranscriptPath string
	CWD            string
	// Name is --name. Non-empty on an existing record renames it; empty takes
	// DefaultName.
	Name string
	// Agent is $CLAUDE_CODE_AGENT, the preferred base for DefaultName.
	Agent string
	// HostPID is the harness process, 0 when unknown (explicit registration).
	HostPID int
	// HostStartedAt is the host process's start time in Unix seconds, 0 when
	// unknown or when HostPID is 0.
	HostStartedAt int64
	// Now stamps created_at and seen_at.
	Now time.Time
	// PriorID supplies §3.5's "reuse the db row's id": given a (kind,
	// session), the id relevo's database already has for it. Nil means none,
	// and a false return mints a fresh id.
	PriorID func(kind, session string) (string, bool)
}

// Init implements §5.1: register, re-attach or move, and hand back the record
// the caller should export.
//
// Every lookup and write happens inside one registry transaction, so `relevo
// planner init --hook` and a concurrently starting `relevo mcp`, or two hook
// firings, serialise: whichever runs second finds the first's record instead of
// creating a second one for the same host or session.
func Init(reg Registry, in InitInput) (Record, InitResult, error) {
	// A *DBRegistry holds one transaction across the whole sequence; that is
	// the only implementation, and the one the concurrency rule is about.
	if dr, ok := reg.(*DBRegistry); ok {
		if err := dr.ensureImported(); err != nil {
			return Record{}, "", err
		}
		var (
			rec Record
			res InitResult
		)
		err := dr.KV.Tx(func(tx db.KVTx) error {
			var e error
			rec, res, e = initLocked(dr.ops(tx), in)
			return e
		})
		return rec, res, err
	}
	return initLocked(regAdapter{reg}, in)
}

// regOps is the transaction-free surface initLocked drives. kvOps implements
// it over one KVTx; regAdapter adapts any other Registry, one call per step.
type regOps interface {
	byName(name string) (Record, error)
	byHost(pid int, startedAt int64) (Record, error)
	bySession(kind, sessionID string) (Record, error)
	create(r Record) (Record, error)
	moveSession(id, sessionID, transcript string, now time.Time) (Record, error)
	setHost(id string, pid int, startedAt int64) (Record, error)
	rename(id, name string) (Record, error)
	touchForced(id string, now time.Time) (Record, error)
}

// regAdapter runs Init against an arbitrary Registry, one locked call per step.
type regAdapter struct{ Registry }

func (a regAdapter) byName(name string) (Record, error)     { return a.ByName(name) }
func (a regAdapter) bySession(k, s string) (Record, error)  { return a.BySession(k, s) }
func (a regAdapter) create(r Record) (Record, error)        { return a.Create(r) }
func (a regAdapter) rename(id, name string) (Record, error) { return a.Rename(id, name) }

func (a regAdapter) byHost(pid int, startedAt int64) (Record, error) {
	return a.ByHost(pid, startedAt)
}

func (a regAdapter) moveSession(id, sessionID, transcript string, now time.Time) (Record, error) {
	return a.MoveSession(id, sessionID, transcript, now)
}

func (a regAdapter) setHost(id string, pid int, startedAt int64) (Record, error) {
	return a.SetHost(id, pid, startedAt)
}

func (a regAdapter) touchForced(id string, now time.Time) (Record, error) {
	rec, err := a.Get(id)
	if err != nil {
		return Record{}, err
	}
	if err := a.Touch(id, now); err != nil {
		return Record{}, err
	}
	rec.SeenAt = now
	return rec, nil
}

// initLocked is §5.1's body. It assumes the caller holds the registry lock.
func initLocked(reg regOps, in InitInput) (Record, InitResult, error) {
	rec, found, err := findCaller(reg, in)
	if err != nil {
		return Record{}, "", err
	}

	if found {
		return reattach(reg, rec, in)
	}
	return register(reg, in)
}

// findCaller looks for the caller's record: by host first, because that is the
// same harness process across /clear and a rename, then by session, which
// covers `--resume` in a new process. Either miss falls through; any other
// error is real.
func findCaller(reg regOps, in InitInput) (Record, bool, error) {
	if in.HostPID > 0 {
		rec, err := reg.byHost(in.HostPID, in.HostStartedAt)
		switch {
		case err == nil:
			return rec, true, nil
		case errors.Is(err, ErrNotFound):
		default:
			return Record{}, false, fmt.Errorf("planner: init: by host: %w", err)
		}
	}

	rec, err := reg.bySession(in.Kind, in.SessionID)
	switch {
	case err == nil:
		return rec, true, nil
	case errors.Is(err, ErrNotFound):
		return Record{}, false, nil
	default:
		return Record{}, false, fmt.Errorf("planner: init: by session: %w", err)
	}
}

// reattach applies §5.1's found branch: rename when asked, move the session
// when it changed, then point the record at the calling process. A move makes
// the result "moved"; everything else is "reattached".
func reattach(reg regOps, rec Record, in InitInput) (Record, InitResult, error) {
	res := InitReattached

	if in.Name != "" && in.Name != rec.Name {
		updated, err := reg.rename(rec.ID, in.Name)
		if err != nil {
			return Record{}, "", err
		}
		rec = updated
	}

	if rec.SessionID != in.SessionID {
		updated, err := reg.moveSession(rec.ID, in.SessionID, in.TranscriptPath, in.Now)
		if err != nil {
			return Record{}, "", err
		}
		rec = updated
		res = InitMoved
	}

	// An explicit registration (HostPID 0) never clears a host the record
	// already has: `--kind/--session` re-attaches to a running planner, and
	// blanking its process would break the host lookup for the next caller.
	if in.HostPID > 0 && rec.HostPID != in.HostPID {
		updated, err := reg.setHost(rec.ID, in.HostPID, in.HostStartedAt)
		if err != nil {
			return Record{}, "", err
		}
		rec = updated
	}

	return stampSeen(reg, rec, res, in)
}

// register is §5.1's else branch: mint (or reuse, §3.5) an id, name the
// record, and create it.
func register(reg regOps, in InitInput) (Record, InitResult, error) {
	id := ""
	if in.PriorID != nil {
		if prior, ok := in.PriorID(in.Kind, in.SessionID); ok {
			id = prior
		}
	}
	if id == "" {
		minted, err := NewID(rand.Reader)
		if err != nil {
			return Record{}, "", err
		}
		id = minted
	}

	name := in.Name
	if name == "" {
		name = DefaultName(in.Agent, in.Kind, func(candidate string) bool {
			_, err := reg.byName(candidate)
			return err == nil
		})
	}

	created, err := reg.create(Record{
		ID:                id,
		Name:              name,
		HarnessKind:       in.Kind,
		SessionID:         in.SessionID,
		HostPID:           in.HostPID,
		HostStartedAt:     in.HostStartedAt,
		CWD:               in.CWD,
		TranscriptLocator: in.TranscriptPath,
		CreatedAt:         in.Now,
		SeenAt:            in.Now,
	})
	if err != nil {
		return Record{}, "", err
	}
	return stampSeen(reg, created, InitCreated, in)
}

// stampSeen carries init's postcondition -- "its seen_at is now" (§4.4) -- for
// every outcome, without the once-a-minute throttle Touch applies.
func stampSeen(reg regOps, rec Record, res InitResult, in InitInput) (Record, InitResult, error) {
	if in.Now.IsZero() {
		return rec, res, nil
	}
	updated, err := reg.touchForced(rec.ID, in.Now)
	if err != nil {
		return Record{}, "", err
	}
	return updated, res, nil
}
