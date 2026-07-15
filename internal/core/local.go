package core

import (
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"
)

const localServePort = 14000

// LocalInstance is a discovered opencode server listening on this machine.
// Local servers may also live inside other processes (the VS Code extension
// embeds one), so they are discovered by probing listening ports instead of
// being configured.
type LocalInstance struct {
	PID     int    `json:"pid"`
	Port    int    `json:"port"`
	Command string `json:"command"` // process name, e.g. "opencode" or "Code Helper (Plugin)"
	Managed bool   `json:"managed"` // true if ocm may stop it (dedicated opencode process)
}

// portCandidate is a local TCP listener found by the platform-specific
// listeningCandidates implementation (lsof on Unix, netstat on Windows).
type portCandidate struct {
	pid     int
	port    int
	command string
}

// DiscoverLocalInstances finds opencode servers on this machine: every local
// listening TCP port (except ssh, whose forwards would proxy to remote
// servers) is health-probed to see whether an opencode server answers.
// password is used for the probe; unprotected servers ignore it.
func DiscoverLocalInstances(password string) []LocalInstance {
	var candidates []portCandidate
	for _, cand := range listeningCandidates() {
		// ssh listeners are tunnels/SOCKS proxies: a health probe would hit
		// a *remote* server through them, so they are never local instances.
		if cand.command == "ssh" || cand.command == "sshd" {
			continue
		}
		candidates = append(candidates, cand)
	}

	// Probe all candidates concurrently; keep the ones that answer like an
	// opencode server.
	results := make([]*LocalInstance, len(candidates))
	done := make(chan struct{}, len(candidates))
	for i, cand := range candidates {
		go func(i int, cand portCandidate) {
			defer func() { done <- struct{}{} }()
			c := NewClient(fmt.Sprintf("http://127.0.0.1:%d", cand.port), password)
			if _, ok := c.Health(); !ok {
				return
			}
			command := procName(cand.pid)
			if command == "" {
				command = cand.command
			}
			results[i] = &LocalInstance{
				PID:     cand.pid,
				Port:    cand.port,
				Command: command,
				Managed: strings.Contains(strings.ToLower(command), "opencode"),
			}
		}(i, cand)
	}
	for range candidates {
		<-done
	}
	var instances []LocalInstance
	for _, r := range results {
		if r != nil {
			instances = append(instances, *r)
		}
	}
	sort.Slice(instances, func(i, j int) bool { return instances[i].Port < instances[j].Port })
	return instances
}

// envWithout returns env with all entries for the given variable removed.
// (With duplicate entries, getenv implementations disagree on which one
// wins, so overriding requires removing the old entry first.)
func envWithout(env []string, name string) []string {
	out := env[:0:0]
	prefix := name + "="
	for _, e := range env {
		if strings.HasPrefix(e, prefix) {
			continue
		}
		out = append(out, e)
	}
	return out
}

// EnvWithPassword returns the current environment with
// OPENCODE_SERVER_PASSWORD set to password (unchanged if password is empty).
// Used when handing off to `opencode attach` / `opencode run --attach`,
// which read the password from that variable.
func EnvWithPassword(password string) []string {
	env := os.Environ()
	if password == "" {
		return env
	}
	return append(envWithout(env, "OPENCODE_SERVER_PASSWORD"),
		"OPENCODE_SERVER_PASSWORD="+password)
}

// LocalOpencodeBin returns the path of the local opencode binary.
func LocalOpencodeBin() (string, error) {
	if bin, err := exec.LookPath("opencode"); err == nil {
		return bin, nil
	}
	name := "opencode"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	home, _ := os.UserHomeDir()
	fallback := filepath.Join(home, ".opencode", "bin", name)
	if _, err := os.Stat(fallback); err == nil {
		return fallback, nil
	}
	return "", fmt.Errorf("opencode binary not found in PATH")
}

