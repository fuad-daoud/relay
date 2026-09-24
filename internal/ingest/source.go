// Package ingest reads a binding -- live under
// ~/.local/state/relevo/<name>/ or in the store's database, or archived as a
// record -- and upserts every fact it holds into internal/db
// (docs/specs/2026-09-20-persistence-design.md §5.2, P3d §4.5).
package ingest

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/fuad-daoud/relevo/internal/store"
)

// Source is one binding, live or archived: it decodes bind.json, lists and
// opens member files by basename, and reports its own origin.
type Source interface {
	// Name is the binding name.
	Name() string
	// Bind decodes bind.json. ErrSource when it is missing or invalid.
	Bind() (store.Binding, error)
	// Open returns member's content and size. member is a basename inside
	// the binding directory. os.ErrNotExist when member does not exist.
	Open(member string) (io.ReadCloser, int64, error)
	// List returns every member's basename, sorted.
	List() ([]string, error)
	// Origin reports "live" and the directory, or "archive" and the
	// tarball path.
	Origin() (kind, path string)
}

// archivedAtter is a Source that knows when it was archived. The mirror's
// binding row carries that stamp, which `relevo show` and the UI read
// (P3d §4.5).
type archivedAtter interface {
	ArchivedAt() (time.Time, bool)
}

// DirSource is a live binding directory.
type dirSource struct {
	dir string
}

// DirSource returns a Source over a live binding directory.
func DirSource(dir string) Source { return dirSource{dir: filepath.Clean(dir)} }

func (d dirSource) Name() string { return filepath.Base(d.dir) }

func (d dirSource) Bind() (store.Binding, error) {
	data, err := os.ReadFile(filepath.Join(d.dir, "bind.json"))
	if err != nil {
		return store.Binding{}, fmt.Errorf("%w: %s: %v", ErrSource, d.dir, err)
	}
	var b store.Binding
	if err := json.Unmarshal(data, &b); err != nil {
		return store.Binding{}, fmt.Errorf("%w: %s: decode bind.json: %v", ErrSource, d.dir, err)
	}
	return b, nil
}

func (d dirSource) Open(member string) (io.ReadCloser, int64, error) {
	f, err := os.Open(filepath.Join(d.dir, member))
	if err != nil {
		return nil, 0, err
	}
	info, err := f.Stat()
	if err != nil {
		f.Close()
		return nil, 0, err
	}
	return f, info.Size(), nil
}

func (d dirSource) List() ([]string, error) {
	entries, err := os.ReadDir(d.dir)
	if err != nil {
		return nil, err
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		names = append(names, e.Name())
	}
	sort.Strings(names)
	return names, nil
}

func (d dirSource) Origin() (string, string) { return "live", d.dir }

// storeSource is a live binding read through store.Store instead of its
// directory: after P3a the binding and its log live in the store's database,
// so bind.json and log.jsonl are synthesized from Load and ReadLog while every
// other member still comes from the binding directory (P3a plan §4.7).
type storeSource struct {
	st   *store.Store
	name string
}

// StoreSource returns a Source over a live binding held in the store's
// database. bind.json is Load's binding re-indented, log.jsonl is ReadLog's
// entries one JSON object per line, and every other member is read through
// Store.ReadFile: from the binding directory while the round is open, from
// the sealed round_file row once it is not (P3c §4.4).
func StoreSource(st *store.Store, name string) Source {
	return storeSource{st: st, name: name}
}

func (s storeSource) Name() string { return s.name }

func (s storeSource) Bind() (store.Binding, error) {
	b, err := s.st.Load(s.name)
	if err != nil {
		return store.Binding{}, fmt.Errorf("%w: %s: %v", ErrSource, s.st.Dir(s.name), err)
	}
	return b, nil
}

func (s storeSource) Open(member string) (io.ReadCloser, int64, error) {
	switch member {
	case "bind.json":
		b, err := s.st.Load(s.name)
		if err != nil {
			return nil, 0, err
		}
		data, err := json.MarshalIndent(b, "", "  ")
		if err != nil {
			return nil, 0, err
		}
		return io.NopCloser(bytes.NewReader(data)), int64(len(data)), nil
	case "log.jsonl":
		entries, err := s.st.ReadLog(s.name)
		if err != nil {
			return nil, 0, err
		}
		var buf bytes.Buffer
		for _, e := range entries {
			line, err := json.Marshal(e)
			if err != nil {
				return nil, 0, err
			}
			buf.Write(line)
			buf.WriteByte('\n')
		}
		data := buf.Bytes()
		return io.NopCloser(bytes.NewReader(data)), int64(len(data)), nil
	default:
		if roundMember(member) {
			// A round file may have been sealed into the store's database
			// (P3c §4.4): ReadFile answers from disk or from the sealed row.
			path := filepath.Join(s.st.Dir(s.name), member)
			data, err := s.st.ReadFile(path)
			if err != nil {
				return nil, 0, err
			}
			return io.NopCloser(bytes.NewReader(data)), int64(len(data)), nil
		}
		return dirSource{dir: s.st.Dir(s.name)}.Open(member)
	}
}

