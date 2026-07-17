//go:build windows

package core

import (
	"encoding/csv"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
)

const createNoWindow = 0x08000000 // CREATE_NO_WINDOW

// detachSysProcAttr returns process attributes that fully detach a child
// process on Windows: a new process group so it never dies with the parent's
// console or Ctrl+C events, and a hidden console (CREATE_NO_WINDOW) that is
// inherited by grandchildren, so no console window ever pops up. Note that
// DETACHED_PROCESS would be wrong here: a console-less child that spawns
// another console program makes Windows open a *visible* console for it.
func detachSysProcAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{
		CreationFlags: syscall.CREATE_NEW_PROCESS_GROUP | createNoWindow,
		HideWindow:    true,
	}
}

// hideWindow makes an exec.Cmd run without flashing a console window.
func hideWindow(cmd *exec.Cmd) *exec.Cmd {
	cmd.SysProcAttr = &syscall.SysProcAttr{
		CreationFlags: createNoWindow,
		HideWindow:    true,
	}
	return cmd
}

// listeningCandidates returns the local TCP listeners (one per port) using
// netstat -ano. Process names are resolved via a single tasklist snapshot.
func listeningCandidates() []portCandidate {
	out, err := hideWindow(exec.Command("netstat", "-ano", "-p", "TCP")).Output()
	if err != nil {
		return nil
	}
	names := taskNames()
	seen := map[int]bool{} // by port
	var candidates []portCandidate
	for line := range strings.Lines(string(out)) {
		fields := strings.Fields(line)
		// e.g.: TCP  127.0.0.1:4096  0.0.0.0:0  LISTENING  1234
		if len(fields) < 5 || fields[0] != "TCP" || fields[3] != "LISTENING" {
			continue
		}
		pid, err := strconv.Atoi(fields[4])
		if err != nil {
			continue
		}
		idx := strings.LastIndex(fields[1], ":")
		if idx < 0 {
			continue
		}
		port, err := strconv.Atoi(fields[1][idx+1:])
		if err != nil || seen[port] {
			continue
		}
		seen[port] = true
		candidates = append(candidates, portCandidate{pid: pid, port: port, command: names[pid]})
	}
	return candidates
}

// taskNames returns a pid -> process name map from one tasklist run.
func taskNames() map[int]string {
	out, err := hideWindow(exec.Command("tasklist", "/FO", "CSV", "/NH")).Output()
	if err != nil {
		return map[int]string{}
	}
	names := map[int]string{}
	r := csv.NewReader(strings.NewReader(string(out)))
	r.FieldsPerRecord = -1
	records, err := r.ReadAll()
	if err != nil {
		return names
	}
	for _, rec := range records {
		if len(rec) < 2 {
			continue
		}
		pid, err := strconv.Atoi(rec[1])
		if err != nil {
			continue
		}
		names[pid] = strings.TrimSuffix(rec[0], ".exe")
	}
	return names
}

// procName returns the process name for pid (without the .exe suffix), e.g.
// "opencode". Empty if the process does not exist.
func procName(pid int) string {
	out, err := hideWindow(exec.Command("tasklist",
		"/FI", fmt.Sprintf("PID eq %d", pid), "/FO", "CSV", "/NH")).Output()
	if err != nil {
		return ""
	}
	for line := range strings.Lines(string(out)) {
		if !strings.HasPrefix(line, `"`) {
			continue // "INFO: no tasks..." (localized) or blank
		}
		r := csv.NewReader(strings.NewReader(line))
		rec, err := r.Read()
		if err != nil || len(rec) < 1 {
			return ""
		}
		return strings.TrimSuffix(rec[0], ".exe")
	}
	return ""
}

// killProcess terminates the process with the given pid (and its children).
func killProcess(pid int) error {
	return hideWindow(exec.Command("taskkill", "/PID", strconv.Itoa(pid), "/T", "/F")).Run()
}

// findSSHTunnelPID returns the pid of the ssh process whose command line
// contains "-L <pattern>", if any.
func findSSHTunnelPID(pattern string) (int, bool) {
	// Pattern may contain user-provided SSH hostnames; escape single quotes
	// for PowerShell's single-quoted string syntax ('' represents one ').
	// String.Contains is an ordinal substring test, unlike -like, whose
	// wildcard syntax would misinterpret [ ] in hostnames (IPv6 literals).
	safe := strings.ReplaceAll(pattern, "'", "''")
	query := fmt.Sprintf(
		`Get-CimInstance Win32_Process -Filter "Name='ssh.exe'" | `+
			`Where-Object { $_.CommandLine -ne $null -and $_.CommandLine.Contains('-L %s') } | `+
			`Select-Object -First 1 -ExpandProperty ProcessId`, safe)
	out, err := hideWindow(exec.Command("powershell",
		"-NoProfile", "-NonInteractive", "-Command", query)).Output()
	if err != nil {
		return 0, false
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(out)))
	if err != nil {
		return 0, false
	}
	return pid, true
}
