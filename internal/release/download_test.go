package release

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
)

// These tests are httptest-only: no test in this package may reach the
// network, and none may reach github.com. The archive is built in memory,
// which is the whole truth about what FetchBinary consumes.
const (
	testTag     = "v0.13.0"
	testArchive = "relevo_v0.13.0_linux_amd64.tar.gz"
	fakeBinary  = "#!/bin/sh\necho fake\n"
)

// tarEntry is one entry of an in-memory archive.
type tarEntry struct {
	name     string
	data     []byte
	typeflag byte
	linkname string
}

// tarGz builds a gzip-compressed tar in memory from entries, in order.
func tarGz(t *testing.T, entries ...tarEntry) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for _, e := range entries {
		hdr := &tar.Header{
			Name:     e.name,
			Mode:     0o755,
			Size:     int64(len(e.data)),
			Typeflag: e.typeflag,
			Linkname: e.linkname,
		}
		if err := tw.WriteHeader(hdr); err != nil {
			t.Fatalf("write header %s: %v", e.name, err)
		}
		if _, err := tw.Write(e.data); err != nil {
			t.Fatalf("write %s: %v", e.name, err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatalf("close tar: %v", err)
	}
	if err := gz.Close(); err != nil {
		t.Fatalf("close gzip: %v", err)
	}
	return buf.Bytes()
}

// checksumLine is one checksums.txt line for data, in sha256sum's format.
func checksumLine(data []byte, name string) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]) + "  " + name + "\n"
}

// assetServer is what the httptest server serves.
type assetServer struct {
	archive       []byte
	checksums     []byte
	archiveStatus int
}

// newAssetServer serves /<tag>/checksums.txt and /<tag>/relevo_<tag>_...tar.gz
// and returns the base URL to hand to Downloader.Base.
func newAssetServer(t *testing.T, s assetServer) string {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/"+testTag+"/", func(w http.ResponseWriter, r *http.Request) {
		switch path.Base(r.URL.Path) {
		case "checksums.txt":
			_, _ = w.Write(s.checksums)
		case testArchive:
			if s.archiveStatus != 0 {
				http.Error(w, "no archive", s.archiveStatus)
				return
			}
			_, _ = w.Write(s.archive)
		default:
			http.NotFound(w, r)
		}
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv.URL
}

// assertEmptyDir pins the postcondition that no temp file is left behind on
// any failure path.
func assertEmptyDir(t *testing.T, dir string) {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read temp dir: %v", err)
	}
	if len(entries) != 0 {
		names := make([]string, 0, len(entries))
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Errorf("destDir %s is not empty: %v", dir, names)
	}
}

// TestFetchBinaryHappyPath pins the whole success path: verified bytes, mode
// 0755, and a temp file that sits in destDir holding exactly the archive's
// relevo entry.
func TestFetchBinaryHappyPath(t *testing.T) {
	archive := tarGz(t,
		tarEntry{name: "relevo", data: []byte(fakeBinary), typeflag: tar.TypeReg},
		tarEntry{name: "README.md", data: []byte("read me\n"), typeflag: tar.TypeReg},
	)
	base := newAssetServer(t, assetServer{
		archive:   archive,
		checksums: []byte(checksumLine(archive, testArchive)),
	})
	dest := t.TempDir()

	got, err := (&Downloader{Base: base}).FetchBinary(context.Background(), testTag, "linux", "amd64", dest)
	if err != nil {
		t.Fatalf("FetchBinary: %v", err)
	}
	if filepath.Clean(filepath.Dir(got)) != filepath.Clean(dest) {
		t.Errorf("binary in %s, want %s", filepath.Dir(got), dest)
	}
	info, err := os.Stat(got)
	if err != nil {
		t.Fatalf("stat %s: %v", got, err)
	}
	if info.Mode().Perm() != 0o755 {
		t.Errorf("mode = %v, want 0755", info.Mode().Perm())
	}
	data, err := os.ReadFile(got)
	if err != nil {
		t.Fatalf("read %s: %v", got, err)
	}
	if string(data) != fakeBinary {
		t.Errorf("contents = %q, want %q", data, fakeBinary)
	}
}

