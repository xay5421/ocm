package core

import (
	"context"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/xay5421/ocm/internal/config"
)

// Manager orchestrates tunnels and remote opencode servers for a set of hosts.
type Manager struct {
	Config *config.Config
	// Log receives human-readable progress messages; may be nil.
	Log func(format string, args ...any)
}

func (m *Manager) logf(format string, args ...any) {
	if m.Log != nil {
		m.Log(format, args...)
	}
}

// localURL returns the base URL of a server reachable on a local port.
func localURL(port int) string {
	return fmt.Sprintf("http://127.0.0.1:%d", port)
}

// BaseURL returns the local URL through which the host's opencode server is
// reachable (via the ssh tunnel, or directly for the local host).
func BaseURL(h config.Host) string {
	return localURL(h.LocalPort)
}

// pollUntil calls cond every interval until it returns true or timeout
// elapses, and reports whether cond ever succeeded.
func pollUntil(timeout, interval time.Duration, cond func() bool) bool {
	deadline := time.Now().Add(timeout)
	for {
		if cond() {
			return true
		}
		if time.Now().After(deadline) {
			return false
		}
		time.Sleep(interval)
	}
}

// sshBaseOpts returns the ssh options shared by every ssh invocation ocm
// makes:
//   - BatchMode: ocm's ssh runs detached or non-interactive, so interactive
//     auth would hang silently; fail with a clear error instead.
//   - ControlMaster=no / ControlPath=none: stay out of any ssh connection
//     multiplexing (e.g. ControlMaster auto in ~/.ssh/config). Otherwise an
//     ocm tunnel could become the mux master that other clients (VSCode
//     Remote-SSH, ...) attach to, and StopTunnel's kill would tear down
//     their connections too - or, attached to someone else's master, ocm
//     would eat into that connection's sshd MaxSessions channel quota.
func sshBaseOpts() []string {
	return []string{
		"-o", "BatchMode=yes",
		"-o", "ConnectTimeout=10",
		"-o", "ControlMaster=no",
		"-o", "ControlPath=none",
	}
}

// tunnelPattern is the ssh argument we can pgrep for.
func tunnelPattern(h config.Host) string {
	return fmt.Sprintf("%d:127.0.0.1:%d %s", h.LocalPort, h.RemotePort, h.SSH)
}

// TunnelPID returns the pid of the ssh tunnel process for h, if running.
func TunnelPID(h config.Host) (int, bool) {
	return findSSHTunnelPID(tunnelPattern(h))
}

// StartTunnel starts a background ssh tunnel for h. It is a no-op if one is
// already running.
//
// ssh's own -f (daemonize after auth) is deliberately not used: Windows
// OpenSSH never actually detaches with -f, so reading the command's output
// would block forever. Instead a plain `ssh -N` is started fully detached,
// with its output going to a log file, and ocm polls until the forwarded
// local port accepts connections.
func (m *Manager) StartTunnel(h config.Host) error {
	if _, ok := TunnelPID(h); ok {
		return nil
	}
	// Fail fast if another process already holds the local port. Without
	// this check the readiness dial below would mistake that process for
	// the tunnel and report success while ssh itself dies from
	// ExitOnForwardFailure.
	probe, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", h.LocalPort))
	if err != nil {
		return fmt.Errorf("local port %d is already in use by another process", h.LocalPort)
	}
	probe.Close()
	m.logf("starting ssh tunnel 127.0.0.1:%d -> %s:%d", h.LocalPort, h.SSH, h.RemotePort)
	logPath := tunnelLogPath(h)
	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer logFile.Close()
	args := append(sshBaseOpts(),
		"-N",
		"-o", "ExitOnForwardFailure=yes",
		"-o", "ServerAliveInterval=30",
		"-o", "ServerAliveCountMax=3",
		"-L", fmt.Sprintf("%d:127.0.0.1:%d", h.LocalPort, h.RemotePort),
		h.SSH,
	)
	cmd := exec.Command("ssh", args...)
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	cmd.SysProcAttr = detachSysProcAttr()
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start ssh: %w", err)
	}
	exited := make(chan error, 1)
	go func() { exited <- cmd.Wait() }()

	deadline := time.Now().Add(30 * time.Second)
	for {
		select {
		case <-exited:
			return fmt.Errorf("ssh tunnel to %s failed: %s", h.SSH, tailOfFile(logPath))
		default:
		}
		// The local forward port starts accepting once ssh has connected,
		// authenticated, and bound the forwarding listener.
		if conn, err := net.DialTimeout("tcp",
			fmt.Sprintf("127.0.0.1:%d", h.LocalPort), time.Second); err == nil {
			conn.Close()
			// Re-check that ssh did not exit between the select above and
			// the successful dial (e.g. it died right after binding).
			select {
			case <-exited:
				return fmt.Errorf("ssh tunnel to %s failed: %s", h.SSH, tailOfFile(logPath))
			default:
			}
			return nil
		}
		if time.Now().After(deadline) {
			_ = cmd.Process.Kill()
			return fmt.Errorf("ssh tunnel to %s did not come up within 30s: %s",
				h.SSH, tailOfFile(logPath))
		}
		time.Sleep(300 * time.Millisecond)
	}
}

