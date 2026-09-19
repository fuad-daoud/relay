package remote

import (
	"context"
	"errors"
	"io"
)

// Snapshot represents a point-in-time capture of git refs produced by TreeTransport.
type Snapshot struct {
	ContentType string
	Body        io.ReadCloser     // nil when Empty; caller closes otherwise
	Heads       map[string]string // ref -> sha the snapshot carries, always filled
	Empty       bool              // nothing newer than `since`; Body is nil
}

// TreeTransport defines the interface for moving git ref snapshots between repositories.
type TreeTransport interface {
	// Snapshot packages every ref in refs, as it stands in repo, relative to
	// since. since "" means "everything reachable". since must be a commit
	// that is an ancestor of every ref; otherwise ErrSinceUnknown.
	Snapshot(ctx context.Context, repo string, refs []string, since string) (Snapshot, error)

	// Absorb applies a body of contentType to repo. Every ref the body
	// carries must be in refs (else ErrUnexpectedRef); every ref in refs the
	// body carries is fast-forwarded (else git.ErrNotFastForward); a ref in
	// refs the body does not carry is left as it is. Returns ref -> new sha
	// for the refs it moved. A zero-length body is a no-op returning an
	// empty map.
	Absorb(ctx context.Context, repo, contentType string, body io.Reader, refs []string) (map[string]string, error)
}

// Transport sentinels wrapped with context (%w). Callers inspect them with errors.Is.
var (
	// ErrUnsupportedType reports that contentType is not supported by the transport.
	ErrUnsupportedType = errors.New("unsupported content type")

	// ErrUnexpectedRef reports that the transport body carries a ref not listed in refs.
	ErrUnexpectedRef = errors.New("unexpected ref in transport body")

	// ErrSinceUnknown reports that since is not a commit in repo or not an ancestor of every ref.
	ErrSinceUnknown = errors.New("since commit is unknown or not an ancestor")
)
