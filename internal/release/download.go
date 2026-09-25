package release

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path"
	"strings"
	"time"
)

// DownloadTimeout is the budget for one whole HTTP request: the release
// archive is a few megabytes, and a stalled connection must not hang the CLI
// forever.
const DownloadTimeout = 5 * time.Minute

// The three bounds keep a hostile or broken endpoint from making relevo
// allocate without limit.
const (
	maxChecksums = 1 << 20
	maxArchive   = 256 << 20
	maxBinary    = 256 << 20
)

// Sentinel errors FetchBinary returns; each is wrapped with the URL or tag by
// the caller side.
var (
	// ErrNoChecksum is checksums.txt holding no line for the archive, or more
	// than one.
	ErrNoChecksum = errors.New("no checksum for the archive")
	// ErrChecksumMismatch is the archive's SHA-256 not matching its line.
	ErrChecksumMismatch = errors.New("archive checksum mismatch")
	// ErrNoBinary is no regular file named relevo at the archive root.
	ErrNoBinary = errors.New("archive holds no relevo binary")
	// ErrTooLarge is a response past its bound.
	ErrTooLarge = errors.New("response too large")
)

// Downloader fetches a release archive, verifies it against checksums.txt and
// extracts the relevo binary into a temp file beside the running executable.
type Downloader struct {
	// Base is the download base URL. The CLI always passes DownloadBase;
	// tests pass an httptest URL. There is no environment override, so no
	// environment variable can redirect a binary download.
	Base string
	// Client is the HTTP client; nil uses one with DownloadTimeout.
	Client *http.Client
}

// FetchBinary produces a verified, executable relevo binary for tag as a temp
// file in destDir and returns its path. Nothing is written to disk before the
// checksum passes, and no temp file is left behind on any error.
func (d *Downloader) FetchBinary(ctx context.Context, tag, goos, goarch, destDir string) (string, error) {
	archiveURL, checksumsURL := assetURLsFrom(d.Base, tag, goos, goarch)
	archiveName := path.Base(archiveURL)

	sum, err := d.fetchChecksum(ctx, checksumsURL, archiveName)
	if err != nil {
		return "", err
	}

	data, err := d.get(ctx, archiveURL, maxArchive)
	if err != nil {
		return "", err
	}
	want, err := hex.DecodeString(sum)
	if err != nil {
		return "", fmt.Errorf("%s: %w", checksumsURL, ErrNoChecksum)
	}
	got := sha256.Sum256(data)
	if subtle.ConstantTimeCompare(got[:], want) != 1 {
		return "", fmt.Errorf("%s: %w", archiveURL, ErrChecksumMismatch)
	}

	bin, err := extractBinary(data)
	if err != nil {
		return "", err
	}

	return writeTempBinary(destDir, bin)
}

// fetchChecksum reads checksums.txt and returns the SHA-256 for archiveName.
// Exactly one line must name the archive, and its first field must be 64
// lowercase hex characters; anything else is ErrNoChecksum.
func (d *Downloader) fetchChecksum(ctx context.Context, url, archiveName string) (string, error) {
	body, err := d.get(ctx, url, maxChecksums)
	if err != nil {
		return "", err
	}

	matches := 0
	sum := ""
	for _, line := range strings.Split(string(body), "\n") {
		fields := strings.Fields(line)
		if len(fields) != 2 || fields[1] != archiveName {
			continue
		}
		matches++
		sum = fields[0]
	}
	if matches != 1 || !isLowerHex64(sum) {
		return "", fmt.Errorf("%s: %w", url, ErrNoChecksum)
	}
	return sum, nil
}

// get performs one bounded, unauthenticated, un-retried GET and returns the
// body. A transport failure wraps ErrOffline; a non-200 names the URL and the
// status.
func (d *Downloader) get(ctx context.Context, url string, limit int64) ([]byte, error) {
	client := d.Client
	if client == nil {
		client = &http.Client{Timeout: DownloadTimeout}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrOffline, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%s: %s", url, resp.Status)
	}

	data, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return nil, fmt.Errorf("%s: %w", url, err)
	}
	if int64(len(data)) > limit {
		return nil, fmt.Errorf("%s: %w", url, ErrTooLarge)
	}
	return data, nil
}

// extractBinary gunzips and reads the verified bytes, returning the first
// regular file named relevo at the archive root. Every other entry is skipped,
// and no entry name ever becomes a filesystem path.
func extractBinary(data []byte) ([]byte, error) {
	gz, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrNoBinary, err)
	}
	defer gz.Close()

	tr := tar.NewReader(gz)
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("%w: %v", ErrNoBinary, err)
		}
		if hdr.Typeflag != tar.TypeReg || path.Clean(hdr.Name) != "relevo" {
			continue
		}
		bin, err := io.ReadAll(io.LimitReader(tr, maxBinary+1))
		if err != nil {
			return nil, err
		}
		if int64(len(bin)) > maxBinary {
			return nil, ErrTooLarge
		}
		return bin, nil
	}
	return nil, ErrNoBinary
}

// writeTempBinary creates a temp file in destDir, writes bin, fsyncs and
// closes it, and makes it executable. Every failure removes the temp file.
func writeTempBinary(destDir string, bin []byte) (string, error) {
	f, err := os.CreateTemp(destDir, ".relevo-update-*")
	if err != nil {
		return "", err
	}
	tmp := f.Name()
	fail := func(err error) (string, error) {
		f.Close()
		os.Remove(tmp)
		return "", err
	}

	if _, err := f.Write(bin); err != nil {
		return fail(err)
	}
	if err := f.Sync(); err != nil {
		return fail(err)
	}
	if err := f.Close(); err != nil {
		os.Remove(tmp)
		return "", err
	}
	if err := os.Chmod(tmp, 0o755); err != nil {
		os.Remove(tmp)
		return "", err
	}
	return tmp, nil
}

// isLowerHex64 reports whether s is exactly 64 lowercase hex characters.
func isLowerHex64(s string) bool {
	if len(s) != 64 {
		return false
	}
	for _, r := range s {
		if (r < '0' || r > '9') && (r < 'a' || r > 'f') {
			return false
		}
	}
	return true
}
