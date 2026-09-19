package usage

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/fuad-daoud/relay/internal/harness"
)

// Mode is how the builder ran.
type Mode string

const (
	ModePane     Mode = "pane"
	ModeHeadless Mode = "headless"
)

// Source is everything a reader needs to find one round's record. It is
// built by internal/relay from a binding; this package never sees one.
type Source struct {
	Harness    string // "claude" | "agy" | "opencode" | "codex"
	Mode       Mode
	Provider   string // the candidate's; "" for an adopted builder
	Model      string // the candidate's; "" for an adopted builder
	Plan       bool   // the candidate's subscription flag
	StreamPath string // headless: the round's NNN-builder.jsonl
	Worktree   string // pane: the binding's worktree; "" for a --cwd binding
	Start, End time.Time
}

// Reader turns a Source into samples. It never errors: an unreadable
// source is zero samples and a note saying why, and the round closes
// regardless.
type Reader interface {
	Read(ctx context.Context, src Source) (samples []Sample, note string)
}

type reader struct {
	exec Exec
	home string
}

// New returns the production reader. exec may be nil (sqlite3 absent);
// home is the user's home directory.
func New(exec Exec, home string) Reader { return reader{exec: exec, home: home} }

func (r reader) Read(ctx context.Context, src Source) ([]Sample, string) {
	switch src.Mode {
	case ModeHeadless:
		return r.readStream(ctx, src)
	case ModePane:
		return r.readPane(ctx, src)
	}
	return nil, "no reader for mode " + string(src.Mode)
}

// exitTrailer is proc.ExitTrailer: the last line relay's supervisor
// writes to a headless stream, after the harness has exited. Copied, not
// imported, so this package stays free of relay's process model;
// TestExitTrailerMatchesProc in internal/relay pins the two equal.
const exitTrailer = "relay-exit:"

// ExitTrailerForTest exposes exitTrailer so internal/relay can pin it to
// proc.ExitTrailer; nothing else calls it.
func ExitTrailerForTest() string { return exitTrailer }

// trailerPoll is how often a still-open stream is re-checked.
const trailerPoll = 200 * time.Millisecond

// tailProbe is how much of the file's end is read to find the last line.
const tailProbe = 256

// streamClosed reports whether path's last non-empty line is the exit
// trailer -- the harness has exited and its final event is on disk.
func streamClosed(path string) bool {
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return false
	}
	size := info.Size()
	n := int64(tailProbe)
	if size < n {
		n = size
	}
	buf := make([]byte, n)
	if _, err := f.ReadAt(buf, size-n); err != nil && err != io.EOF {
		return false
	}
	lines := strings.Split(strings.TrimRight(string(buf), "\n"), "\n")
	last := lines[len(lines)-1]
	return strings.HasPrefix(last, exitTrailer)
}

// waitClosed blocks until the stream is closed or ctx is done, and
// reports which. A round closes on the builder's done marker, which the
// harness writes before its final event: the wait is what turns a race
// into a measured figure.
func waitClosed(ctx context.Context, path string) bool {
	for {
		if streamClosed(path) {
			return true
		}
		select {
		case <-ctx.Done():
			return false
		case <-time.After(trailerPoll):
		}
	}
}

func (r reader) readStream(ctx context.Context, src Source) ([]Sample, string) {
	if _, err := os.Stat(src.StreamPath); err != nil {
		return nil, "no stream"
	}
	closed := waitClosed(ctx, src.StreamPath)
	f, err := os.Open(src.StreamPath)
	if err != nil {
		return nil, "no stream"
	}
	defer f.Close()
	var samples []Sample
	switch src.Harness {
	case "claude":
		samples = claudeStream(f, src.Provider)
	case "agy":
		samples = agyStream(f, src.Provider, src.Model)
	case "opencode":
		samples = opencodeStream(f, src.Provider, src.Model)
	case "codex":
		model := src.Model
		if id, _, err := harness.SplitEffort(src.Model); err == nil {
			model = id
		}
		samples = codexStream(f, src.Provider, model)
	default:
		return nil, "no reader for " + src.Harness
	}
	switch {
	case len(samples) == 0:
		return nil, "no usage events"
	case !closed:
		return samples, "stream still open"
	}
	return samples, ""
}

func (r reader) readPane(ctx context.Context, src Source) ([]Sample, string) {
	switch src.Harness {
	case "agy":
		return nil, "agy keeps no usage record"
	case "codex":
		return nil, "codex pane usage not read"
	case "claude", "opencode":
	default:
		return nil, "no reader for " + src.Harness
	}
	if src.Worktree == "" {
		return nil, "shared cwd"
	}
	switch src.Harness {
	case "claude":
		dir := filepath.Join(r.home, ".claude", "projects", ProjectSlug(src.Worktree))
		if info, err := os.Stat(dir); err != nil || !info.IsDir() {
			return nil, "no claude project dir"
		}
		samples := claudeProject(os.DirFS(dir), src.Worktree, src.Start, src.End, src.Provider)
		if len(samples) == 0 {
			return nil, "no usage events"
		}
		return samples, ""
	default: // opencode
		db := filepath.Join(r.home, ".local", "share", "opencode", "opencode.db")
		return opencodeDB(ctx, r.exec, db, src.Worktree, src.Start, src.End)
	}
}
