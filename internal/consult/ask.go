package consult

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/fuad-daoud/relevo/internal/candidate"
	"github.com/fuad-daoud/relevo/internal/harness"
	"github.com/fuad-daoud/relevo/internal/spawn"
	"github.com/fuad-daoud/relevo/internal/store"
)

// ErrNotAConsultRole reports an ask naming an alias that is a builder. A
// builder is persistent and writes; handing one a prompt with no plan in it
// would start a round that never was.
var ErrNotAConsultRole = errors.New("that actor is the builder actor; bind it with relevo bind, not relevo ask")

// ErrTreelessUnsupported reports an ask for a role declaring tree "none".
// No treeless role ships yet, and silently running one in the binding's tree
// would put an agent somewhere its role did not ask for.
var ErrTreelessUnsupported = errors.New("treeless consult actors are not implemented")

// ErrConsultCap reports an ask that would exceed the binding's running-consult
// cap. An idle harness pane holds roughly 800 MB.
var ErrConsultCap = errors.New("binding is at its consult cap")

// Timeout is how long a consult may stay working before relevo gives up on it.
// A consult reads and writes one file; the alternative to a deadline is a
// record that never becomes terminal and holds a cap slot.
const Timeout = 10 * time.Minute

// Request is one consult to reserve, spawn and record.
type Request struct {
	Name     string
	ID       string
	Role     string
	Endpoint store.Endpoint
	Body     []byte

	// Round > 0 is a round consult: Argv is the resume argv the caller already
	// built and validated, and Inline says whether the question was staged
	// inline or as a file. Round 0 spawns a fresh headless process.
	Round  int
	Argv   []string
	Inline bool

	// Candidate, Spec and Tier are the resolved launch inputs of a headless
	// consult (Round == 0).
	Candidate candidate.Candidate
	Spec      harness.RoleSpec
	Tier      harness.Tier

	// Output is the output label the headless prompt asks for -- "plan",
	// "findings", "notes", or a custom agent's own. Empty means
	// DefaultOutput. It is used only when Round == 0: a round consult has
	// its own prompt (roundAskPrompt), which is unchanged.
	Output string

	// Pick builds the candidate-pick log entry for a running headless consult;
	// nil when the consult writes none.
	Pick func(round int) *store.LogEntry
	// AskNote is the ask log entry's Note.
	AskNote string
}

// Spawn reserves, starts and records one consult. It returns the record even on
// a spawn or save failure, so the caller can report what was written; the
// returned consult is zero when the reservation itself failed.
func Spawn(ctx context.Context, d Deps, req Request) (store.Consult, error) {
	var (
		render func(ref string) string
		prompt string
		inline bool
	)
	if req.Round > 0 {
		inline = req.Inline
	} else {
		render = headlessRender(req.Output)
		prompt, inline = inlinePrompt(render, req.Body)
	}

	consult, cwd, owner, err := reserve(d, req, inline)
	if err != nil {
		return consult, err
	}

	streamPath := d.Store.ConsultStreamPath(req.Name, consult.Round, consult.ID)
	spawnErr := start(ctx, d, req, &consult, cwd, owner, streamPath, prompt, inline, render)

	return save(d, req, consult, spawnErr)
}

// reserve is the phase every consult shares: under the lock, load the binding,
// refuse a remote one, a closed one and one at its consult cap, write the
// record, stage the question, and save. roleName is the record's role label and
// endpoint its endpoint minus the fields the spawn fills in. It returns the
// loaded binding's Owner, so the caller needs no second load.
func reserve(d Deps, req Request, inline bool) (store.Consult, string, string, error) {
	var (
		consult store.Consult
		cwd     string
		owner   string
	)
	err := d.Store.WithLock(func(tx *store.Tx) error {
		b, err := tx.Load(req.Name)
		if err != nil {
			return err
		}
		owner = b.Owner
		if b.Builder.Remote() {
			return errors.New("consults are local-only")
		}
		if b.State == store.StateBroken || b.State == store.StateDone || b.State == store.StatePaused {
			return fmt.Errorf("binding %q is %s; a consult needs a live binding to attach to", b.Name, b.State)
		}
		if Running(b) >= consultCap(b) {
			return fmt.Errorf("binding %q has %d running consults (cap %d), wait for one to finish: %w",
				b.Name, Running(b), consultCap(b), ErrConsultCap)
		}

		consult = store.Consult{
			ID:           req.ID,
			Role:         req.Role,
			Round:        b.Round,
			AskPath:      d.Store.AskPath(b.Name, b.Round, req.ID),
			FindingsPath: d.Store.FindingsPath(b.Name, b.Round, req.ID),
			Endpoint:     req.Endpoint,
			State:        store.ConsultSpawning,
			SpawnedAt:    d.Now().UTC(),
		}

		if inline {
			if err := tx.PutRoundFile(b.Name, b.Round, consult.AskPath, req.Body); err != nil {
				return fmt.Errorf("record question at %s: %w", consult.AskPath, err)
			}
		} else if err := os.WriteFile(consult.AskPath, req.Body, 0o644); err != nil {
			return fmt.Errorf("stage question at %s: %w", consult.AskPath, err)
		}

		b.Consults = append(b.Consults, consult)
		if err := tx.Save(b); err != nil {
			return err
		}
		cwd = b.CWD
		return nil
	})
	return consult, cwd, owner, err
}

