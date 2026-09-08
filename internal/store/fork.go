package store

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// roundOfFile parses the leading NNN- of a binding directory entry and returns
// the round number. It accepts every suffix the store writes today without
// enumerating them, so a future round artifact is copied by forks with no change.
func roundOfFile(base string) (int, bool) {
	idx := strings.IndexByte(base, '-')
	if idx <= 0 || idx == len(base)-1 {
		return 0, false
	}
	for i := 0; i < idx; i++ {
		if base[i] < '0' || base[i] > '9' {
			return 0, false
		}
	}
	n, err := strconv.Atoi(base[:idx])
	if err != nil || n < 1 {
		return 0, false
	}
	return n, true
}

// ForkState copies src's history into a NEW binding directory for dst under the
// state lock.
func (s *Store) ForkState(src, dst string, throughRound int) error {
	return s.WithLock(func(tx *Tx) error {
		return tx.ForkState(src, dst, throughRound)
	})
}

// ForkState copies src's history into a NEW binding directory for dst: every
// log entry with Round <= throughRound, and every round file whose leading
// number is <= throughRound. Copied log entries are all marked Confirmed, so a
// fork begins with nothing pending. It does NOT write dst's bind.json -- the
// caller owns the new Binding and saves it.
//
// Preconditions:  the lock is held; src exists; dst has no directory yet;
//
//	throughRound >= 1.
//
// Postconditions: dst's directory exists with log.jsonl and the copied round
//
//	files, all mode 0644; src is not modified in any way.
//
// Errors: ErrNotFound (src), a wrapped copy error, or an error if dst exists.
//
//	On error, dst's directory is removed, so a failed fork leaves no
//	half-populated binding for List to pick up.
func (t *Tx) ForkState(src, dst string, throughRound int) error {
	if err := ValidName(dst); err != nil {
		return err
	}
	if throughRound < 1 {
		return fmt.Errorf("through round must be >= 1, got %d", throughRound)
	}

	if _, err := t.s.load(src); err != nil {
		return err
	}

	dstDir := t.s.Dir(dst)
	if _, err := os.Lstat(dstDir); err == nil {
		return fmt.Errorf("binding directory %q already exists", dst)
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("stat destination dir %s: %w", dstDir, err)
	}

	if err := os.Mkdir(dstDir, bindingDirMode); err != nil {
		return fmt.Errorf("create binding dir %q: %w", dst, err)
	}

	var success bool
	defer func() {
		if !success {
			_ = os.RemoveAll(dstDir)
		}
	}()

	entries, err := t.s.readLog(src)
	if err != nil {
		return fmt.Errorf("read log %q: %w", src, err)
	}

	var logBuf []byte
	for _, e := range entries {
		if e.Round <= throughRound {
			e.Confirmed = true
			e.DeliveredAt = nil
			raw, err := json.Marshal(e)
			if err != nil {
				return fmt.Errorf("encode log entry for %q: %w", dst, err)
			}
			logBuf = append(logBuf, raw...)
			logBuf = append(logBuf, '\n')
		}
	}

	dstLog := t.s.logPath(dst)
	if err := os.WriteFile(dstLog, logBuf, bindingFileMode); err != nil {
		return fmt.Errorf("write log for %q: %w", dst, err)
	}
	if err := os.Chmod(dstLog, bindingFileMode); err != nil {
		return fmt.Errorf("chmod log for %q: %w", dst, err)
	}

	srcDir := t.s.Dir(src)
	dirEntries, err := os.ReadDir(srcDir)
	if err != nil {
		return fmt.Errorf("read binding dir %q: %w", src, err)
	}

	for _, de := range dirEntries {
		if de.IsDir() {
			continue
		}
		base := de.Name()
		r, ok := roundOfFile(base)
		if !ok || r > throughRound {
			continue
		}
		srcFile := filepath.Join(srcDir, base)
		dstFile := filepath.Join(dstDir, base)
		if err := copyFile(srcFile, dstFile, bindingFileMode); err != nil {
			return fmt.Errorf("copy %s: %w", base, err)
		}
	}

	success = true
	return nil
}

func copyFile(src, dst string, mode os.FileMode) (err error) {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()

	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_EXCL, mode)
	if err != nil {
		return err
	}
	defer func() {
		if cerr := out.Close(); cerr != nil && err == nil {
			err = cerr
		}
	}()

	if _, err = io.Copy(out, in); err != nil {
		return err
	}
	if err = out.Sync(); err != nil {
		return err
	}
	if err = os.Chmod(dst, mode); err != nil {
		return err
	}
	return nil
}
