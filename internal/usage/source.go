package usage

import (
	"context"
	"os"
	"path/filepath"
	"time"
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
	Harness    string // "claude" | "agy" | "opencode"
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
		return r.readStream(src)
	case ModePane:
		return r.readPane(ctx, src)
	}
	return nil, "no reader for mode " + string(src.Mode)
}

func (r reader) readStream(src Source) ([]Sample, string) {
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
	default:
		return nil, "no reader for " + src.Harness
	}
	if len(samples) == 0 {
		return nil, "no usage events"
	}
	return samples, ""
}

func (r reader) readPane(ctx context.Context, src Source) ([]Sample, string) {
	switch src.Harness {
	case "agy":
		return nil, "agy keeps no usage record"
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
