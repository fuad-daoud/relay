package store

import (
	"fmt"
	"path/filepath"
	"time"

	"github.com/fuad-daoud/relevo/internal/db"
)

// PutRoundFile writes one round file of a live binding directly as a
// round_file row, with no file on disk: the artifacts relevo produces without
// ever writing bytes to the binding directory (a drift or diff patch, a
// consult's findings). It is a Tx method, so the state lock is already held.
//
// A path outside the binding's directory, a basename without the round-file
// NNN- prefix, a mismatched NNN- or a round below 1 is refused with nothing
// written. A name with no live record is an ErrNotFound error, never a silent
// no-op: an unrecorded put would vanish, which is what writing a file never did.
func (t *Tx) PutRoundFile(name string, round int, path string, body []byte) error {
	if filepath.Dir(path) != t.s.Dir(name) {
		return fmt.Errorf("put round file %s: not in binding %q's directory", path, name)
	}
	base := filepath.Base(path)
	if !roundBaseRe.MatchString(base) {
		return fmt.Errorf("put round file %s: base %q is not a round file name", path, base)
	}
	if r, ok := roundOfFile(base); !ok || r != round {
		return fmt.Errorf("put round file %s: base %q is round %d, not %d", path, base, r, round)
	}
	if round < 1 {
		return fmt.Errorf("put round file %s: round %d is below 1", path, round)
	}

	d, err := t.s.dbForWrite()
	if err != nil {
		return err
	}
	rec, ok, err := d.RecordGet(t.s.owner, name)
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("%s: %w", name, ErrNotFound)
	}

	now := time.Now().UTC()
	return d.Tx(func(dtx *db.Tx) error {
		return dtx.RoundFilePut(rec.ID, base, round, body, now, now)
	})
}
