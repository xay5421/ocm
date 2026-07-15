//go:build !windows

package cli

import "os"

// launchedFromGUI reports whether ocm was started from a graphical shell
// (e.g. double-clicked in a Linux file manager) rather than a terminal:
// stdout is not a tty and no TERM is set.
//
// Note: on macOS, Finder runs command-line binaries inside a Terminal window
// (a real tty), so a Finder double-click shows the usage text instead.
func launchedFromGUI() bool {
	fi, err := os.Stdout.Stat()
	if err != nil {
		return false
	}
	return fi.Mode()&os.ModeCharDevice == 0 && os.Getenv("TERM") == ""
}

// freeConsole is a no-op outside Windows: GUI launches have no terminal
// window to hide in the first place.
func freeConsole() {}