// TestFetchBinaryChecksumMismatch pins that a wrong checksum is refused before
// anything is written, and leaves destDir empty.
func TestFetchBinaryChecksumMismatch(t *testing.T) {
	archive := tarGz(t, tarEntry{name: "relevo", data: []byte(fakeBinary), typeflag: tar.TypeReg})
	base := newAssetServer(t, assetServer{
		archive:   archive,
		checksums: []byte(checksumLine([]byte("something else"), testArchive)),
	})
	dest := t.TempDir()

	_, err := (&Downloader{Base: base}).FetchBinary(context.Background(), testTag, "linux", "amd64", dest)
	if !errors.Is(err, ErrChecksumMismatch) {
		t.Fatalf("FetchBinary = %v, want ErrChecksumMismatch", err)
	}
	assertEmptyDir(t, dest)
}

// TestFetchBinaryNoChecksum pins that a checksums.txt without a line for the
// archive is ErrNoChecksum.
func TestFetchBinaryNoChecksum(t *testing.T) {
	archive := tarGz(t, tarEntry{name: "relevo", data: []byte(fakeBinary), typeflag: tar.TypeReg})
	base := newAssetServer(t, assetServer{
		archive:   archive,
		checksums: []byte(checksumLine(archive, "relevo_v0.12.0_linux_amd64.tar.gz")),
	})
	dest := t.TempDir()

	_, err := (&Downloader{Base: base}).FetchBinary(context.Background(), testTag, "linux", "amd64", dest)
	if !errors.Is(err, ErrNoChecksum) {
		t.Fatalf("FetchBinary = %v, want ErrNoChecksum", err)
	}
	assertEmptyDir(t, dest)
}

// TestFetchBinaryNoBinary pins an archive with no relevo at the root.
func TestFetchBinaryNoBinary(t *testing.T) {
	archive := tarGz(t, tarEntry{name: "README.md", data: []byte("read me\n"), typeflag: tar.TypeReg})
	base := newAssetServer(t, assetServer{
		archive:   archive,
		checksums: []byte(checksumLine(archive, testArchive)),
	})
	dest := t.TempDir()

	_, err := (&Downloader{Base: base}).FetchBinary(context.Background(), testTag, "linux", "amd64", dest)
	if !errors.Is(err, ErrNoBinary) {
		t.Fatalf("FetchBinary = %v, want ErrNoBinary", err)
	}
	assertEmptyDir(t, dest)
}

// TestFetchBinaryRootOnly pins that only a relevo at the archive root counts:
// neither "../relevo" nor "sub/relevo" is the binary.
func TestFetchBinaryRootOnly(t *testing.T) {
	archive := tarGz(t,
		tarEntry{name: "../relevo", data: []byte(fakeBinary), typeflag: tar.TypeReg},
		tarEntry{name: "sub/relevo", data: []byte(fakeBinary), typeflag: tar.TypeReg},
	)
	base := newAssetServer(t, assetServer{
		archive:   archive,
		checksums: []byte(checksumLine(archive, testArchive)),
	})
	dest := t.TempDir()

	_, err := (&Downloader{Base: base}).FetchBinary(context.Background(), testTag, "linux", "amd64", dest)
	if !errors.Is(err, ErrNoBinary) {
		t.Fatalf("FetchBinary = %v, want ErrNoBinary", err)
	}
	assertEmptyDir(t, dest)
}

// TestFetchBinary404 pins that a non-200 names the status and writes nothing.
func TestFetchBinary404(t *testing.T) {
	archive := tarGz(t, tarEntry{name: "relevo", data: []byte(fakeBinary), typeflag: tar.TypeReg})
	base := newAssetServer(t, assetServer{
		archive:       archive,
		checksums:     []byte(checksumLine(archive, testArchive)),
		archiveStatus: http.StatusNotFound,
	})
	dest := t.TempDir()

	_, err := (&Downloader{Base: base}).FetchBinary(context.Background(), testTag, "linux", "amd64", dest)
	if err == nil {
		t.Fatal("FetchBinary on a 404 = nil error, want an error")
	}
	if !bytes.Contains([]byte(err.Error()), []byte("404")) {
		t.Errorf("error = %q, want it to name the 404 status", err)
	}
	assertEmptyDir(t, dest)
}

