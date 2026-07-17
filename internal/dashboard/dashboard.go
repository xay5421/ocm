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
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/xay5421/ocm/internal/core"
)

//go:embed static
var staticFS embed.FS

// Server is the dashboard HTTP server. It serves the UI shown in the native
// dashboard window; the process lifetime is tied to that window (see
// cmdDashboard in internal/cli).
type Server struct {
	Manager *core.Manager

	// stateMu serializes snapshotting so concurrent /api/state requests
	// (slow hosts) cannot pile up; results are briefly cached.
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

// Serve blocks serving HTTP on addr until ctx is cancelled.
func (s *Server) Serve(ctx context.Context, addr string) error {
	srv := &http.Server{
		Addr:    addr,
		Handler: s.Handler(),
		// No WriteTimeout: actions like "up" legitimately take up to a
		// minute (ssh tunnel + remote server start). ReadHeaderTimeout
		// still guards the accept path; the server binds to 127.0.0.1
		// only.
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
