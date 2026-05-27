// Package web serves the tubctl HTTP API and the static UI.
package web

import (
	"context"
	"embed"
	"encoding/json"
	"errors"
	"io/fs"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"sublog.org/tubctl/internal/tub"
)

//go:embed all:public
var staticFS embed.FS

// Server brokers HTTP requests against a single shared tub.Client.
type Server struct {
	Tub        *tub.Client
	TimeFormat string // "24" or "12"
	Static     fs.FS
	Log        *slog.Logger

	connectMu sync.Mutex
}

// New wires a Server with the given client and embedded static assets.
func New(tubClient *tub.Client, timeFormat string, log *slog.Logger) (*Server, error) {
	sub, err := fs.Sub(staticFS, "public")
	if err != nil {
		return nil, err
	}
	return &Server{
		Tub:        tubClient,
		TimeFormat: timeFormat,
		Static:     sub,
		Log:        log,
	}, nil
}

// Handler returns the http.Handler for the whole app.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/health", s.handleHealth)
	mux.HandleFunc("GET /api/config", s.handleConfig)
	mux.HandleFunc("GET /api/state",  s.handleState)
	mux.HandleFunc("POST /api/set",   s.handleSet)
	mux.Handle("/", http.FileServer(http.FS(s.Static)))
	return mux
}

// ensureConnected makes sure the tub client is logged in, dialing if needed.
func (s *Server) ensureConnected(ctx context.Context) error {
	if s.Tub.LoggedIn() {
		return nil
	}
	s.connectMu.Lock()
	defer s.connectMu.Unlock()
	if s.Tub.LoggedIn() {
		return nil
	}
	cctx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	return s.Tub.Connect(cctx)
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":        true,
		"connected": s.Tub.LoggedIn(),
	})
}

func (s *Server) handleConfig(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"timeFormat": s.TimeFormat})
}

func (s *Server) handleState(w http.ResponseWriter, r *http.Request) {
	if err := s.ensureConnected(r.Context()); err != nil {
		writeError(w, http.StatusServiceUnavailable, err)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	st, err := s.Tub.Read(ctx)
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, err)
		return
	}
	writeJSON(w, http.StatusOK, st)
}

func (s *Server) handleSet(w http.ResponseWriter, r *http.Request) {
	if err := s.ensureConnected(r.Context()); err != nil {
		writeError(w, http.StatusServiceUnavailable, err)
		return
	}
	var updates map[string]any
	if err := json.NewDecoder(r.Body).Decode(&updates); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if len(updates) == 0 {
		writeError(w, http.StatusBadRequest, errors.New("empty update body"))
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 6*time.Second)
	defer cancel()

	// read current state to preserve un-flagged attrs
	before, err := s.Tub.Read(ctx)
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, err)
		return
	}
	if err := s.Tub.Write(ctx, updates, before); err != nil {
		writeError(w, http.StatusServiceUnavailable, err)
		return
	}
	// brief settle, then echo new state
	time.Sleep(400 * time.Millisecond)
	after, err := s.Tub.Read(ctx)
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, err)
		return
	}
	writeJSON(w, http.StatusOK, after)
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, code int, err error) {
	writeJSON(w, code, map[string]string{"error": err.Error()})
}
