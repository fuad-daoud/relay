// Package ingest reads a binding's directory -- live under
// ~/.local/state/relevo/<name>/, or a gc tarball under .archive/ -- and
// upserts every fact it holds into internal/db
// (docs/specs/2026-09-20-persistence-design.md §5.2).
package ingest

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/fuad-daoud/relevo/internal/store"
)

// Source is one binding directory, live or archived: it decodes bind.json,
// lists and opens member files by basename, and reports its own origin.
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

// maxCachedTarMember is how large a tar member TarSource caches in memory at
// index time; larger members stream on demand by re-reading the tarball,
// since a tar has no random-access index.
const maxCachedTarMember = 64 * 1024 * 1024

// archiveStampLayout matches store's archive() stamp format
// (internal/store/store.go archiveStampLayout): YYYYMMDD-HHMMSS.
const archiveStampLayout = "20060102-150405"

type tarMember struct {
	size int64
	data []byte // nil when size > maxCachedTarMember
}

// tarSource is a gc tarball, indexed once at construction: archive() writes
// a flat <name>/ directory of files (internal/store/store.go tarGzDir), so
// every member name here is the basename inside that directory.
type tarSource struct {
	path       string
	name       string // the binding name, from the tarball's top-level directory
	members    map[string]tarMember
	order      []string // sorted basenames
	archivedAt time.Time
	hasStamp   bool
}

// TarSource opens path once to index its members: it reads the gzip tar
// sequentially, caching small members in memory since a tar has no
// name -> offset index; members over 64 MB are re-read from disk on demand
// by Open.
func TarSource(path string) (Source, error) {
	ts := &tarSource{path: path, members: map[string]tarMember{}}
	if err := ts.index(); err != nil {
		return nil, err
	}
	ts.parseStamp()
	return ts, nil
}

func (ts *tarSource) index() error {
	f, err := os.Open(ts.path)
	if err != nil {
		return fmt.Errorf("%w: %s: %v", ErrSource, ts.path, err)
	}
	defer f.Close()

	gz, err := gzip.NewReader(f)
	if err != nil {
		return fmt.Errorf("%w: %s: %v", ErrSource, ts.path, err)
	}
	defer gz.Close()

	tr := tar.NewReader(gz)
	for {
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return fmt.Errorf("%w: %s: %v", ErrSource, ts.path, err)
		}
		if h.Typeflag != tar.TypeReg {
			continue
		}

		trimmed := strings.Trim(h.Name, "/")
		parts := strings.SplitN(trimmed, "/", 2)
		if len(parts) != 2 || parts[1] == "" || strings.Contains(parts[1], "/") {
			// Not "<name>/<file>", or nested deeper -- archive() writes a
			// flat directory, so this is not a member we understand.
			continue
		}
		if ts.name == "" {
			ts.name = parts[0]
		}
		base := parts[1]

		m := tarMember{size: h.Size}
		if h.Size <= maxCachedTarMember {
			data, err := io.ReadAll(tr)
			if err != nil {
				return fmt.Errorf("%w: %s: read %s: %v", ErrSource, ts.path, base, err)
			}
			m.data = data
		}
		ts.members[base] = m
		ts.order = append(ts.order, base)
	}

	if ts.name == "" {
		return fmt.Errorf("%w: %s: empty or unrecognised archive", ErrSource, ts.path)
	}
	sort.Strings(ts.order)
	return nil
}

// parseStamp extracts the archive stamp from the tarball's file name
// (<name>-YYYYMMDD-HHMMSS.tar.gz), matching store.ListArchives.
func (ts *tarSource) parseStamp() {
	base := strings.TrimSuffix(filepath.Base(ts.path), ".tar.gz")
	if len(base) < len(archiveStampLayout)+2 {
		return
	}
	cut := len(base) - len(archiveStampLayout)
	if base[cut-1] != '-' {
		return
	}
	at, err := time.Parse(archiveStampLayout, base[cut:])
	if err != nil {
		return
	}
	ts.archivedAt = at.UTC()
	ts.hasStamp = true
}

// ArchivedAt returns the stamp parsed from the tarball's file name, and
// whether one was found.
func (ts *tarSource) ArchivedAt() (time.Time, bool) { return ts.archivedAt, ts.hasStamp }

func (ts *tarSource) Name() string { return ts.name }

func (ts *tarSource) Bind() (store.Binding, error) {
	rc, _, err := ts.Open("bind.json")
	if err != nil {
		return store.Binding{}, fmt.Errorf("%w: %s: %v", ErrSource, ts.path, err)
	}
	defer rc.Close()

	data, err := io.ReadAll(rc)
	if err != nil {
		return store.Binding{}, fmt.Errorf("%w: %s: read bind.json: %v", ErrSource, ts.path, err)
	}
	var b store.Binding
	if err := json.Unmarshal(data, &b); err != nil {
		return store.Binding{}, fmt.Errorf("%w: %s: decode bind.json: %v", ErrSource, ts.path, err)
	}
	return b, nil
}

func (ts *tarSource) Open(member string) (io.ReadCloser, int64, error) {
	m, ok := ts.members[member]
	if !ok {
		return nil, 0, os.ErrNotExist
	}
	if m.size <= maxCachedTarMember {
		return io.NopCloser(bytes.NewReader(m.data)), m.size, nil
	}
	return ts.openStreaming(member, m.size)
}

// openStreaming re-reads the tarball from the start to reach a member too
// large to have been cached at index time.
func (ts *tarSource) openStreaming(member string, size int64) (io.ReadCloser, int64, error) {
	f, err := os.Open(ts.path)
	if err != nil {
		return nil, 0, err
	}
	gz, err := gzip.NewReader(f)
	if err != nil {
		f.Close()
		return nil, 0, err
	}
	tr := tar.NewReader(gz)
	for {
		h, err := tr.Next()
		if err == io.EOF {
			gz.Close()
			f.Close()
			return nil, 0, os.ErrNotExist
		}
		if err != nil {
			gz.Close()
			f.Close()
			return nil, 0, err
		}
		trimmed := strings.Trim(h.Name, "/")
		parts := strings.SplitN(trimmed, "/", 2)
		if len(parts) == 2 && parts[1] == member {
			return &tarMemberReader{tr: tr, gz: gz, f: f}, h.Size, nil
		}
	}
}

// tarMemberReader streams one member's content from a dedicated tar/gzip/file
// chain, closing all three together.
type tarMemberReader struct {
	tr *tar.Reader
	gz *gzip.Reader
	f  *os.File
}

func (r *tarMemberReader) Read(p []byte) (int, error) { return r.tr.Read(p) }

func (r *tarMemberReader) Close() error {
	gzErr := r.gz.Close()
	fErr := r.f.Close()
	if gzErr != nil {
		return gzErr
	}
	return fErr
}

func (ts *tarSource) List() ([]string, error) {
	out := make([]string, len(ts.order))
	copy(out, ts.order)
	return out, nil
}

func (ts *tarSource) Origin() (string, string) { return "archive", ts.path }
