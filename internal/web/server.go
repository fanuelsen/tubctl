// Package web serves the tubctl HTTP API and the static UI.
package web

import (
	"context"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
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
	mux.HandleFunc("GET /api/events", s.handleEvents)
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

// handleEvents streams Status updates to the client as Server-Sent Events.
// Sends an initial state on connect, then every Status the tub client sees —
// both device-pushed (0x04) state-change reports and read responses. A
// background re-read every 15s acts as a safety net in case the device doesn't
// push for some change (e.g. slow temperature drift), and a comment ping every
// 20s keeps idle connections alive through any intervening proxies.
func (s *Server) handleEvents(w http.ResponseWriter, r *http.Request) {
	if err := s.ensureConnected(r.Context()); err != nil {
		writeError(w, http.StatusServiceUnavailable, err)
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming not supported", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")

	ch, cancel := s.Tub.Subscribe(4)
	defer cancel()

	sendState := func(st *tub.Status) bool {
		b, err := json.Marshal(st)
		if err != nil {
			return false
		}
		if _, err := fmt.Fprintf(w, "event: state\ndata: %s\n\n", b); err != nil {
			return false
		}
		flusher.Flush()
		return true
	}

	// Initial snapshot so the client renders immediately without waiting for
	// the first device-pushed update or background refresh.
	{
		ctx, c := context.WithTimeout(r.Context(), 5*time.Second)
		st, err := s.Tub.Read(ctx)
		c()
		if err == nil && !sendState(st) {
			return
		}
	}

	refresh := time.NewTicker(15 * time.Second)
	defer refresh.Stop()
	ping := time.NewTicker(20 * time.Second)
	defer ping.Stop()

	for {
		select {
		case <-r.Context().Done():
			return
		case st, ok := <-ch:
			if !ok {
				return
			}
			if !sendState(st) {
				return
			}
		case <-refresh.C:
			ctx, c := context.WithTimeout(context.Background(), 4*time.Second)
			_, _ = s.Tub.Read(ctx)
			c()
		case <-ping.C:
			if _, err := fmt.Fprint(w, ": ping\n\n"); err != nil {
				return
			}
			flusher.Flush()
		}
	}
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

	// Log what actually changed at the tub (filtered to attrs the request touched).
	// This gives operators visibility into every control action.
	changes := stateDiff(before, after, updates)
	if len(changes) > 0 {
		s.Log.Info("set", "changes", changes, "from", subset(before, updates), "ip", clientIP(r))
	} else {
		s.Log.Info("set (no-op)", "requested", updates, "ip", clientIP(r))
	}

	writeJSON(w, http.StatusOK, after)
}

// stateDiff returns the post-write values for any attribute in `requested` that
// actually changed. Empty result = device didn't honor the write.
func stateDiff(before, after *tub.Status, requested map[string]any) map[string]any {
	a := statusToMap(after)
	b := statusToMap(before)
	out := map[string]any{}
	for k := range requested {
		if av, ok := a[k]; ok && !reflectEqual(b[k], av) {
			out[k] = av
		}
	}
	return out
}

// subset returns the before-values for the attrs in `requested`, so the log line
// shows "from=X to=Y" pairs.
func subset(s *tub.Status, requested map[string]any) map[string]any {
	m := statusToMap(s)
	out := map[string]any{}
	for k := range requested {
		if v, ok := m[k]; ok {
			out[k] = v
		}
	}
	return out
}

func statusToMap(s *tub.Status) map[string]any {
	if s == nil {
		return nil
	}
	return map[string]any{
		"power":            s.Power,
		"heat_power":       s.HeatPower,
		"filter_power":     s.FilterPower,
		"wave_power":       s.WavePower,
		"locked":           s.Locked,
		"earth":            s.Earth,
		"temp_set_unit":    s.TempSetUnit,
		"temp_set":         s.TempSet,
		"heat_appm_min":    s.HeatAppmMin,
		"heat_timer_min":   s.HeatTimerMin,
		"filter_appm_min":  s.FilterAppmMin,
		"filter_timer_min": s.FilterTimerMin,
		"wave_appm_min":    s.WaveAppmMin,
		"wave_timer_min":   s.WaveTimerMin,
	}
}

func reflectEqual(a, b any) bool {
	// Cheap value equality for the types we have here (bool / uint8 / uint16 / int).
	return fmt.Sprintf("%v", a) == fmt.Sprintf("%v", b)
}

// clientIP best-effort extracts the requester IP for the log line.
func clientIP(r *http.Request) string {
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		return xff
	}
	host := r.RemoteAddr
	if i := indexLastColon(host); i >= 0 {
		return host[:i]
	}
	return host
}

func indexLastColon(s string) int {
	for i := len(s) - 1; i >= 0; i-- {
		if s[i] == ':' {
			return i
		}
	}
	return -1
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, code int, err error) {
	writeJSON(w, code, map[string]string{"error": err.Error()})
}
