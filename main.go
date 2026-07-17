package main

import (
	"fmt"
	"os"
	"runtime"

	"github.com/xay5421/ocm/internal/cli"
)

// The dashboard window (Cocoa on macOS in particular) must run on the
// process's main OS thread; locking here, before the scheduler moves the
// main goroutine, guarantees that.
func init() { runtime.LockOSThread() }

func main() {
	if err := cli.Run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}
