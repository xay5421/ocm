//go:build windows

package cli

import (
	"os"
	"os/exec"
)

// execReplace approximates Unix execve on Windows: the child runs with
// inherited stdio and environment, and its exit code becomes ours. It only
// returns on error (when the child could not be started).
func execReplace(bin string, argv []string, env []string) error {
	cmd := exec.Command(bin, argv[1:]...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Env = env
	if err := cmd.Run(); err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			os.Exit(exitErr.ExitCode())
		}
		return err
	}
	os.Exit(0)
	return nil
}
