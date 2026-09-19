package classify

import (
	"fmt"
	"os"
)

// KeyFileUsable says whether a key file with these permission bits may be
// read. Any of the group or other bits set (mode & 0o077 != 0) refuses,
// with a reason the user can act on: `typesafe.key is readable by others
// (mode 0644); chmod 600 it`. Pure.
func KeyFileUsable(mode os.FileMode) (ok bool, reason string) {
	if mode.Perm()&0o077 != 0 {
		return false, fmt.Sprintf("typesafe.key is readable by others (mode 0%o); chmod 600 it", mode.Perm())
	}
	return true, ""
}
