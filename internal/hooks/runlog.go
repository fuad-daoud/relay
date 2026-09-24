package hooks

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/fuad-daoud/relevo/internal/db"
)

// runLogKey is the kv row the whole run log lives in: a JSON array of HookRun,
// the newest last (P3b round 2 §3).
const runLogKey = "hooks.log"

// runLogCap caps the stored array: the newest 200 runs.
const runLogCap = 200

// importOutputCap is how much of a legacy hooks.log the import keeps: its last
// 4 KiB.
const importOutputCap = 4 << 10

// legacyLogName is the pre-database log file's base name under the state root.
const legacyLogName = "hooks.log"

// KVLog is the RunLog over the machine database's kv row: the whole array of
// runs under one key, read, appended and capped inside one transaction (§4.4).
type KVLog struct {
	// KV is the kv handle the runs live in: the machine database.
	KV db.DBTxKV

	// Root is the relevo state root: a legacy <Root>/hooks.log there is
	// imported once and removed. "" imports nothing.
	Root string

	importOnce sync.Once
	importErr  error
}

var _ RunLog = (*KVLog)(nil)

// NewKVLog is the run log over kv, with root the state root a legacy hooks.log
// is adopted from.
func NewKVLog(kv db.DBTxKV, root string) *KVLog { return &KVLog{KV: kv, Root: root} }

// Append adds one run: read the array, append, cap it at 200, write, in one
// transaction.
func (l *KVLog) Append(run HookRun) error {
	if err := l.ensureImported(); err != nil {
		return err
	}
	return l.KV.Tx(func(tx db.KVTx) error {
		runs, err := l.read(tx)
		if err != nil {
			return err
		}
		runs = append(runs, run)
		if len(runs) > runLogCap {
			runs = runs[len(runs)-runLogCap:]
		}
		raw, err := json.Marshal(runs)
		if err != nil {
			return fmt.Errorf("hooks: encode run log: %w", err)
		}
		return tx.KVPut(runLogKey, raw)
	})
}

// Runs returns the stored runs, oldest first, adopting a legacy file first.
func (l *KVLog) Runs() ([]HookRun, error) {
	if err := l.ensureImported(); err != nil {
		return nil, err
	}
	return l.read(l.KV)
}

// read decodes the stored array from one kv handle.
func (l *KVLog) read(kv db.KVTx) ([]HookRun, error) {
	raw, ok, err := kv.KVGet(runLogKey)
	if err != nil {
		return nil, fmt.Errorf("hooks: read %s: %w", runLogKey, err)
	}
	if !ok {
		return nil, nil
	}
	var runs []HookRun
	if err := json.Unmarshal(raw, &runs); err != nil {
		return nil, fmt.Errorf("hooks: decode %s: %w", runLogKey, err)
	}
	return runs, nil
}

// ensureImported adopts a legacy hooks.log once per store: one HookRun with
// event "imported" and the file's last 4 KiB as its output is put, and only
// then is the file removed (§4.4). The row wins over a file that is still
// there, so a machine that has already migrated never re-reads it. It is a
// no-op with no Root.
func (l *KVLog) ensureImported() error {
	if l.Root == "" {
		return nil
	}
	l.importOnce.Do(func() { l.importErr = l.importFile() })
	return l.importErr
}

func (l *KVLog) importFile() error {
	path := filepath.Join(l.Root, legacyLogName)
	raw, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("hooks: read %s: %w", path, err)
	}

	if _, ok, err := l.KV.KVGet(runLogKey); err != nil {
		return err
	} else if ok {
		return nil
	}

	encoded, err := json.Marshal([]HookRun{{
		At:     fileModTime(path),
		Event:  "imported",
		Output: lastBytes(raw, importOutputCap),
	}})
	if err != nil {
		return fmt.Errorf("hooks: encode imported run: %w", err)
	}
	// Put, then remove, never the reverse: a crash between them leaves the
	// file, and the next run imports it again.
	if err := l.KV.KVPut(runLogKey, encoded); err != nil {
		return err
	}
	if err := os.Remove(path); err != nil {
		slog.Warn("hooks: could not remove imported run log", "path", path, "err", err)
	}
	return nil
}

// fileModTime stamps the imported run with the file's own time; a stat that
// fails reads as now.
func fileModTime(path string) time.Time {
	info, err := os.Stat(path)
	if err != nil {
		return time.Now().UTC()
	}
	return info.ModTime().UTC()
}

// lastBytes is raw's last n bytes, or all of it when it is shorter.
func lastBytes(raw []byte, n int) string {
	if len(raw) > n {
		raw = raw[len(raw)-n:]
	}
	return string(raw)
}
