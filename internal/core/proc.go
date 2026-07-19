package core

import (
	"strconv"
	"strings"
)

// procEntry is one running process: pid plus full command line. Produced by
// the platform-specific sshProcesses implementations.
type procEntry struct {
	pid     int
	cmdline string
}

// parsePidLine parses a "<pid> <command line>" line as produced by the
// platform-specific process listings.
func parsePidLine(line string) (procEntry, bool) {
	line = strings.TrimSpace(line)
	pidStr, cmdline, found := strings.Cut(line, " ")
	if !found {
		pidStr = line
	}
	pid, err := strconv.Atoi(pidStr)
	if err != nil {
		return procEntry{}, false
	}
	return procEntry{pid: pid, cmdline: strings.TrimSpace(cmdline)}, true
}

// matchTunnel returns the pid of the first process whose command line
// contains "-L <pattern>" (the ssh forwarding argument ocm started it with).
func matchTunnel(procs []procEntry, pattern string) (int, bool) {
	needle := "-L " + pattern
	for _, p := range procs {
		if strings.Contains(p.cmdline, needle) {
			return p.pid, true
		}
	}
	return 0, false
}

// findSSHTunnelPID returns the pid of the ssh process whose command line
// contains "-L <pattern>", if any. For checking many hosts at once, use
// sshProcesses + matchTunnel to pay for a single process scan.
func findSSHTunnelPID(pattern string) (int, bool) {
	return matchTunnel(sshProcesses(), pattern)
}