// TestFetchBinarySymlinkIsNotTheBinary pins that a symlink entry named relevo
// is skipped: only a regular file is the binary.
func TestFetchBinarySymlinkIsNotTheBinary(t *testing.T) {
	archive := tarGz(t, tarEntry{name: "relevo", typeflag: tar.TypeSymlink, linkname: "/bin/sh"})
	base := newAssetServer(t, assetServer{
		archive:   archive,
		checksums: []byte(checksumLine(archive, testArchive)),
	})
	dest := t.TempDir()

	_, err := (&Downloader{Base: base}).FetchBinary(context.Background(), testTag, "linux", "amd64", dest)
	if !errors.Is(err, ErrNoBinary) {
		t.Fatalf("FetchBinary = %v, want ErrNoBinary", err)
	}
	assertEmptyDir(t, dest)
}

// TestFetchBinaryDuplicateChecksumLine pins the "exactly one line" rule: when
// checksums.txt names the archive twice the file is ambiguous and must be
// ErrNoChecksum, with nothing written. Round 1's code already enforces this;
// the test exists so the rule cannot be loosened silently.
func TestFetchBinaryDuplicateChecksumLine(t *testing.T) {
	archive := tarGz(t, tarEntry{name: "relevo", data: []byte(fakeBinary), typeflag: tar.TypeReg})
	line := checksumLine(archive, testArchive)
	base := newAssetServer(t, assetServer{
		archive:   archive,
		checksums: []byte(line + line),
	})
	dest := t.TempDir()

	_, err := (&Downloader{Base: base}).FetchBinary(context.Background(), testTag, "linux", "amd64", dest)
	if !errors.Is(err, ErrNoChecksum) {
		t.Fatalf("FetchBinary = %v, want ErrNoChecksum", err)
	}
	assertEmptyDir(t, dest)
}

// TestFetchBinaryMalformedChecksum pins the other half of the rule: the
// archive's line must hold 64 lowercase hex characters. An uppercase hash
// and a 63-character hash are both ErrNoChecksum.
func TestFetchBinaryMalformedChecksum(t *testing.T) {
	archive := tarGz(t, tarEntry{name: "relevo", data: []byte(fakeBinary), typeflag: tar.TypeReg})
	sum := sha256.Sum256(archive)
	good := hex.EncodeToString(sum[:])

	tests := []struct {
		name string
		hash string
	}{
		{"uppercase hash", strings.ToUpper(good)},
		{"63-character hash", good[:63]},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			base := newAssetServer(t, assetServer{
				archive:   archive,
				checksums: []byte(tc.hash + "  " + testArchive + "\n"),
			})
			dest := t.TempDir()

			_, err := (&Downloader{Base: base}).FetchBinary(context.Background(), testTag, "linux", "amd64", dest)
			if !errors.Is(err, ErrNoChecksum) {
				t.Fatalf("FetchBinary = %v, want ErrNoChecksum", err)
			}
			assertEmptyDir(t, dest)
		})
	}
}

// TestFetchBinaryRefusesNonReleaseTag pins the guard at the point of use: a
// tag IsReleaseTag rejects must be refused before any request, and nothing
// is written. The counter proves the server was never asked.
func TestFetchBinaryRefusesNonReleaseTag(t *testing.T) {
	var requests atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		http.NotFound(w, r)
	}))
	t.Cleanup(srv.Close)
	dest := t.TempDir()

	_, err := (&Downloader{Base: srv.URL}).FetchBinary(context.Background(), "v1.2.3/../x", "linux", "amd64", dest)
	if !errors.Is(err, ErrBadTag) {
		t.Fatalf("FetchBinary = %v, want ErrBadTag", err)
	}
	if got := requests.Load(); got != 0 {
		t.Errorf("server saw %d request(s), want 0: a bad tag must never reach the network", got)
	}
	assertEmptyDir(t, dest)
}