// tunnelLogPath is where the ssh tunnel for h writes its output.
func tunnelLogPath(h config.Host) string {
	return filepath.Join(os.TempDir(), fmt.Sprintf("ocm-tunnel-%d.log", h.LocalPort))
}

// tailOfFile returns the (trimmed) tail of a small log file for error
// messages, or a placeholder if it is empty or unreadable.
func tailOfFile(path string) string {
	data, err := os.ReadFile(path)
	if err != nil || len(strings.TrimSpace(string(data))) == 0 {
		return "(no ssh output, see " + path + ")"
	}
	s := strings.TrimSpace(string(data))
	if len(s) > 500 {
		s = "..." + s[len(s)-500:]
	}
	return s
}

// StopTunnel kills the ssh tunnel for h if running.
func (m *Manager) StopTunnel(h config.Host) error {
	pid, ok := TunnelPID(h)
	if !ok {
		return nil
	}
	m.logf("stopping ssh tunnel (pid %d)", pid)
	return killProcess(pid)
}

// remoteOpencodePath returns h.Opencode as a double-quoted string for use
// inside a remote shell command, with a leading ~ rewritten to $HOME so it
// still expands inside the quotes. h.Opencode is validated at config load
// time to contain only safe characters.
func remoteOpencodePath(h config.Host) string {
	p := strings.TrimSpace(h.Opencode)
	if strings.HasPrefix(p, "~/") {
		p = "$HOME/" + p[2:]
	} else if p == "~" {
		p = "$HOME"
	}
	return `"` + p + `"`
}

// UpgradeOpencode runs `opencode upgrade` on the remote host and returns the
// combined output, ending in a "version: X.Y.Z" line with the binary's
// version after the upgrade. A running `opencode serve` keeps executing the
// old version until it is restarted.
//
// Note: `opencode upgrade` only works for binaries installed via the
// official install script; for package-manager installs (npm, brew, ...) it
// prints an explanatory message, which is passed through to the caller.
func (m *Manager) UpgradeOpencode(h config.Host) (string, error) {
	bin := remoteOpencodePath(h)
	upgradeCmd := fmt.Sprintf(`%s upgrade 2>&1 && printf 'version: ' && %s --version 2>&1`, bin, bin)
	m.logf("running opencode upgrade on %s", h.SSH)
	// Generous deadline: the upgrade downloads a new binary.
	ctx, cancel := context.WithTimeout(context.Background(), 180*time.Second)
	defer cancel()
	cmd := hideWindow(exec.CommandContext(ctx, "ssh",
		append(sshBaseOpts(), h.SSH, upgradeCmd)...))
	out, err := cmd.CombinedOutput()
	if ctx.Err() != nil {
		return "", fmt.Errorf("opencode upgrade on %s timed out after 180s", h.SSH)
	}
	if err != nil {
		return "", fmt.Errorf("opencode upgrade on %s failed: %v: %s",
			h.SSH, err, strings.TrimSpace(string(out)))
	}
	return strings.TrimSpace(string(out)), nil
}

