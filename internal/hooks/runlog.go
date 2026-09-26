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

const (
	runLogKey       = "hooks.log"
	runLogCap       = 200
	importOutputCap = 4 << 10
	legacyLogName   = "hooks.log"
)

// KVLog is a RunLog over the machine database's kv row, capped at runLogCap.
type KVLog struct {
	KV db.DBTxKV
	// Root is the state root a legacy hooks.log is imported from once; ""
	// imports nothing.
	Root string

	importOnce sync.Once
	importErr  error
}

var _ RunLog = (*KVLog)(nil)

func NewKVLog(kv db.DBTxKV, root string) *KVLog { return &KVLog{KV: kv, Root: root} }

// Append adds run, capping the stored array at runLogCap, in one transaction.
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

// ensureImported adopts a legacy hooks.log once per store: a row already
// present always wins over the file, so a migrated machine never re-reads it.
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
	// Put before remove: a crash between them just re-imports next run.
	if err := l.KV.KVPut(runLogKey, encoded); err != nil {
		return err
	}
	if err := os.Remove(path); err != nil {
		slog.Warn("hooks: could not remove imported run log", "path", path, "err", err)
	}
	return nil
}

func fileModTime(path string) time.Time {
	info, err := os.Stat(path)
	if err != nil {
		return time.Now().UTC()
	}
	return info.ModTime().UTC()
}

func lastBytes(raw []byte, n int) string {
	if len(raw) > n {
		raw = raw[len(raw)-n:]
	}
	return string(raw)
}
