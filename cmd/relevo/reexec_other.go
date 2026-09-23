//go:build !unix

package main

import "errors"

// reexec has no implementation off unix. The daemon never installs the upgrade
// hook there, so this is unreachable in practice; it keeps the tree compiling.
func reexec(exe string, argv, env []string) error {
	return errors.ErrUnsupported
}