// StartLocalServe starts a new local `opencode serve` on a fixed port and
// waits until it is healthy.
func (m *Manager) StartLocalServe() (HostState, error) {
	bin, err := LocalOpencodeBin()
	if err != nil {
		return HostState{}, err
	}
	port := localServePort
	password := m.Config.LocalPassword
	url := fmt.Sprintf("http://127.0.0.1:%d", port)
	for _, inst := range DiscoverLocalInstances(password) {
		if inst.Port == port {
			c := NewClient(url, password)
			version, ok := c.Health()
			if !ok {
				break
			}
			return HostState{Name: "local", Local: true, URL: url, Healthy: true,
				Version: version, Command: inst.Command, Managed: inst.Managed, PID: inst.PID}, nil
		}
	}
	l, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
	if err != nil {
		return HostState{}, fmt.Errorf("local port %d is already in use", port)
	}
	l.Close()
	m.logf("starting local opencode serve on port %d", port)
	home, _ := os.UserHomeDir()
	logFile, err := os.OpenFile(filepath.Join(home, ".opencode-serve.log"),
		os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return HostState{}, err
	}
	defer logFile.Close()
	cmd := exec.Command(bin, "serve",
		"--port", strconv.Itoa(port), "--hostname", "127.0.0.1")
	if password != "" {
		cmd.Env = append(envWithout(os.Environ(), "OPENCODE_SERVER_PASSWORD"),
			"OPENCODE_SERVER_PASSWORD="+password)
	}
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	cmd.Stdin = nil
	// Fully detach: new session (Unix) / detached process group (Windows) so
	// the server never dies with ocm's process group or controlling terminal.
	cmd.SysProcAttr = detachSysProcAttr()
	if err := cmd.Start(); err != nil {
		return HostState{}, fmt.Errorf("start local serve: %w", err)
	}
	pid := cmd.Process.Pid
	go cmd.Wait() // reap when it eventually exits

	deadline := time.Now().Add(30 * time.Second)
	c := NewClient(url, password)
	for {
		if v, ok := c.Health(); ok {
			return HostState{Name: "local", Local: true, URL: url, Healthy: true,
				Version: v, Command: "opencode", Managed: true, PID: pid}, nil
		}
		if time.Now().After(deadline) {
			return HostState{}, fmt.Errorf("local serve on port %d did not become healthy (check ~/.opencode-serve.log)", port)
		}
		time.Sleep(300 * time.Millisecond)
	}
}

// StopLocalServe terminates a local opencode server by pid, after verifying
// the pid actually belongs to an opencode process.
func (m *Manager) StopLocalServe(pid int) error {
	command := procName(pid)
	if command == "" {
		return fmt.Errorf("pid %d not found", pid)
	}
	if !strings.Contains(strings.ToLower(command), "opencode") {
		return fmt.Errorf("pid %d is not an opencode process (%s)", pid, command)
	}
	m.logf("stopping local opencode server (pid %d)", pid)
	return killProcess(pid)
}

// RestartLocal stops a managed local server by pid and starts a fresh one on
// the fixed local port.
func (m *Manager) RestartLocal(pid int) (HostState, error) {
	if err := m.StopLocalServe(pid); err != nil {
		return HostState{}, err
	}
	// Wait until the process is gone so its port is released.
	deadline := time.Now().Add(10 * time.Second)
	for {
		if procName(pid) == "" {
			break
		}
		if time.Now().After(deadline) {
			return HostState{}, fmt.Errorf("pid %d did not exit", pid)
		}
		time.Sleep(300 * time.Millisecond)
	}
	return m.StartLocalServe()
}

// SnapshotLocal probes all discovered local instances. Only instances that
// answer the health check are returned (the TUI's server also matches).
func (m *Manager) SnapshotLocal(withSessions bool, sessionLimit int) []HostState {
	var states []HostState
	password := m.Config.LocalPassword
	for _, inst := range DiscoverLocalInstances(password) {
		url := fmt.Sprintf("http://127.0.0.1:%d", inst.Port)
		c := NewClient(url, password)
		version, ok := c.Health()
		if !ok {
			continue
		}
		st := HostState{
			Name:    "local",
			Local:   true,
			PID:     inst.PID,
			Command: inst.Command,
			Managed: inst.Managed,
			URL:     url,
			Healthy: true,
			Version: version,
		}
		if withSessions {
			fillSessions(c, &st, sessionLimit)
		}
		states = append(states, st)
	}
	return states
}