// start spawns the consult's process and fills its endpoint. A launch or start
// failure leaves the record silent with the failure in its note; argv is used
// as-is for a round consult, whose caller already built it.
func start(ctx context.Context, d Deps, req Request, c *store.Consult, cwd, owner, streamPath, prompt string, inline bool, render func(ref string) string) error {
	argv := req.Argv
	var spawnErr error
	if argv == nil {
		ref := prompt
		if !inline {
			ref = render("Read: " + c.AskPath)
		}
		argv, spawnErr = spawn.HeadlessLaunch(req.Candidate, req.Spec, req.Tier, Timeout, ref, cwd, d.Store.Dir(req.Name))
	}
	if spawnErr != nil {
		c.State = store.ConsultSilent
		c.Note = "spawn failed: " + brief(spawnErr)
		return spawnErr
	}

	// The consult is a process, not a pane: no tab, no agent start, no prompt
	// to type. The stream carries its final message and the exit trailer.
	handle, err := d.Runner.Start(ctx, spawn.ProcSpec{
		Dir:        cwd,
		Argv:       argv,
		LogPath:    streamPath,
		StreamPath: streamPath,
		Scope:      d.Scope("consult", owner, req.Name, c.Round, c.ID),
	})
	if err != nil {
		c.State = store.ConsultSilent
		c.Note = "spawn failed: " + brief(err)
		return fmt.Errorf("start consult %q: %w", c.Endpoint.AgentName, err)
	}
	c.Endpoint = store.Endpoint{
		AgentName: c.Endpoint.AgentName,
		SessionID: c.Endpoint.SessionID,
		Kind:      c.Endpoint.Kind,
		Mode:      store.ModeHeadless,
		PID:       handle.PID,
		StartedAt: handle.StartedAt.Unix(),
		LogPath:   streamPath,
	}
	c.State = store.ConsultRunning
	return nil
}

// save is the record phase every consult shares: upsert the record and, when it
// is running, append its log entries. It combines a spawn failure with a save
// failure, so neither half of what went wrong is lost.
func save(d Deps, req Request, consult store.Consult, spawnErr error) (store.Consult, error) {
	var pick *store.LogEntry
	if consult.State == store.ConsultRunning && req.Pick != nil {
		pick = req.Pick(consult.Round)
	}
	if err := record(d, req.Name, consult, pick, req.AskNote); err != nil {
		if spawnErr != nil {
			return consult, strandError(spawnErr, err)
		}
		if req.Round > 0 {
			return consult, fmt.Errorf("consult %s is running but could not be recorded: %w", consult.ID, err)
		}
		return consult, fmt.Errorf("consult %s is running as pid %d but could not be recorded: %w", consult.ID, consult.Endpoint.PID, err)
	}
	return consult, spawnErr
}

// record upserts the consult and appends its log entries under the lock. pick
// is the candidate-pick entry a consult chosen from a candidate writes; nil for
// the round path, which resumes a named session rather than resolving one.
// askNote is the ask entry's Note, and the only thing that tells the two apart.
func record(d Deps, name string, consult store.Consult, pick *store.LogEntry, askNote string) error {
	return d.Store.WithLock(func(tx *store.Tx) error {
		b, err := tx.Load(name)
		if err != nil {
			return err
		}

		found := false
		for i, existing := range b.Consults {
			if existing.ID == consult.ID {
				b.Consults[i] = consult
				found = true
				break
			}
		}
		if !found {
			b.Consults = append(b.Consults, consult)
		}

		if consult.State == store.ConsultRunning {
			if pick != nil {
				if err := tx.AppendLog(b.Name, *pick); err != nil {
					return err
				}
			}

			entry := store.LogEntry{
				TS:        d.Now().UTC(),
				Round:     consult.Round,
				Direction: store.DirToConsult,
				Kind:      store.KindAsk,
				Path:      consult.AskPath,
				Note:      askNote,
				Confirmed: true,
			}
			if err := tx.AppendLog(b.Name, entry); err != nil {
				return err
			}
		}

		return tx.Save(b)
	})
}

// Running counts ConsultSpawning as well as ConsultRunning. A reservation
// occupies a slot the cap cares about, or two concurrent asks would both see
// it free.
func Running(b store.Binding) int {
	n := 0
	for _, c := range b.Consults {
		if c.State == store.ConsultSpawning || c.State == store.ConsultRunning {
			n++
		}
	}
	return n
}

func consultCap(b store.Binding) int {
	if b.ConsultCap > 0 {
		return b.ConsultCap
	}
	return store.DefaultConsultCap
}

// RandomID returns 8 hex characters. A failed CSPRNG read is not a reason to
// refuse a consult: the id only has to be unique within one binding, and the
// clock is sufficient for that.
func RandomID() string {
	var b [4]byte
	if _, err := rand.Read(b[:]); err != nil {
		return fmt.Sprintf("%08x", uint32(time.Now().UnixNano()))
	}
	return hex.EncodeToString(b[:])
}

// newID returns the caller's id minter, or RandomID when it has none.
func newID(d Deps) string {
	if d.NewID != nil {
		return d.NewID()
	}
	return RandomID()
}

// strandError combines the failure that stranded a consult with any failure to
// record it. Returning only the save error hides the half that explains what
// actually went wrong.
func strandError(cause, saveErr error) error {
	if saveErr != nil {
		return fmt.Errorf("%w (also failed to record the consult: %s)", cause, saveErr.Error())
	}
	return cause
}

// brief reduces an error to one line, for a note field where a multi-line
// message would break the line's shape.
func brief(err error) string {
	if err == nil {
		return ""
	}
	s := strings.TrimSpace(err.Error())
	if idx := strings.Index(s, "\n"); idx != -1 {
		s = strings.TrimSpace(s[:idx])
	}
	return s
}
