//go:build !windows

package cli

// launchedFromGUI reports whether ocm was started from a graphical shell
// rather than a terminal. Only Windows has this detection (double-clicking
// ocm.exe): on macOS the app bundle passes an explicit `dashboard` argument
// (see tools/makeapp), and on Linux there is no native dashboard window, so
// a GUI launch would only leave a headless server behind.
func launchedFromGUI() bool { return false }

// freeConsole is a no-op outside Windows.
func freeConsole() {}
