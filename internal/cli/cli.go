// Package cli implements the ocm command line interface.
package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/xay5421/ocm/internal/config"
	"github.com/xay5421/ocm/internal/core"
	"github.com/xay5421/ocm/internal/dashboard"
)

const usage = `ocm - opencode connection manager

Usage:
  ocm list [--json]                 List hosts and their status
  ocm status [--json]               List hosts with sessions and live status
  ocm up <host>                     Ensure tunnel + remote opencode serve
  ocm down <host> [--serve]         Stop tunnel (--serve also stops remote server)
  ocm connect <host> [dir] [args…]  Up + attach local TUI to the remote server
  ocm run <host> [args…] <prompt>   Up + run a prompt on the remote server
  ocm restart <host>                Restart the remote server (e.g. after config change)
  ocm up local                      Start a local opencode serve (fixed port 14000)
  ocm down local [pid]              Stop a discovered local server
  ocm restart local [pid]           Restart a local server (fixed port 14000)
  ocm dashboard [--port N] [--up]   Open the dashboard app window (Windows/macOS;
                                    serves http://127.0.0.1:4800, closing the
                                    window exits; elsewhere open the URL manually)
  ocm config                        Print config file path and contents
  ocm version                       Print the ocm version

On Windows, double-clicking ocm.exe (or ocm-dashboard.exe) opens the
dashboard window; on macOS use the ocm.app bundle.

Hosts are defined in ~/.config/ocm/config.json (override with $OCM_CONFIG).
Extra args after <host> are passed through to 'opencode attach' / 'opencode run'.

Server passwords (HTTP basic auth, opencode's OPENCODE_SERVER_PASSWORD):
  top-level "password" field        default password for all servers
  per-host  "password" field        protects that remote server (overrides default)
  top-level "local_password" field  protects local servers started by ocm (overrides default)
ocm exports the password when starting servers and authenticates with it.
`

// Version is stamped at build time via
// -ldflags "-X github.com/xay5421/ocm/internal/cli.Version=v1.2.3".
var Version = "dev"

// Run executes the CLI.
func Run(args []string) error {
	if len(args) == 0 {
		// Double-clicked in Explorer (Windows): open the dashboard
		// window instead of printing help nobody would see. The
		// console window is dropped; closing the dashboard window
		// stops the process.
		if launchedFromGUI() {
			freeConsole()
			args = []string{"dashboard"}
		} else {
			fmt.Print(usage)
			return nil
		}
	}
	cmd, rest := args[0], args[1:]

	switch cmd {
	case "help", "-h", "--help":
		fmt.Print(usage)
		return nil
	case "version", "-v", "--version":
		fmt.Println("ocm", Version)
		return nil
	}

	cfg, err := config.Load()
	if err != nil {
		return err
	}
	m := &core.Manager{Config: cfg, Log: func(f string, a ...any) {
		fmt.Fprintf(os.Stderr, "ocm: "+f+"\n", a...)
	}}

	switch cmd {
	case "list":
		return cmdList(m, rest, false)
	case "status":
		return cmdList(m, rest, true)
	case "up":
		return cmdUp(m, rest)
	case "down":
		return cmdDown(m, rest)
	case "restart":
		return cmdRestart(m, rest)
	case "connect":
		return cmdConnect(m, rest)
	case "run":
		return cmdRun(m, rest)
	case "dashboard":
		return cmdDashboard(m, rest)
	case "config":
		fmt.Println(config.Path())
		data, err := os.ReadFile(config.Path())
		if err != nil {
			return err
		}
		fmt.Print(config.SafeMarshal(data))
		return nil
	default:
		return fmt.Errorf("unknown command %q\n\n%s", cmd, usage)
	}
}

func hasFlag(args []string, flag string) (bool, []string) {
	out := args[:0:0]
	found := false
	for _, a := range args {
		if a == flag {
			found = true
			continue
		}
		out = append(out, a)
	}
	return found, out
}

func cmdList(m *core.Manager, args []string, withSessions bool) error {
	asJSON, _ := hasFlag(args, "--json")
	states := m.SnapshotAll(withSessions, 5)
	if asJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(states)
	}
	w := tabwriter.NewWriter(os.Stdout, 2, 4, 2, ' ', 0)
	fmt.Fprintln(w, "HOST\tTUNNEL\tSERVER\tVERSION\tURL")
	for _, st := range states {
		tunnel, server := "-", "-"
		if st.Local {
			tunnel = fmt.Sprintf("(%s pid %d)", st.Command, st.PID)
		} else if st.Tunnel {
			tunnel = fmt.Sprintf("up (pid %d)", st.TunnelPID)
		}
		if st.Healthy {
			server = "healthy"
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n", st.Name, tunnel, server, st.Version, st.URL)
	}
	w.Flush()
	if !withSessions {
		return nil
	}
	for _, st := range states {
		if !st.Healthy {
			continue
		}
		fmt.Printf("\n%s sessions:\n", st.Name)
		if len(st.Sessions) == 0 {
			fmt.Println("  (none)")
			continue
		}
		sw := tabwriter.NewWriter(os.Stdout, 2, 4, 2, ' ', 0)
		for _, s := range st.Sessions {
			updated := time.UnixMilli(s.Updated).Format("01-02 15:04")
			status := s.Status
			if status == "" {
				status = "idle"
			}
			fmt.Fprintf(sw, "  %s\t%s\t%s\t%s\n", s.ID, status, updated, core.Truncate(s.Title, 60))
		}
		sw.Flush()
	}
	return nil
}

