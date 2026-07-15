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
	"net/http"
	"strconv"
	"time"

	"ocm/internal/core"
)

//go:embed static
var staticFS embed.FS

// Server is the dashboard HTTP server.
type Server struct {
	Manager *core.Manager
}

// New creates a dashboard server.
func New(m *core.Manager) *Server {
	return &Server{Manager: m}
}

// Handler builds the http handler.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()

	static, _ := fs.Sub(staticFS, "static")
	mux.Handle("GET /", http.FileServer(http.FS(static)))

	mux.HandleFunc("GET /api/state", s.handleState)
	mux.HandleFunc("POST /api/hosts/{name}/up", s.handleUp)
	mux.HandleFunc("POST /api/hosts/{name}/down", s.handleDown)
	mux.HandleFunc("POST /api/hosts/{name}/restart", s.handleRestart)
	mux.HandleFunc("POST /api/local/up", s.handleLocalUp)
	mux.HandleFunc("POST /api/local/{pid}/down", s.handleLocalDown)
	mux.HandleFunc("POST /api/local/{pid}/restart", s.handleLocalRestart)
	return mux
}

// Serve blocks serving HTTP on addr until ctx is cancelled.
func (s *Server) Serve(ctx context.Context, addr string) error {
	srv := &http.Server{Addr: addr, Handler: s.Handler()}
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
	writeJSON(w, s.Manager.SnapshotAll(false, 0))
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
