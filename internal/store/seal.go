package store

import (
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/fuad-daoud/relevo/internal/db"
	"github.com/fuad-daoud/relevo/internal/legacy"
)

// roundBaseRe matches the basename of a round file: the NNN- prefix every
// file relevo writes for a round carries (plan, report, done, builder logs
// and streams, gate log, question, diff, drift, and a consult's ask, findings
// and streams). It is what ReadFile and StatFile test a path with before
// looking a miss up in round_file (P3c §4.2).
var roundBaseRe = regexp.MustCompile(`^\d{3}-`)

// ReadFile returns path's bytes: from disk when the file is there, and
// otherwise from the sealed round_file row when path names a round file of a
// live binding that a seal pass already moved into the database.
//
// A miss returns os.ReadFile's own error, so a caller's
// errors.Is(err, fs.ErrNotExist) keeps working exactly as it did.
func (s *Store) ReadFile(path string) ([]byte, error) {
	data, err := os.ReadFile(path)
	if err == nil {
		return data, nil
	}
	if !errors.Is(err, fs.ErrNotExist) {
		return nil, err
	}

	d, recordID, base, ok, lerr := s.sealedLookup(path)
	if lerr != nil {
		return nil, lerr
	}
	if !ok {
		return nil, err
	}

	body, _, found, gerr := d.RoundFileGet(recordID, base)
	if gerr != nil {
		return nil, gerr
	}
	if !found {
		return nil, err
	}
	return body, nil
}

// StatFile is ReadFile for os.Stat callers: the file's size and mtime when it
// is on disk, and the sealed row's when it was sealed. ok is false when the
// path is neither on disk nor a sealed round file.
//
// A miss returns os.Stat's own error alongside ok == false, so an err check
// keeps working as it did and an ok check is the cleaner test for a caller
// that only asks whether the file is there.
func (s *Store) StatFile(path string) (size int64, mtime time.Time, ok bool, err error) {
	info, err := os.Stat(path)
	if err == nil {
		return info.Size(), info.ModTime(), true, nil
	}
	if !errors.Is(err, fs.ErrNotExist) {
		return 0, time.Time{}, false, err
	}
	missErr := err

	d, recordID, base, found, lerr := s.sealedLookup(path)
	if lerr != nil {
		return 0, time.Time{}, false, lerr
	}
	if !found {
		return 0, time.Time{}, false, missErr
	}

	body, mt, ok, gerr := d.RoundFileGet(recordID, base)
	if gerr != nil {
		return 0, time.Time{}, false, gerr
	}
	if !ok {
		return 0, time.Time{}, false, missErr
	}
	return int64(len(body)), mt, true, nil
}

// sealedLookup resolves path as <s.root>/<name>/<base>, with base a round
// file's basename, and returns the store's database and the record id whose
// round_file rows hold it: the live record of the binding name, or -- when the
// name has no live record -- the name's most recently archived one, which is
// where archive() put its round files when it freed the name (P3d §4.1).
//
// found is false when the path is not such a path, when the name has neither
// a live nor an archived record, and when the root has no database at all --
// so a miss costs no error and leaves the caller's own ErrNotExist in place.
func (s *Store) sealedLookup(path string) (d *db.DB, recordID, base string, found bool, err error) {
	base = filepath.Base(path)
	if !roundBaseRe.MatchString(base) {
		return nil, "", "", false, nil
	}
	dir := filepath.Dir(path)
	if filepath.Dir(dir) != filepath.Clean(s.root) {
		return nil, "", "", false, nil
	}

	d, err = s.dbForRead()
	if err != nil || d == nil {
		return nil, "", "", false, err
	}
	rec, ok, err := d.RecordGet(s.owner, filepath.Base(dir))
	if err != nil {
		return nil, "", "", false, err
	}
	if !ok {
		rec, ok, err = d.RecordGetArchivedByName(s.owner, filepath.Base(dir))
		if err != nil || !ok {
			return nil, "", "", false, err
		}
	}
	return d, rec.ID, base, true, nil
}

// RoundFiles returns the basenames of name's round files: what is in its
// directory, excluding directories and dot-files, united with the sealed
// names in the database. Sorted and de-duplicated, so a sealed file reads the
// same as a present one (P3c §4.2, §4.4).
func (s *Store) RoundFiles(name string) ([]string, error) {
	seen := map[string]bool{}

	entries, err := os.ReadDir(s.Dir(name))
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return nil, fmt.Errorf("read binding dir %q: %w", name, err)
	}
	for _, e := range entries {
		if e.IsDir() || strings.HasPrefix(e.Name(), ".") {
			continue
		}
		seen[e.Name()] = true
	}

	d, err := s.dbForRead()
	if err != nil {
		return nil, err
	}
	if d != nil {
		rec, ok, err := d.RecordGet(s.owner, name)
		if err != nil {
			return nil, err
		}
		if ok {
			names, err := d.RoundFileList(rec.ID)
			if err != nil {
				return nil, err
			}
			for _, n := range names {
				seen[n] = true
			}
		}
	}

	out := make([]string, 0, len(seen))
	for n := range seen {
		out = append(out, n)
	}
	sort.Strings(out)
	return out, nil
}

