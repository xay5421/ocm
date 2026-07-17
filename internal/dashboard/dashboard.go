// Package dashboard serves a minimal local web UI for managing opencode
// connections: one row per host with its localhost URL, health state and
// connect/disconnect actions. It reuses internal/core.
package dashboard

import (
	"context"
	"embed"
	"encoding/json"
	"fmt"
	"io/fs"
	"net"
	"net/http"
	"net/http/pprof"
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/xay5421/ocm/internal/core"
)

//go:embed static
var staticFS embed.FS

// Server is the dashboard HTTP server.
type Server struct {
	Manager *core.Manager
	// ExitOnIdle, when > 0, makes the process exit after no browser page
	// has been connected (via /api/events) for this long. Used for GUI
	// launches, where there is no terminal to Ctrl-C.
	ExitOnIdle time.Duration

	watchers atomic.Int64 // open /api/events connections

	// stateMu serializes snapshotting so concurrent /api/state requests
	// (several tabs, slow hosts) cannot pile up; results are briefly cached.
	stateMu    sync.Mutex
	stateCache []core.HostState
	stateAt    time.Time
}

// New creates a dashboard server.
func New(m *core.Manager) *Server {
	return &Server{Manager: m}
}

// Handler builds the http handler.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()

	static, err := fs.Sub(staticFS, "static")
	if err != nil {
		panic("embedded static directory missing: " + err.Error())
	}
	mux.Handle("GET /", http.FileServer(http.FS(static)))

	mux.HandleFunc("GET /api/state", s.handleState)
	mux.HandleFunc("POST /api/hosts/{name}/up", s.requireOrigin(s.handleUp))
	mux.HandleFunc("POST /api/hosts/{name}/down", s.requireOrigin(s.handleDown))
	mux.HandleFunc("POST /api/hosts/{name}/restart", s.requireOrigin(s.handleRestart))
	mux.HandleFunc("POST /api/local/up", s.requireOrigin(s.handleLocalUp))
	mux.HandleFunc("POST /api/local/{pid}/down", s.requireOrigin(s.handleLocalDown))
	mux.HandleFunc("POST /api/local/{pid}/restart", s.requireOrigin(s.handleLocalRestart))
	mux.HandleFunc("POST /api/quit", s.requireOrigin(s.handleQuit))
	mux.HandleFunc("GET /api/events", s.handleEvents)
	// Diagnostics: the server binds to 127.0.0.1 only, and a goroutine dump
	// (/debug/pprof/goroutine?debug=2) is invaluable if it ever wedges.
	mux.HandleFunc("GET /debug/pprof/", pprof.Index)
	mux.HandleFunc("GET /debug/pprof/profile", pprof.Profile)
	mux.HandleFunc("GET /debug/pprof/trace", pprof.Trace)
	return requireLoopbackHost(mux)
}

// requireLoopbackHost rejects requests whose Host header is not a loopback
// name. The server only ever binds to 127.0.0.1, but a DNS-rebinding attack
// (an attacker domain resolving to 127.0.0.1) would still arrive over
// loopback - with the attacker's domain in the Host header, which is what
// this check catches.
func requireLoopbackHost(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		host := r.Host
		if h, _, err := net.SplitHostPort(host); err == nil {
			host = h
		}
		switch host {
		case "127.0.0.1", "localhost", "::1", "[::1]":
			next.ServeHTTP(w, r)
		default:
			writeErr(w, http.StatusForbidden, fmt.Errorf("forbidden host %q", host))
		}
	})
}

// requireOrigin wraps a handler to reject cross-origin POST requests. Since
// the dashboard binds to 127.0.0.1, only same-origin requests (or requests
// without an Origin header, such as curl) are allowed. This prevents a
// malicious website from CSRF-ing the dashboard and bringing down tunnels.
func (s *Server) requireOrigin(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			next(w, r)
			return
		}
		origin := r.Header.Get("Origin")
		if origin == "" {
			next(w, r)
			return
		}
		if strings.HasPrefix(origin, "http://127.0.0.1:") ||
			strings.HasPrefix(origin, "http://localhost:") {
			next(w, r)
			return
		}
		writeErr(w, http.StatusForbidden, fmt.Errorf("cross-origin requests are not allowed"))
	}
}

// handleEvents is a server-sent-events stream the page keeps open as a
// liveness signal (and reconnects automatically). Browsers throttle timers in
// background tabs but keep established connections, so this is more reliable
// than a polling heartbeat for detecting "no page open anymore".
//
// Each stream is capped at maxEventStreamAge: writes into a half-closed
// connection (browser tab gone, FIN received but never RST) do not fail, so
// without the cap such dead streams would count as watchers forever and keep
// an --exit-on-idle dashboard alive. Live pages reconnect instantly.
func (s *Server) handleEvents(w http.ResponseWriter, r *http.Request) {
	const maxEventStreamAge = 5 * time.Minute
	fl, ok := w.(http.Flusher)
	if !ok {
		writeErr(w, 500, fmt.Errorf("streaming unsupported"))
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	s.watchers.Add(1)
	defer s.watchers.Add(-1)
	fmt.Fprint(w, ": connected\n\n")
	fl.Flush()
	expire := time.NewTimer(maxEventStreamAge)
	defer expire.Stop()
	tick := time.NewTicker(15 * time.Second)
	defer tick.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case <-expire.C:
			return
		case <-tick.C:
			if _, err := fmt.Fprint(w, ": ping\n\n"); err != nil {
				return
			}
			fl.Flush()
		}
	}
}

