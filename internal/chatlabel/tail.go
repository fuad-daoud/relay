package chatlabel

import (
	"bytes"
	"io"
	"os"
)

// ReadTail returns the last max bytes of the file at path, starting at a line
// boundary. max <= 0 means DefaultTailBytes. Errors are returned as-is: the
// caller turns them into the empty label.
//
// A transcript can be megabytes long and is appended to, so the window is taken
// from the end and its first partial line is dropped.
func ReadTail(path string, max int64) ([]byte, error) {
	if max <= 0 {
		max = DefaultTailBytes
	}

	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	info, err := f.Stat()
	if err != nil {
		return nil, err
	}
	size := info.Size()
	if size <= max {
		return io.ReadAll(f)
	}

	if _, err := f.Seek(size-max, io.SeekStart); err != nil {
		return nil, err
	}
	buf := make([]byte, max)
	if _, err := io.ReadFull(f, buf); err != nil {
		return nil, err
	}

	// The window opens mid-line, so everything up to and including the first
	// newline is partial and must go.
	if i := bytes.IndexByte(buf, '\n'); i >= 0 {
		return buf[i+1:], nil
	}
	return []byte{}, nil
}