func cmdUp(m *core.Manager, args []string) error {
	if len(args) < 1 {
		return fmt.Errorf("usage: ocm up <host>")
	}
	if args[0] == "local" {
		st, err := m.StartLocalServe()
		if err != nil {
			return err
		}
		fmt.Printf("local ready: %s (opencode %s, pid %d)\n", st.URL, st.Version, st.PID)
		return nil
	}
	name, h, err := m.Config.Get(args[0])
	if err != nil {
		return err
	}
	v, err := m.Up(h)
	if err != nil {
		return err
	}
	fmt.Printf("%s ready: %s (opencode %s)\n", name, core.BaseURL(h), v)
	return nil
}

func cmdDown(m *core.Manager, args []string) error {
	stopServe, args := hasFlag(args, "--serve")
	if len(args) < 1 {
		return fmt.Errorf("usage: ocm down <host> [--serve] | ocm down local [pid]")
	}
	if args[0] == "local" {
		return cmdDownLocal(m, args[1:])
	}
	name, h, err := m.Config.Get(args[0])
	if err != nil {
		return err
	}
	if err := m.Down(h, stopServe); err != nil {
		return err
	}
	fmt.Printf("%s down\n", name)
	return nil
}

func cmdRestart(m *core.Manager, args []string) error {
	if len(args) < 1 {
		return fmt.Errorf("usage: ocm restart <host> | ocm restart local [pid]")
	}
	if args[0] == "local" {
		pid, err := pickLocalManaged(m, args[1:])
		if err != nil {
			return err
		}
		st, err := m.RestartLocal(pid)
		if err != nil {
			return err
		}
		fmt.Printf("local restarted: %s (opencode %s, pid %d)\n", st.URL, st.Version, st.PID)
		return nil
	}
	name, h, err := m.Config.Get(args[0])
	if err != nil {
		return err
	}
	v, err := m.RestartServe(h)
	if err != nil {
		return err
	}
	fmt.Printf("%s restarted: %s (opencode %s)\n", name, core.BaseURL(h), v)
	return nil
}

// pickLocalManaged resolves which managed local instance a command targets:
// an explicit pid argument, or the only managed instance running.
func pickLocalManaged(m *core.Manager, args []string) (int, error) {
	if len(args) >= 1 {
		var pid int
		if _, err := fmt.Sscanf(args[0], "%d", &pid); err != nil {
			return 0, fmt.Errorf("invalid pid %q", args[0])
		}
		return pid, nil
	}
	var managed []core.HostState
	for _, st := range m.SnapshotLocal(false, 0) {
		if st.Managed {
			managed = append(managed, st)
		}
	}
	switch len(managed) {
	case 0:
		return 0, fmt.Errorf("no managed local opencode server running")
	case 1:
		return managed[0].PID, nil
	default:
		fmt.Println("multiple local servers running, specify a pid:")
		for _, st := range managed {
			fmt.Printf("  pid %d  %s  (%s)\n", st.PID, st.URL, st.Command)
		}
		return 0, fmt.Errorf("ambiguous")
	}
}

func cmdDownLocal(m *core.Manager, args []string) error {
	pid, err := pickLocalManaged(m, args)
	if err != nil {
		return err
	}
	if err := m.StopLocalServe(pid); err != nil {
		return err
	}
	fmt.Printf("local (pid %d) down\n", pid)
	return nil
}

// ensureLocal returns a running local instance, starting one if needed.
// Dedicated opencode processes are preferred over embedded servers (e.g. the
// VS Code extension).
func ensureLocal(m *core.Manager) (core.HostState, error) {
	states := m.SnapshotLocal(false, 0)
	for _, st := range states {
		if st.Managed {
			return st, nil
		}
	}
	if len(states) > 0 {
		return states[0], nil
	}
	return m.StartLocalServe()
}

