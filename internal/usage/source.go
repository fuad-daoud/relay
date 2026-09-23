package usage

import (
	"context"
	"io"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/fuad-daoud/relevo/internal/harness"
)

// Mode is how the builder ran.
type Mode string

const (
	// ModePane is history only since #303: the db's history rows carry
	// builder_mode='pane' and readers must still parse it, but nothing runs a
	// pane any more.
	ModePane     Mode = "pane"
	ModeHeadless Mode = "headless"
)

// Source is everything a reader needs to find one round's record. It is
// built by internal/relevo from a binding; this package never sees one.
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
	// Peek is Read without waiting for the record to close (#234): what
	// is on disk now, for a surface that shows a running round. It never
	// blocks past ctx and never errors.
	Peek(ctx context.Context, src Source) (samples []Sample, note string)
}

// reader is a value type; mu and cache are pointers so every copy shares
// them (#234): the cache maps a StreamPath to its parse state, so Peek on
// every ui tick parses the appended bytes only. Not persisted.
type reader struct {
	mu    *sync.Mutex
	cache map[string]*streamCache
}

// New returns the production reader.
func New() Reader {
	return reader{mu: &sync.Mutex{}, cache: map[string]*streamCache{}}
}

func (r reader) Read(ctx context.Context, src Source) ([]Sample, string) {
	switch src.Mode {
	case ModeHeadless:
		return r.readStream(ctx, src, true)
	}
	return nil, "no reader for mode " + string(src.Mode)
}

// Peek is Read without the wait for the exit trailer (#234): a headless
// stream is read as it stands (it may still be open).
func (r reader) Peek(ctx context.Context, src Source) ([]Sample, string) {
	switch src.Mode {
	case ModeHeadless:
		return r.readStream(ctx, src, false)
	}
	return nil, "no reader for mode " + string(src.Mode)
}

// exitTrailer is proc.ExitTrailer: the last line relevo's supervisor
// writes to a headless stream, after the harness has exited. Copied, not
// imported, so this package stays free of relevo's process model;
// TestExitTrailerMatchesProc in internal/relevo pins the two equal.
const exitTrailer = "relevo-exit:"

// legacyExitTrailer is legacy.ExitTrailer: the same line a stream written
// before the rename ends in (#292 §1). Copied rather than imported, exactly
// as exitTrailer is, so this package keeps its independence from relevo's
// process model; TestExitTrailerMatchesUsage in internal/proc pins both
// copies equal.
const legacyExitTrailer = "relay-exit:"

// ExitTrailerForTest exposes exitTrailer so internal/relevo can pin it to
// proc.ExitTrailer; nothing else calls it.
func ExitTrailerForTest() string { return exitTrailer }

// LegacyExitTrailerForTest exposes legacyExitTrailer so internal/proc can
// pin it to legacy.ExitTrailer; nothing else calls it.
func LegacyExitTrailerForTest() string { return legacyExitTrailer }

// trailerPoll is how often a still-open stream is re-checked.
const trailerPoll = 200 * time.Millisecond

// tailProbe is how much of the file's end is read to find the last line.
const tailProbe = 256

// streamClosed reports whether path's last non-empty line is the exit
// trailer -- the harness has exited and its final event is on disk. A stream
// written before the rename ends in the relay-exit: form instead, which
// closes the same way (#292 §1).
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
	return strings.HasPrefix(last, exitTrailer) || strings.HasPrefix(last, legacyExitTrailer)
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

func (r reader) readStream(ctx context.Context, src Source, wait bool) ([]Sample, string) {
	if _, err := os.Stat(src.StreamPath); err != nil {
		return nil, "no stream"
	}
	closed := false
	if wait {
		closed = waitClosed(ctx, src.StreamPath)
	} else {
		closed = streamClosed(src.StreamPath)
	}
	samples, note := r.parseCached(src)
	if note != "" {
		return nil, note // "no reader for <harness>"
	}
	switch {
	case len(samples) == 0:
		return nil, "no usage events"
	case !closed:
		return samples, "stream still open"
	}
	return samples, ""
}

// parseCached reads src's stream through the per-stream cache (#234):
// stat first; an unchanged (size, mtime) is answered from the carry
// without opening the file, an appended file is parsed from where the
// last read stopped, and a file that shrank (truncated, or a new round
// reusing the path) resets the entry.
func (r reader) parseCached(src Source) ([]Sample, string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	info, err := os.Stat(src.StreamPath)
	if err != nil {
		return nil, "no stream"
	}
	// Keyed by path and harness: a different harness reading the same
	// path must never pick up the previous harness's carry (a round's
	// stream belongs to one builder, but a test -- and a mid-round
	// builder switch -- can point two harnesses at one path).
	key := src.StreamPath + "\x00" + src.Harness
	e := r.cache[key]
	if e == nil || info.Size() < e.offset {
		model := src.Model
		if src.Harness == "codex" {
			if id, _, err := harness.SplitEffort(src.Model); err == nil {
				model = id
			}
		}
		c, ok := newCarry(src.Harness, src.Provider, model)
		if !ok {
			return nil, "no reader for " + src.Harness
		}
		e = &streamCache{carry: c}
		r.cache[key] = e
	}
	if info.Size() == e.size && info.ModTime().Equal(e.mtime) {
		return e.carry.samples(), ""
	}
	f, err := os.Open(src.StreamPath)
	if err != nil {
		return nil, "no stream"
	}
	defer f.Close()
	if _, err := f.Seek(e.offset, io.SeekStart); err != nil {
		return nil, "no stream"
	}
	read, err := io.ReadAll(f)
	if err != nil {
		return nil, "no stream"
	}
	buf := append(e.tail, read...)
	last := 0
	for i, b := range buf {
		if b != '\n' {
			continue
		}
		line := buf[last:i]
		last = i + 1
		if len(line) > 0 && line[0] == '{' && len(line) <= maxLine {
			e.carry.feed(line)
		}
	}
	e.tail = append([]byte(nil), buf[last:]...)
	if len(e.tail) > maxLine {
		// A pathological line: dropped, as scanLines drops one today.
		e.tail = nil
	}
	// offset is the bytes of the file already read from disk, tail bytes
	// included: the next parse seeks here, so the held tail is never
	// re-read, and buf above (tail + new bytes) is the file's own
	// sequence. (Reading from size-len(tail) instead would re-read the
	// tail and double it inside buf.)
	e.offset = info.Size()
	e.size, e.mtime = info.Size(), info.ModTime()
	return e.carry.samples(), ""
}
