package remote

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"

	"github.com/fuad-daoud/relay/internal/git"
)

var _ TreeTransport = (*BundleTransport)(nil)

// BundleTransport implements TreeTransport using git bundles.
type BundleTransport struct {
	git *git.Client
	tmp string
}

// NewBundleTransport creates a new BundleTransport using g and tmpDir for temporary bundle files.
// If tmpDir is "", os.TempDir() is used.
func NewBundleTransport(g *git.Client, tmpDir string) *BundleTransport {
	if tmpDir == "" {
		tmpDir = os.TempDir()
	}
	return &BundleTransport{
		git: g,
		tmp: tmpDir,
	}
}

type fileRemover struct {
	*os.File
	path string
	once sync.Once
}

func (f *fileRemover) Close() error {
	var err error
	f.once.Do(func() {
		err = f.File.Close()
		_ = os.Remove(f.path)
	})
	return err
}

// Snapshot packages every ref in refs relative to since into a git bundle.
func (t *BundleTransport) Snapshot(ctx context.Context, repo string, refs []string, since string) (Snapshot, error) {
	f, err := os.CreateTemp(t.tmp, "relay-bundle-*.bundle")
	if err != nil {
		return Snapshot{}, err
	}
	tmpPath := f.Name()
	_ = f.Close()

	heads, empty, err := t.git.BundleCreate(ctx, repo, tmpPath, refs, since)
	if err != nil {
		_ = os.Remove(tmpPath)
		if errors.Is(err, git.ErrRefMissing) && strings.Contains(err.Error(), "since is not an ancestor") {
			return Snapshot{}, fmt.Errorf("%w: %v", ErrSinceUnknown, err)
		}
		return Snapshot{}, err
	}

	if empty {
		_ = os.Remove(tmpPath)
		return Snapshot{
			ContentType: ContentTypeGitBundle,
			Heads:       heads,
			Empty:       true,
		}, nil
	}

	opened, err := os.Open(tmpPath)
	if err != nil {
		_ = os.Remove(tmpPath)
		return Snapshot{}, err
	}

	return Snapshot{
		ContentType: ContentTypeGitBundle,
		Body:        &fileRemover{File: opened, path: tmpPath},
		Heads:       heads,
		Empty:       false,
	}, nil
}

// Absorb applies a bundle body to repo, updating the specified refs.
func (t *BundleTransport) Absorb(ctx context.Context, repo, contentType string, body io.Reader, refs []string) (map[string]string, error) {
	if contentType != ContentTypeGitBundle {
		return nil, ErrUnsupportedType
	}

	tmpFile, err := os.CreateTemp(t.tmp, "relay-bundle-*.bundle")
	if err != nil {
		return nil, err
	}
	tmpPath := tmpFile.Name()
	defer os.Remove(tmpPath)

	n, err := io.Copy(tmpFile, body)
	_ = tmpFile.Close()
	if err != nil {
		return nil, err
	}
	if n == 0 {
		return map[string]string{}, nil
	}

	heads, err := t.git.BundleHeads(ctx, repo, tmpPath)
	if err != nil {
		return nil, err
	}

	allowed := make(map[string]bool, len(refs))
	for _, r := range refs {
		allowed[r] = true
	}
	for ref := range heads {
		if !allowed[ref] {
			return nil, fmt.Errorf("%w: ref %s", ErrUnexpectedRef, ref)
		}
	}

	return t.git.FetchBundle(ctx, repo, tmpPath, refs)
}
