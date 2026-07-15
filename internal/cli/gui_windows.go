//go:build windows

package cli

import (
	"syscall"
	"unsafe"
)

// launchedFromGUI reports whether ocm was started by double-clicking it in
// Explorer (or an equivalent GUI launch) rather than from a shell.
//
// When Explorer starts a console program it allocates a fresh console that is
// owned by that process alone, so GetConsoleProcessList returns exactly one
// pid. When started from cmd/PowerShell/Windows Terminal, the shell is
// attached to the same console too, so the count is >= 2.
func launchedFromGUI() bool {
	kernel32 := syscall.NewLazyDLL("kernel32.dll")
	proc := kernel32.NewProc("GetConsoleProcessList")
	if err := proc.Find(); err != nil {
		return false
	}
	pids := make([]uint32, 4)
	n, _, _ := proc.Call(uintptr(unsafe.Pointer(&pids[0])), uintptr(len(pids)))
	return n == 1
}
