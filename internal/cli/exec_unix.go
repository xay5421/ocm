//go:build !windows

package cli

import "syscall"

// execReplace replaces the current process with bin (execve). It only
// returns on error.
func execReplace(bin string, argv []string, env []string) error {
	return syscall.Exec(bin, argv, env)
}