// idleWatch exits the process once no page has been connected for
// s.ExitOnIdle. The countdown also runs from startup, so a dashboard whose
// browser never opened cleans itself up too.
func (s *Server) idleWatch(ctx context.Context) {
	idleSince := time.Now()
	tick := time.NewTicker(5 * time.Second)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
			if s.watchers.Load() > 0 {
				idleSince = time.Now()
				continue
			}
			if time.Since(idleSince) >= s.ExitOnIdle {
				fmt.Fprintf(os.Stderr, "ocm: dashboard idle for %s, exiting\n", s.ExitOnIdle)
				os.Exit(0)
			}
		}
	}
}

// handleQuit exits the ocm process. It exists so the dashboard can be closed
// when there is no terminal to Ctrl-C (e.g. started by double-click).
// Tunnels and servers keep running; only the dashboard process stops.
func (s *Server) handleQuit(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, map[string]bool{"ok": true})
	go func() {
		time.Sleep(200 * time.Millisecond) // let the response flush
		os.Exit(0)
	}()
}

// Serve blocks serving HTTP on addr until ctx is cancelled.
func (s *Server) Serve(ctx context.Context, addr string) error {
	if s.ExitOnIdle > 0 {
		go s.idleWatch(ctx)
	}
	srv := &http.Server{
		Addr:    addr,
		Handler: s.Handler(),
		// No WriteTimeout: /api/events is a long-lived SSE stream, and
		// actions like "up" legitimately take up to a minute (ssh tunnel +
		// remote server start). ReadHeaderTimeout still guards the accept
		// path; the server binds to 127.0.0.1 only.
		ReadTimeout: 10 * time.Second,
		IdleTimeout: 60 * time.Second,
	}
	go func() {
		<-ctx.Done()
		shutCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		srv.Shutdown(shutCtx)
	}()
	return srv.ListenAndServe()
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, code int, err error) {
	w.WriteHeader(code)
	writeJSON(w, map[string]string{"error": err.Error()})
}

func (s *Server) handleState(w http.ResponseWriter, r *http.Request) {
	s.stateMu.Lock()
	defer s.stateMu.Unlock()
	if time.Since(s.stateAt) > 2*time.Second {
		s.stateCache = s.Manager.SnapshotAll(false, 0)
		s.stateAt = time.Now()
	}
	writeJSON(w, s.stateCache)
}

func (s *Server) handleUp(w http.ResponseWriter, r *http.Request) {
	_, h, err := s.Manager.Config.Get(r.PathValue("name"))
	if err != nil {
		writeErr(w, 404, err)
		return
	}
	version, err := s.Manager.Up(h)
	if err != nil {
		writeErr(w, 500, err)
		return
	}
	writeJSON(w, map[string]string{"version": version})
}

func (s *Server) handleDown(w http.ResponseWriter, r *http.Request) {
	_, h, err := s.Manager.Config.Get(r.PathValue("name"))
	if err != nil {
		writeErr(w, 404, err)
		return
	}
	if err := s.Manager.Down(h, false); err != nil {
		writeErr(w, 500, err)
		return
	}
	writeJSON(w, map[string]bool{"ok": true})
}

func (s *Server) handleRestart(w http.ResponseWriter, r *http.Request) {
	_, h, err := s.Manager.Config.Get(r.PathValue("name"))
	if err != nil {
		writeErr(w, 404, err)
		return
	}
	version, err := s.Manager.RestartServe(h)
	if err != nil {
		writeErr(w, 500, err)
		return
	}
	writeJSON(w, map[string]string{"version": version})
}

func (s *Server) handleLocalRestart(w http.ResponseWriter, r *http.Request) {
	pid, err := strconv.Atoi(r.PathValue("pid"))
	if err != nil {
		writeErr(w, 400, fmt.Errorf("invalid pid"))
		return
	}
	st, err := s.Manager.RestartLocal(pid)
	if err != nil {
		writeErr(w, 500, err)
		return
	}
	writeJSON(w, st)
}

func (s *Server) handleLocalUp(w http.ResponseWriter, r *http.Request) {
	st, err := s.Manager.StartLocalServe()
	if err != nil {
		writeErr(w, 500, err)
		return
	}
	writeJSON(w, st)
}

func (s *Server) handleLocalDown(w http.ResponseWriter, r *http.Request) {
	pid, err := strconv.Atoi(r.PathValue("pid"))
	if err != nil {
		writeErr(w, 400, fmt.Errorf("invalid pid"))
		return
	}
	if err := s.Manager.StopLocalServe(pid); err != nil {
		writeErr(w, 500, err)
		return
	}
	writeJSON(w, map[string]bool{"ok": true})
}
