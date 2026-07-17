// Command ocm-dashboard opens the ocm dashboard in a native window.
//
// It exists for double-click use on Windows: built with
// -ldflags -H=windowsgui it runs in the GUI subsystem, so no console window
// flashes (the main ocm.exe is a console app and always briefly shows one).
package main

import (
	"fmt"
	"os"
	"runtime"

	"github.com/xay5421/ocm/internal/cli"
)

// The dashboard window must run on the process's main OS thread.
func init() { runtime.LockOSThread() }

func main() {
	if err := cli.Run([]string{"dashboard"}); err != nil {
		// There is usually no console to see this; best effort.
		fmt.Fprintln(os.Stderr, "ocm:", err)
		os.Exit(1)
	}
}
