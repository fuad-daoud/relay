//go:build unix

package main

import "syscall"

// reexec replaces this process's image with exe, keeping the pid and the
// children. It only returns on failure.
func reexec(exe string, argv, env []string) error {
	return syscall.Exec(exe, argv, env)
}