// StartServe launches `opencode serve` on the remote host in the background.
// It is safe to call when a server is already running: the command checks the
// port first.
func (m *Manager) StartServe(h config.Host) error {
	// The password is fed through stdin (read -r) so it never appears on the
	// remote command line / in remote `ps` output. The health pre-check
	// treats 200 (healthy) and 401 (running, password-protected) both as
	// "already running", so it needs no credentials.
	servePath := remoteOpencodePath(h)
	serveCmd := fmt.Sprintf(
		`IFS= read -r OCM_PW; `+
			`code=$(curl -s -o /dev/null -m 2 -w '%%{http_code}' http://127.0.0.1:%d/global/health 2>/dev/null); `+
			`if [ "$code" != 200 ] && [ "$code" != 401 ]; then `+
			`if [ -n "$OCM_PW" ]; then export OPENCODE_SERVER_PASSWORD="$OCM_PW"; fi; `+
			`nohup %s serve --port %d --hostname 127.0.0.1 >>"$HOME/.opencode-serve.log" 2>&1 </dev/null & fi`,
		h.RemotePort, servePath, h.RemotePort)
	m.logf("starting opencode serve on %s (port %d)", h.SSH, h.RemotePort)
	// Hard deadline: a wedged remote host can hang an ssh session forever
	// (past ConnectTimeout, which only covers the TCP connect), which would
	// in turn hang whoever called Up (CLI command or dashboard request).
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	cmd := hideWindow(exec.CommandContext(ctx, "ssh",
		append(sshBaseOpts(), h.SSH, serveCmd)...))
	cmd.Stdin = strings.NewReader(h.Password + "\n")
	out, err := cmd.CombinedOutput()
	if ctx.Err() != nil {
		return fmt.Errorf("start serve on %s timed out after 60s", h.SSH)
	}
	if err != nil {
		return fmt.Errorf("start serve failed: %v: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// StopServe kills opencode serve on the remote host.
func (m *Manager) StopServe(h config.Host) error {
	// Two alternatives: the first matches a binary named "opencode" however
	// it was started; the second matches whatever StartServe launched (the
	// configured binary may have any name, e.g. a wrapper script, but ocm
	// always passes --hostname 127.0.0.1). The [o]/[s] brackets keep the
	// pattern from matching the remote shell that runs this very pkill.
	killCmd := fmt.Sprintf(
		`pkill -f "[o]pencode serve --port %d|[s]erve --port %d --hostname 127.0.0.1" || true`,
		h.RemotePort, h.RemotePort)
	m.logf("stopping opencode serve on %s", h.SSH)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	err := hideWindow(exec.CommandContext(ctx, "ssh",
		append(sshBaseOpts(), h.SSH, killCmd)...)).Run()
	if ctx.Err() != nil {
		return fmt.Errorf("stop serve on %s timed out after 30s", h.SSH)
	}
	return err
}

// Down disconnects a remote host: the tunnel is stopped and the remote server
// keeps running unless stopServe is set.
func (m *Manager) Down(h config.Host, stopServe bool) error {
	if stopServe {
		if err := m.StopServe(h); err != nil {
			return err
		}
	}
	return m.StopTunnel(h)
}

// WaitHealthy polls the server via the tunnel until healthy or timeout.
func WaitHealthy(h config.Host, timeout time.Duration) (string, bool) {
	c := NewClient(BaseURL(h), h.Password)
	var version string
	ok := pollUntil(timeout, 500*time.Millisecond, func() bool {
		v, ok := c.Health()
		version = v
		return ok
	})
	return version, ok
}

// Up makes the host reachable: tunnel up + remote server running. Returns the
// server version.
func (m *Manager) Up(h config.Host) (string, error) {
	if err := m.StartTunnel(h); err != nil {
		return "", err
	}
	if v, ok := WaitHealthy(h, 2*time.Second); ok {
		return v, nil
	}
	if err := m.StartServe(h); err != nil {
		return "", err
	}
	v, ok := WaitHealthy(h, 30*time.Second)
	if !ok {
		return "", fmt.Errorf("server on %s did not become healthy (check ~/.opencode-serve.log on that machine)", h.SSH)
	}
	return v, nil
}

// RestartServe restarts opencode serve on the remote host (e.g. after its
// config changed) and waits until it is healthy again.
func (m *Manager) RestartServe(h config.Host) (string, error) {
	if err := m.StopServe(h); err != nil {
		return "", err
	}
	// Wait for the old server to actually go away, otherwise StartServe's
	// health pre-check would see the dying instance and skip the start.
	c := NewClient(BaseURL(h), h.Password)
	stopped := pollUntil(10*time.Second, 300*time.Millisecond, func() bool {
		_, ok := c.Health()
		return !ok
	})
	if !stopped {
		return "", fmt.Errorf("old server on %s did not stop", h.SSH)
	}
	return m.Up(h)
}

// HostState is a point-in-time snapshot of one host, used by list/status
// and served to the dashboard as JSON.
type HostState struct {
	Name      string        `json:"name"`
	SSH       string        `json:"ssh,omitempty"`
	URL       string        `json:"url"`
	Local     bool          `json:"local"`
	PID       int           `json:"pid,omitempty"`
	Command   string        `json:"command,omitempty"` // local: owning process name
	Managed   bool          `json:"managed,omitempty"` // local: ocm may stop it
	Tunnel    bool          `json:"tunnel"`
	TunnelPID int           `json:"tunnel_pid,omitempty"`
	Healthy   bool          `json:"healthy"`
	Version   string        `json:"version,omitempty"`
	Sessions  []SessionInfo `json:"sessions,omitempty"`
	Error     string        `json:"error,omitempty"`
}

// SessionInfo is a session together with its live status.
type SessionInfo struct {
	ID        string `json:"id"`
	Title     string `json:"title"`
	Directory string `json:"directory"`
	Status    string `json:"status,omitempty"`
	Updated   int64  `json:"updated"`
}

// fillSessions populates st.Sessions from the server behind c.
func fillSessions(c *Client, st *HostState, sessionLimit int) {
	sessions, err := c.Sessions()
	if err != nil {
		st.Error = err.Error()
		return
	}
	status, err := c.SessionStatus()
	if err != nil {
		status = map[string]string{}
	}
	for i, s := range sessions {
		if sessionLimit > 0 && i >= sessionLimit {
			break
		}
		st.Sessions = append(st.Sessions, SessionInfo{
			ID:        s.ID,
			Title:     s.Title,
			Directory: s.Directory,
			Status:    status[s.ID],
			Updated:   s.Time.Updated,
		})
	}
}

// snapshotWithTunnel inspects one remote host without changing anything,
// with the tunnel pid already resolved, so SnapshotAll can look up all
// tunnels with a single process scan.
func (m *Manager) snapshotWithTunnel(name string, h config.Host, tunnelPID int, tunnel bool,
	withSessions bool, sessionLimit int) HostState {
	st := HostState{Name: name, SSH: h.SSH, URL: BaseURL(h)}
	if tunnel {
		st.Tunnel = true
		st.TunnelPID = tunnelPID
	}
	c := NewClient(st.URL, h.Password)
	version, ok := c.Health()
	st.Healthy = ok
	st.Version = version
	if ok && withSessions {
		fillSessions(c, &st, sessionLimit)
	}
	return st
}

// SnapshotAll inspects all hosts concurrently: auto-discovered local
// instances first, then the configured remote hosts. Tunnel pids for all
// hosts are resolved from one process scan (on Windows each scan spawns a
// PowerShell process, so per-host scans would dominate the snapshot cost).
func (m *Manager) SnapshotAll(withSessions bool, sessionLimit int) []HostState {
	names := m.Config.Names()
	states := make([]HostState, len(names))
	done := make(chan struct{}, len(names)+1)
	var local []HostState
	go func() {
		local = m.SnapshotLocal(withSessions, sessionLimit)
		done <- struct{}{}
	}()
	var procs []procEntry
	if len(names) > 0 {
		procs = sshProcesses()
	}
	for i, name := range names {
		go func(i int, name string) {
			h := m.Config.Hosts[name]
			pid, ok := matchTunnel(procs, tunnelPattern(h))
			states[i] = m.snapshotWithTunnel(name, h, pid, ok, withSessions, sessionLimit)
			done <- struct{}{}
		}(i, name)
	}
	for range len(names) + 1 {
		<-done
	}
	return append(local, states...)
}
