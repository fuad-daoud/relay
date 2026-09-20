package ingest

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"path/filepath"

	"github.com/fuad-daoud/relay/internal/db"
)

// headSampleBytes is how much of an append-only member's start is hashed
// to detect a rewrite (docs/specs/2026-09-20-persistence-design.md §5.2).
const headSampleBytes = 4096

// sha256Hex is the hex-encoded sha256 of data.
func sha256Hex(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// cursorSourceKey is the ingest_cursor natural key for member of src:
// "<abs dir>/<member>" for a live source, "<tarball>::<member>" for an
// archive.
func cursorSourceKey(src Source, member string) string {
	kind, path := src.Origin()
	if kind == "archive" {
		return path + "::" + member
	}
	return filepath.Join(path, member)
}

// readAppendOnly reads the content opener produces in full and returns
// every complete line (without its trailing newline) added since cur --
// from cur's byte offset when it is still valid, from the start otherwise
// -- plus the cursor to save under sourceKey. A trailing partial line is
// left for the next read. startSeq is how many complete lines precede the
// first returned line (0 when reading from the start), so a caller
// numbering rows by absolute line index in the file can continue from it.
//
// The rewrite check hashes only the bytes already confirmed read by the
// saved cursor -- the first min(headSampleBytes, cur.ByteOffset) bytes --
// never the current file's own first headSampleBytes: for a file still
// under headSampleBytes long, comparing against the live head would make
// every ordinary append look like a rewrite, since appending grows what
// "the first headSampleBytes" contains. Hashing only the confirmed prefix
// is stable under append and still catches a rewrite that changes it.
//
// reset is true when the content shrank below cur.ByteOffset or that
// prefix's hash no longer matches cur.HeadSHA: the caller logs that at
// Info. It is not an error -- readAppendOnly always returns every line
// from wherever it started reading, and the UNIQUE keys on event and
// transcript make re-appending already-known rows a no-op.
func readAppendOnly(opener func() (io.ReadCloser, error), sourceKey string, cur db.Cursor, hasCursor bool) (lines [][]byte, startSeq int, next db.Cursor, reset bool, err error) {
	rc, err := opener()
	if err != nil {
		return nil, 0, db.Cursor{}, false, err
	}
	defer rc.Close()

	data, err := io.ReadAll(rc)
	if err != nil {
		return nil, 0, db.Cursor{}, false, err
	}

	var offset int64
	if hasCursor {
		sample := cur.ByteOffset
		if sample > headSampleBytes {
			sample = headSampleBytes
		}
		if int64(len(data)) >= cur.ByteOffset && sha256Hex(data[:sample]) == cur.HeadSHA {
			offset = cur.ByteOffset
		} else {
			reset = true
		}
	}

	startSeq = bytes.Count(data[:offset], []byte{'\n'})

	newOffset := offset
	tail := data[offset:]
	if end := bytes.LastIndexByte(tail, '\n'); end >= 0 {
		complete := tail[:end]
		for _, line := range bytes.Split(complete, []byte{'\n'}) {
			lines = append(lines, line)
		}
		newOffset = offset + int64(end) + 1
	}

	newSample := newOffset
	if newSample > headSampleBytes {
		newSample = headSampleBytes
	}
	newHeadSHA := sha256Hex(data[:newSample])

	return lines, startSeq, db.Cursor{Source: sourceKey, ByteOffset: newOffset, HeadSHA: newHeadSHA}, reset, nil
}
