package core

import (
	"fmt"
	"os/exec"
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

// BaseURL returns the local URL through which the host's opencode server is
// reachable (via the ssh tunnel, or directly for the local host).
func BaseURL(h config.Host) string {
	return fmt.Sprintf("http://127.0.0.1:%d", h.LocalPort)
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
func (m *Manager) StartTunnel(h config.Host) error {
	if _, ok := TunnelPID(h); ok {
		return nil
	}
	m.logf("starting ssh tunnel 127.0.0.1:%d -> %s:%d", h.LocalPort, h.SSH, h.RemotePort)
	cmd := hideWindow(exec.Command("ssh",
		"-f", "-N",
		"-o", "ExitOnForwardFailure=yes",
		"-o", "ConnectTimeout=10",
		"-o", "ServerAliveInterval=30",
		"-o", "ServerAliveCountMax=3",
		"-L", fmt.Sprintf("%d:127.0.0.1:%d", h.LocalPort, h.RemotePort),
		h.SSH,
	))
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("ssh tunnel to %s failed: %v: %s", h.SSH, err, strings.TrimSpace(string(out)))
	}
	return nil
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

// StartServe launches `opencode serve` on the remote host in the background.
// It is safe to call when a server is already running: the command checks the
// port first.
func (m *Manager) StartServe(h config.Host) error {
	// The password is fed through stdin (read -r) so it never appears on the
	// remote command line / in remote `ps` output. The health pre-check
	// treats 200 (healthy) and 401 (running, password-protected) both as
	// "already running", so it needs no credentials.
	serveCmd := fmt.Sprintf(
		`IFS= read -r OCM_PW; `+
			`code=$(curl -s -o /dev/null -m 2 -w '%%{http_code}' http://127.0.0.1:%d/global/health 2>/dev/null); `+
			`if [ "$code" != 200 ] && [ "$code" != 401 ]; then `+
			`if [ -n "$OCM_PW" ]; then export OPENCODE_SERVER_PASSWORD="$OCM_PW"; fi; `+
			`nohup %s serve --port %d --hostname 127.0.0.1 >>"$HOME/.opencode-serve.log" 2>&1 </dev/null & fi`,
		h.RemotePort, h.Opencode, h.RemotePort)
	m.logf("starting opencode serve on %s (port %d)", h.SSH, h.RemotePort)
	cmd := hideWindow(exec.Command("ssh",
		"-o", "ConnectTimeout=10",
		"-o", "BatchMode=yes",
		h.SSH, serveCmd))
	cmd.Stdin = strings.NewReader(h.Password + "\n")
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("start serve failed: %v: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// StopServe kills opencode serve on the remote host.
func (m *Manager) StopServe(h config.Host) error {
	// The [o] bracket keeps the pattern from matching the remote shell that
	// runs this very pkill command.
	killCmd := fmt.Sprintf(`pkill -f "[o]pencode serve --port %d" || true`, h.RemotePort)
	m.logf("stopping opencode serve on %s", h.SSH)
	return hideWindow(exec.Command("ssh", "-o", "ConnectTimeout=10", "-o", "BatchMode=yes", h.SSH, killCmd)).Run()
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
	deadline := time.Now().Add(timeout)
	for {
		if v, ok := c.Health(); ok {
			return v, true
		}
		if time.Now().After(deadline) {
			return "", false
		}
		time.Sleep(500 * time.Millisecond)
	}
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
	deadline := time.Now().Add(10 * time.Second)
	for {
		if _, ok := c.Health(); !ok {
			break
		}
		if time.Now().After(deadline) {
			return "", fmt.Errorf("old server on %s did not stop", h.SSH)
		}
		time.Sleep(300 * time.Millisecond)
	}
	return m.Up(h)
}

// HostState is a point-in-time snapshot of one host, used by list/status and
// consumable by the future dashboard as JSON.
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

// Snapshot inspects one remote host without changing anything.
func (m *Manager) Snapshot(name string, h config.Host, withSessions bool, sessionLimit int) HostState {
	st := HostState{Name: name, SSH: h.SSH, URL: BaseURL(h)}
	if pid, ok := TunnelPID(h); ok {
		st.Tunnel = true
		st.TunnelPID = pid
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
// instances first, then the configured remote hosts.
func (m *Manager) SnapshotAll(withSessions bool, sessionLimit int) []HostState {
	names := m.Config.Names()
	states := make([]HostState, len(names))
	done := make(chan struct{}, len(names)+1)
	var local []HostState
	go func() {
		local = m.SnapshotLocal(withSessions, sessionLimit)
		done <- struct{}{}
	}()
	for i, name := range names {
		go func(i int, name string) {
			states[i] = m.Snapshot(name, m.Config.Hosts[name], withSessions, sessionLimit)
			done <- struct{}{}
		}(i, name)
	}
	for range len(names) + 1 {
		<-done
	}
	return append(local, states...)
}