// RoundsOnDisk returns the round numbers whose NNN-* files are present in
// name's directory, ascending. It is what the daemon's seal pass iterates
// (P3c §4.3): a round whose files are already sealed has none left, so it
// never appears here again.
func (s *Store) RoundsOnDisk(name string) ([]int, error) {
	entries, err := os.ReadDir(s.Dir(name))
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("read binding dir %q: %w", name, err)
	}

	seen := map[int]bool{}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		if r, ok := roundOfFile(e.Name()); ok {
			seen[r] = true
		}
	}

	out := make([]int, 0, len(seen))
	for r := range seen {
		out = append(out, r)
	}
	sort.Ints(out)
	return out, nil
}

// Sealable reports whether round's files of b can become database rows: the
// round must be closed, and nothing that still reads the round's files may be
// in flight (P3c §4.2, §4.3). Pure.
//
// A round closes only when it advances (reconcile close), so round < b.Round
// is the closed test. The blockers are:
//
//   - the stream drain, which appends to the closed round's builder log until
//     the next round starts: it still owns round when Builder.StreamRound is
//     round and the stream is not yet drained. PID is not consulted: a
//     builder that cleared its PID without being killed keeps flushing, and
//     only the supervisor's exit trailer says it is really done;
//   - a consult of that round that has not finished: its own stream, ask and
//     findings are still readable (the state predicate is the consult
//     reconciler's);
//   - a gate run for that round.
//
// A DONE binding seals too: done is not a blocker. Round-naming fields a
// decision does not depend on (HaltNotifiedRound, ForkedAtRound, Serve's
// rounds) are deliberately not consulted.
func Sealable(b Binding, round int, streamDrained bool) bool {
	if round >= b.Round {
		return false
	}
	if b.Builder.StreamRound == round && !streamDrained {
		return false
	}
	for _, c := range b.Consults {
		if c.Round == round && consultActive(c) {
			return false
		}
	}
	if b.GateRun != nil && b.GateRun.Round == round {
		return false
	}
	return true
}

// ExitTrailer prefixes the one line the builder's supervisor appends to a
// round's builder stream when the process exits: "relevo-exit:<code>". It
// names proc.ExitTrailer (internal/proc), which writes that line. This package
// cannot import internal/proc to share the literal -- proc imports
// internal/relevo, which imports this package -- so the literal is repeated
// here, and an external test (exit_trailer_ext_test.go) asserts the two are
// equal.
const ExitTrailer = "relevo-exit:"

// rusageTrailer prefixes the supervisor's resource line, "relevo-rusage:...",
// the line the supervisor writes just before the exit trailer. It names
// proc.RusageTrailer (internal/proc), which writes that line, and
// internal/relevo.RusageTrailerPrefix; this package cannot import proc to share
// the literal -- proc imports internal/relevo, which imports this package -- so
// the literal is repeated here, exactly as ExitTrailer above is.
const rusageTrailer = "relevo-rusage:"

// StreamDrained reports whether round's builder stream is fully consumed by
// the drain, so nothing more will be rendered out of it into that round's
// builder log (P3c §4.2, §4.3). Only the round the endpoint is currently
// draining (Builder.StreamRound) can be undrained; every other round is
// vacuously drained, and the seal pass passes true for it.
//
// A missing stream is drained: there is nothing to render. Otherwise the
// stream is drained exactly when the supervisor's exit trailer is in its
// content -- the relevo spelling, or the pre-rename spelling internal/legacy
// keeps -- and every byte the endpoint's cursor (StreamOffset) has
// not rendered yet is a trailer line. An offset at or past EOF has nothing
// left to render, so it is drained.
//
// The trailer is what says the builder really exited; the cursor is only how
// far the drain got. A pre-rename stream stops its cursor before the
// supervisor's trailing rusage and exit lines, so a stream whose only
// unrendered bytes are those lines is drained too. It is the caller's test for
// Sealable's stream-drain blocker.
func (s *Store) StreamDrained(b Binding, round int) bool {
	if round != b.Builder.StreamRound {
		return true
	}
	path := s.BuilderStreamPath(b.Name, round)
	info, err := os.Stat(path)
	if errors.Is(err, fs.ErrNotExist) {
		return true
	}
	if err != nil {
		return false
	}
	body, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	text := string(body)
	if !strings.Contains(text, "\n"+ExitTrailer) && !strings.Contains(text, "\n"+legacy.ExitTrailer) {
		return false
	}
	off := b.Builder.StreamOffset
	if off < 0 {
		off = 0
	}
	if off >= info.Size() {
		return true
	}
	if trailerLinesOnly(text[off:]) {
		return true
	}
	// The exit trailer proves the builder is gone, so nothing will write this
	// stream again. A drain that stopped short -- the pre-rename drain could
	// stop mid-line, a few bytes before the trailer -- will never advance
	// either; once the stream has been quiet for staleStreamAfter, what it has
	// not rendered is final, and the round may seal.
	return time.Since(info.ModTime()) >= staleStreamAfter
}

