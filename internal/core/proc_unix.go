//go:build !windows

package core

import (
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"syscall"
)

// detachSysProcAttr returns process attributes that fully detach a child
// process: a new session so it never dies with the parent's process group or
// controlling terminal.
func detachSysProcAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{Setsid: true}
}

// hideWindow is a no-op on Unix; on Windows it prevents console popups.
func hideWindow(cmd *exec.Cmd) *exec.Cmd { return cmd }

// listeningCandidates returns the local TCP listeners (one per port) using
// lsof. ssh listeners are excluded by the caller via the command name.
func listeningCandidates() []portCandidate {
	out, err := exec.Command("lsof", "-nP", "-iTCP", "-sTCP:LISTEN").Output()
	if err != nil {
		return nil
	}
	seen := map[int]bool{} // by port
	var candidates []portCandidate
	for line := range strings.Lines(string(out)) {
		fields := strings.Fields(line)
		if len(fields) < 9 {
			continue
		}
		command := lsofCommand(fields[0])
		pid, err := strconv.Atoi(fields[1])
		if err != nil {
			continue
		}
		addr := fields[len(fields)-2] // NAME column, e.g. 127.0.0.1:4096 or *:4096
		if fields[len(fields)-1] != "(LISTEN)" {
			addr = fields[len(fields)-1]
		}
		idx := strings.LastIndex(addr, ":")
		if idx < 0 {
			continue
		}
		port, err := strconv.Atoi(strings.TrimSuffix(addr[idx+1:], "(LISTEN)"))
		if err != nil || seen[port] {
			continue
		}
		seen[port] = true
		candidates = append(candidates, portCandidate{pid: pid, port: port, command: command})
	}
	return candidates
}

// lsofCommand decodes the COMMAND column of lsof output (spaces are shown as
// \x20).
func lsofCommand(s string) string {
	return strings.ReplaceAll(s, `\x20`, " ")
}

// procName returns the base name of the process command for pid, e.g.
// "opencode" or "Code Helper (Plugin)". Empty if the process does not exist.
func procName(pid int) string {
	out, err := exec.Command("ps", "-p", strconv.Itoa(pid), "-o", "comm=").Output()
	if err != nil {
		return ""
	}
	comm := strings.TrimSpace(string(out))
	if idx := strings.LastIndex(comm, "/"); idx >= 0 {
		comm = comm[idx+1:]
	}
	return comm
}

// killProcess terminates the process with the given pid (SIGTERM).
func killProcess(pid int) error {
	return syscall.Kill(pid, syscall.SIGTERM)
}

// findSSHTunnelPID returns the pid of the ssh process whose command line
// contains "-L <pattern>", if any.
func findSSHTunnelPID(pattern string) (int, bool) {
	// Quote regex metacharacters so that SSH hostnames containing dots or
	// other regex-special characters are matched literally.
	out, err := exec.Command("pgrep", "-f", "ssh.*-L "+regexp.QuoteMeta(pattern)).Output()
	if err != nil {
		return 0, false
	}
	fields := strings.Fields(string(out))
	if len(fields) == 0 {
		return 0, false
	}
	pid, err := strconv.Atoi(fields[0])
	if err != nil {
		return 0, false
	}
	return pid, true
}