// resolveTarget makes the target of a connect/run command reachable and
// returns what the opencode handoff needs: the server URL, the host's
// default directory (empty for local) and the password.
func resolveTarget(m *core.Manager, hostArg string) (url, dir, password string, err error) {
	if hostArg == "local" {
		st, err := ensureLocal(m)
		if err != nil {
			return "", "", "", err
		}
		return st.URL, "", m.Config.LocalPassword, nil
	}
	_, h, err := m.Config.Get(hostArg)
	if err != nil {
		return "", "", "", err
	}
	if _, err := m.Up(h); err != nil {
		return "", "", "", err
	}
	return core.BaseURL(h), h.Dir, h.Password, nil
}

func cmdConnect(m *core.Manager, args []string) error {
	if len(args) < 1 {
		return fmt.Errorf("usage: ocm connect <host> [dir] [extra opencode attach args…]")
	}
	url, dir, password, err := resolveTarget(m, args[0])
	if err != nil {
		return err
	}
	rest := args[1:]
	if len(rest) > 0 && !strings.HasPrefix(rest[0], "-") {
		dir = rest[0]
		rest = rest[1:]
	}
	attach := []string{"attach", url}
	if dir != "" {
		attach = append(attach, "--dir", dir)
	}
	attach = append(attach, rest...)
	return execOpencode(attach, password)
}

func cmdRun(m *core.Manager, args []string) error {
	if len(args) < 2 {
		return fmt.Errorf("usage: ocm run <host> [opencode run args…] <prompt>")
	}
	url, _, password, err := resolveTarget(m, args[0])
	if err != nil {
		return err
	}
	runArgs := append([]string{"run", "--attach", url}, args[1:]...)
	return execOpencode(runArgs, password)
}

func cmdDashboard(m *core.Manager, args []string) error {
	port := 4800
	up, args := hasFlag(args, "--up")
	for i := 0; i < len(args); i++ {
		if args[i] != "--port" {
			continue
		}
		if i+1 >= len(args) {
			return fmt.Errorf("--port requires a value")
		}
		p, err := strconv.Atoi(args[i+1])
		if err != nil || p < 1 || p > 65535 {
			return fmt.Errorf("invalid port %q", args[i+1])
		}
		port = p
		i++
	}
	if up {
		for _, name := range m.Config.Names() {
			h := m.Config.Hosts[name]
			if _, err := m.Up(h); err != nil {
				fmt.Fprintf(os.Stderr, "ocm: warning: %s: %v\n", name, err)
			}
		}
	}
	addr := fmt.Sprintf("127.0.0.1:%d", port)
	url := "http://" + addr
	fmt.Printf("ocm dashboard: %s\n", url)

	// Serve in the background, show the native window on this (main)
	// goroutine, and shut down when it is closed.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	errc := make(chan error, 1)
	srv := dashboard.New(m)
	go func() { errc <- srv.Serve(ctx, addr) }()
	waitReachable(addr, 2*time.Second)
	if openWindow(url, "ocm") {
		cancel()
		<-errc
		return nil
	}
	switch runtime.GOOS {
	case "windows", "darwin":
		// The window should have opened; do not linger headless.
		cancel()
		<-errc
		return fmt.Errorf("could not open the dashboard window (is the WebView2 runtime installed?)")
	default:
		// No native window on this platform: keep serving so the URL
		// above can be opened manually; Ctrl-C to stop.
		return <-errc
	}
}

// waitReachable polls addr until it accepts TCP connections, so the window
// does not navigate before the dashboard listener is up.
func waitReachable(addr string, timeout time.Duration) {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if c, err := net.DialTimeout("tcp", addr, 200*time.Millisecond); err == nil {
			c.Close()
			return
		}
		time.Sleep(25 * time.Millisecond)
	}
}

// openBrowser opens url in the default browser, best effort. It is used by
// the dashboard window to open external links (see window_*.go).
func openBrowser(url string) {
	switch runtime.GOOS {
	case "windows":
		exec.Command("rundll32", "url.dll,FileProtocolHandler", url).Run()
	case "darwin":
		exec.Command("open", url).Run()
	default:
		exec.Command("xdg-open", url).Run()
	}
}

// execOpencode hands control to the local opencode binary: on Unix the
// current process is replaced (execve), on Windows the child runs with
// inherited stdio and its exit code is propagated. password (if any) is
// passed via OPENCODE_SERVER_PASSWORD, which `opencode attach` and
// `opencode run --attach` use as the default basic auth password; this keeps
// it off the command line.
func execOpencode(args []string, password string) error {
	bin, err := core.LocalOpencodeBin()
	if err != nil {
		return err
	}
	argv := append([]string{bin}, args...)
	fmt.Fprintf(os.Stderr, "ocm: exec %s\n", strings.Join(argv, " "))
	return execReplace(bin, argv, core.EnvWithPassword(password))
}