// staleStreamAfter is how long a stream that carries its exit trailer must go
// unwritten before StreamDrained stops waiting for the drain to catch up.
const staleStreamAfter = time.Hour

// trailerLinesOnly reports whether every line in s is one the supervisor's
// exit leaves behind: an empty line, or a rusage or exit trailer line in
// either the relevo or the pre-rename spelling. It is how StreamDrained tests
// the bytes a drain has not rendered yet: any payload line means the builder
// may still be flushing, and only the supervisor's own trailer may remain.
func trailerLinesOnly(s string) bool {
	for _, line := range strings.Split(s, "\n") {
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, ExitTrailer) ||
			strings.HasPrefix(line, legacy.ExitTrailer) ||
			strings.HasPrefix(line, rusageTrailer) ||
			strings.HasPrefix(line, legacy.RusageTrailer) {
			continue
		}
		return false
	}
	return true
}

// consultActive reports whether a consult can still need its round's files.
// It is the predicate reconcileConsults works from: done and silent are
// terminal, spawning and running are not.
func consultActive(c Consult) bool {
	switch c.State {
	case ConsultDone, ConsultSilent:
		return false
	}
	return true
}

// SealRound writes every NNN-* file of name's round -- round files and
// consult files alike -- into the database in one transaction, and, only
// after that transaction commits, removes them from the directory. It returns
// how many files it sealed.
//
// A read or write error leaves every file in place and is returned: the next
// pass retries, and RoundFilePut is an upsert, so a removal that failed after
// the commit re-puts the same bytes and removes them again. A removal error
// is logged, never returned: the seal itself succeeded. Non-NNN files (today
// land-gate.log) are never sealed. The state lock is already held by the
// caller (P3c §4.2).
func (t *Tx) SealRound(name string, round int) (int, error) {
	files, err := roundFilesOfDir(t.s.Dir(name), round)
	if err != nil || len(files) == 0 {
		return 0, err
	}

	d, err := t.s.dbForWrite()
	if err != nil {
		return 0, err
	}
	rec, ok, err := d.RecordGet(t.s.owner, name)
	if err != nil {
		return 0, err
	}
	if !ok {
		return 0, nil
	}

	now := time.Now().UTC()
	sealed := make([]string, 0, len(files))
	err = d.Tx(func(dtx *db.Tx) error {
		sealed = sealed[:0]
		for _, path := range files {
			base := filepath.Base(path)
			body, rerr := os.ReadFile(path)
			if rerr != nil {
				return fmt.Errorf("seal %s: %w", base, rerr)
			}
			info, serr := os.Stat(path)
			if serr != nil {
				return fmt.Errorf("seal %s: %w", base, serr)
			}
			if perr := dtx.RoundFilePut(rec.ID, base, round, body, info.ModTime(), now); perr != nil {
				return perr
			}
			sealed = append(sealed, path)
		}
		return nil
	})
	if err != nil {
		return 0, err
	}

	for _, path := range sealed {
		if rerr := os.Remove(path); rerr != nil && !errors.Is(rerr, fs.ErrNotExist) {
			slog.Warn("seal: could not remove sealed round file", "binding", name, "file", filepath.Base(path), "err", rerr)
		}
	}
	return len(sealed), nil
}

// roundFilesOfDir returns the paths of dir's files whose leading NNN- is
// round, sorted. A directory with no such file -- or no directory at all --
// is an empty list, not an error.
func roundFilesOfDir(dir string, round int) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("read binding dir %s: %w", dir, err)
	}

	var out []string
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		if r, ok := roundOfFile(e.Name()); !ok || r != round {
			continue
		}
		out = append(out, filepath.Join(dir, e.Name()))
	}
	sort.Strings(out)
	return out, nil
}