func (s storeSource) List() ([]string, error) {
	names, err := s.st.RoundFiles(s.name)
	if err != nil {
		return nil, err
	}
	names = append(names, "bind.json", "log.jsonl")
	sort.Strings(names)
	return names, nil
}

func (s storeSource) Origin() (string, string) { return "live", s.st.Dir(s.name) }

// archivedSource is an archived binding read through store.Store instead of a
// tarball: after P3d the binding is a record, its log is its event rows and its
// round files are round_file rows (P3d §4.5). It is StoreSource's counterpart
// for the records archive() and the tarball import minted.
type archivedSource struct {
	st       *store.Store
	recordID string
}

// ArchivedSource returns a Source over an archived record held in the store's
// database. bind.json is the record decoded and re-indented, log.jsonl is
// ArchivedLog's entries one JSON object per line, and every NNN-* member is
// read through Store.ArchivedFile. It is how the daemon feeds archived records
// into the mirror without a tarball (P3d §4.5).
func ArchivedSource(st *store.Store, recordID string) Source {
	return archivedSource{st: st, recordID: recordID}
}

func (s archivedSource) Name() string {
	b, ok, err := s.st.ArchivedRecord(s.recordID)
	if err != nil || !ok {
		return ""
	}
	return b.Name
}

func (s archivedSource) Bind() (store.Binding, error) {
	b, ok, err := s.st.ArchivedRecord(s.recordID)
	if err != nil {
		return store.Binding{}, fmt.Errorf("%w: record %s: %v", ErrSource, s.recordID, err)
	}
	if !ok {
		return store.Binding{}, fmt.Errorf("%w: record %s: not found", ErrSource, s.recordID)
	}
	return b, nil
}

func (s archivedSource) Open(member string) (io.ReadCloser, int64, error) {
	switch member {
	case "bind.json":
		b, err := s.Bind()
		if err != nil {
			return nil, 0, err
		}
		data, err := json.MarshalIndent(b, "", "  ")
		if err != nil {
			return nil, 0, err
		}
		return io.NopCloser(bytes.NewReader(data)), int64(len(data)), nil
	case "log.jsonl":
		entries, err := s.st.ArchivedLog(s.recordID)
		if err != nil {
			return nil, 0, err
		}
		var buf bytes.Buffer
		for _, e := range entries {
			line, err := json.Marshal(e)
			if err != nil {
				return nil, 0, err
			}
			buf.Write(line)
			buf.WriteByte('\n')
		}
		data := buf.Bytes()
		return io.NopCloser(bytes.NewReader(data)), int64(len(data)), nil
	default:
		if !roundMember(member) {
			return nil, 0, os.ErrNotExist
		}
		body, ok, err := s.st.ArchivedFile(s.recordID, member)
		if err != nil {
			return nil, 0, err
		}
		if !ok {
			return nil, 0, os.ErrNotExist
		}
		return io.NopCloser(bytes.NewReader(body)), int64(len(body)), nil
	}
}

func (s archivedSource) List() ([]string, error) {
	names, err := s.st.ArchivedFiles(s.recordID)
	if err != nil {
		return nil, err
	}
	names = append(names, "bind.json", "log.jsonl")
	sort.Strings(names)
	return names, nil
}

// Origin reports the kind the mirror records for an archived source. The path
// is empty: an archived record has no directory or tarball behind it any more
// (P3d §4.5).
func (s archivedSource) Origin() (string, string) { return "archive", "" }

// ArchivedAt is the record's archived stamp, which ingest reports as the
// mirror binding's ArchivedAt (P3d §4.5).
func (s archivedSource) ArchivedAt() (time.Time, bool) {
	return s.st.ArchivedAtOf(s.recordID)
}

// roundMember reports whether member is a round file's basename: the NNN-
// prefix every file relevo writes for a round carries. Only those can be
// sealed into the database, so only those are read through Store.ReadFile.
func roundMember(member string) bool {
	if len(member) < 4 || member[3] != '-' {
		return false
	}
	for i := 0; i < 3; i++ {
		if member[i] < '0' || member[i] > '9' {
			return false
		}
	}
	return true
}
