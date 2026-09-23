//go:build !unix

package upgrade

import (
	"errors"

	"github.com/fuad-daoud/relevo/internal/store"
)

// ExeIdentity has no implementation off unix: there is no dev/ino to read, and
// the daemon cannot re-exec there anyway.
func ExeIdentity(string) (store.FileID, error) {
	return store.FileID{}, errors.ErrUnsupported
}
