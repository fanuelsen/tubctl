// Package web serves the tubctl HTTP API and the static UI.
package web

import (
	"context"
	"crypto/subtle"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"sublog.org/tubctl/internal/sched"
	"sublog.org/tubctl/internal/tub"
)

// maxBodyBytes caps request bodies on the write endpoints so a hostile client
// can't exhaust memory (or, for schedules, disk) with a giant payload.
const maxBodyBytes = 64 << 10

// maxSSEClients caps concurrent /api/events streams. Each holds a goroutine and
// drives periodic tub reads; unbounded, they starve real requests (read timeout).
const maxSSEClients = 16

//go:embed all:public
var staticFS embed.FS

// Server brokers HTTP requests against a single shared tub.Client.
type Server struct {
	Tub          *tub.Client
	Sched        *sched.Scheduler
	TimeFormat   string   // "24" or "12"
	AuthToken    string   // if set, required on write endpoints
	AllowedHosts []string // if non-empty, Host header must match (anti DNS-rebind)
	Static       fs.FS
	Log          *slog.Logger

	connectMu sync.Mutex
	sseCount  atomic.Int32
}

// New wires a Server with the given client, scheduler, and embedded static assets.
func New(tubClient *tub.Client, scheduler *sched.Scheduler, timeFormat string, log *slog.Logger) (*Server, error) {
	sub, err := fs.Sub(staticFS, "public")
	if err != nil {
		return nil, err
	}
	return &Server{
		Tub:        tubClient,
		Sched:      scheduler,
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
	mux.HandleFunc("GET /api/state", s.handleState)
	mux.HandleFunc("GET /api/events", s.handleEvents)
	mux.HandleFunc("POST /api/set", s.handleSet)
	mux.HandleFunc("GET /api/schedules", s.handleGetSchedules)
	mux.HandleFunc("PUT /api/schedules", s.handlePutSchedules)
	mux.Handle("/", http.FileServer(http.FS(s.Static)))
	return securityHeaders(mux)
}

// securityHeaders adds hardening headers to every response. Defense-in-depth
// for the localStorage auth token: forbid framing, MIME sniffing, and referrer
// leakage, and restrict the page to same-origin resources. script-src/style-src
// need 'unsafe-inline' because index.html inlines both (single-file UI).
func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Content-Security-Policy",
			"default-src 'self'; script-src 'self' 'unsafe-inline'; style-src 'self' 'unsafe-inline'; "+
				"img-src 'self' data:; connect-src 'self'; frame-ancestors 'none'; base-uri 'none'; form-action 'self'")
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Referrer-Policy", "no-referrer")
		next.ServeHTTP(w, r)
	})
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

// guardWrite enforces anti-CSRF / anti-DNS-rebinding checks and the optional
// shared-secret token on state-changing requests. It returns false (and writes
// the error response) when the request should be refused.
//
// The threat is a browser the victim controls being coerced into issuing the
// request cross-origin. We layer cheap, independent defenses:
//   - Sec-Fetch-Site: browsers tag cross-site/same-site requests; reject those.
//   - Content-Type must be application/json, so a CORS "simple request" can't
//     smuggle a body without first triggering (and failing) a preflight.
//   - Optional Host allowlist defeats DNS rebinding (the rebound request still
//     carries the attacker's Host).
//   - Optional bearer token for actual authentication when exposed beyond a
//     trusted LAN. Non-browser clients (no Sec-Fetch-Site header) still must
//     pass the Content-Type and token checks.
func (s *Server) guardWrite(w http.ResponseWriter, r *http.Request) bool {
	switch r.Header.Get("Sec-Fetch-Site") {
	case "cross-site", "same-site":
		writeError(w, http.StatusForbidden, errors.New("cross-origin request rejected"))
		return false
	}
	if ct := r.Header.Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		writeError(w, http.StatusUnsupportedMediaType, errors.New("Content-Type must be application/json"))
		return false
	}
	if len(s.AllowedHosts) > 0 && !hostAllowed(r.Host, s.AllowedHosts) {
		writeError(w, http.StatusForbidden, errors.New("host not allowed"))
		return false
	}
	if s.AuthToken != "" && !tokenOK(r, s.AuthToken) {
		writeError(w, http.StatusUnauthorized, errors.New("missing or invalid auth token"))
		return false
	}
	return true
}

// readJSONBody size-limits and decodes a JSON request body. On failure it
// writes the appropriate error (413 when over the cap, 400 otherwise) and
// returns false.
func readJSONBody(w http.ResponseWriter, r *http.Request, v any) bool {
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBodyBytes))
	if err := dec.Decode(v); err != nil {
		var mbe *http.MaxBytesError
		if errors.As(err, &mbe) {
			writeError(w, http.StatusRequestEntityTooLarge, errors.New("request body too large"))
		} else {
			writeError(w, http.StatusBadRequest, err)
		}
		return false
	}
	return true
}

// tokenOK constant-time compares a bearer token from X-Auth-Token or
// Authorization: Bearer against the configured secret.
func tokenOK(r *http.Request, want string) bool {
	got := r.Header.Get("X-Auth-Token")
	if got == "" {
		if a := r.Header.Get("Authorization"); strings.HasPrefix(a, "Bearer ") {
			got = strings.TrimPrefix(a, "Bearer ")
		}
	}
	return got != "" && subtle.ConstantTimeCompare([]byte(got), []byte(want)) == 1
}

// hostAllowed reports whether the request Host (port stripped) is in the list.
func hostAllowed(host string, allowed []string) bool {
	h := host
	if hh, _, err := net.SplitHostPort(host); err == nil {
		h = hh
	}
	for _, a := range allowed {
		if strings.EqualFold(h, a) {
			return true
		}
	}
	return false
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
	// Cap concurrent streams so a flood can't exhaust goroutines or starve the
	// serialized tub connection. Reserve a slot before doing any work.
	if n := s.sseCount.Add(1); n > maxSSEClients {
		s.sseCount.Add(-1)
		writeError(w, http.StatusServiceUnavailable, errors.New("too many event streams"))
		return
	}
	defer s.sseCount.Add(-1)

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
	if !s.guardWrite(w, r) {
		return
	}
	// Decode and validate before dialing the tub, so bad input fails fast.
	var updates map[string]any
	if !readJSONBody(w, r, &updates) {
		return
	}
	if len(updates) == 0 {
		writeError(w, http.StatusBadRequest, errors.New("empty update body"))
		return
	}
	// Validate types/ranges so we reject bad input instead of silently wrapping
	// it modulo 256/65536 on the wire (same contract the CLI enforces).
	if err := tub.ValidateUpdates(updates); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if err := s.ensureConnected(r.Context()); err != nil {
		writeError(w, http.StatusServiceUnavailable, err)
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
		s.Log.Info("set", "changes", changes, "from", subset(before, updates), "ip", clientIP(r), "xff", forwardedFor(r))
	} else {
		s.Log.Info("set (no-op)", "requested", updates, "ip", clientIP(r), "xff", forwardedFor(r))
	}

	writeJSON(w, http.StatusOK, after)
}

func (s *Server) handleGetSchedules(w http.ResponseWriter, r *http.Request) {
	if s.Sched == nil {
		writeJSON(w, http.StatusOK, []sched.Schedule{})
		return
	}
	writeJSON(w, http.StatusOK, s.Sched.List())
}

func (s *Server) handlePutSchedules(w http.ResponseWriter, r *http.Request) {
	if !s.guardWrite(w, r) {
		return
	}
	if s.Sched == nil {
		writeError(w, http.StatusServiceUnavailable, errors.New("scheduler not configured"))
		return
	}
	var list []sched.Schedule
	if !readJSONBody(w, r, &list) {
		return
	}
	saved, err := s.Sched.Replace(list)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	s.Log.Info("schedules updated", "count", len(saved), "ip", clientIP(r), "xff", forwardedFor(r))
	writeJSON(w, http.StatusOK, saved)
}

// stateDiff returns the post-write values for any attribute in `requested` that
// actually changed. Empty result = device didn't honor the write.
func stateDiff(before, after *tub.Status, requested map[string]any) map[string]any {
	a := after.Map()
	b := before.Map()
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
	m := s.Map()
	out := map[string]any{}
	for k := range requested {
		if v, ok := m[k]; ok {
			out[k] = v
		}
	}
	return out
}

func reflectEqual(a, b any) bool {
	// Cheap value equality for the types we have here (bool / uint8 / uint16 / int).
	return fmt.Sprintf("%v", a) == fmt.Sprintf("%v", b)
}

// clientIP extracts the requester IP from the socket address for the log line.
// X-Forwarded-For is deliberately ignored: it's client-controlled, and the set
// log doubles as an audit trail. When present it's logged separately (see
// forwardedFor) so reverse-proxy deployments still get the upstream hint.
func clientIP(r *http.Request) string {
	if host, _, err := net.SplitHostPort(r.RemoteAddr); err == nil {
		return host
	}
	return r.RemoteAddr
}

// forwardedFor returns a sanitized X-Forwarded-For value for logging, or "".
// Untrusted input: cap the length and strip control characters so a client
// can't stuff garbage or fake extra fields into the log line.
func forwardedFor(r *http.Request) string {
	xff := r.Header.Get("X-Forwarded-For")
	if len(xff) > 100 {
		xff = xff[:100]
	}
	return strings.Map(func(c rune) rune {
		if c < 0x20 || c == 0x7f {
			return -1
		}
		return c
	}, xff)
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, code int, err error) {
	writeJSON(w, code, map[string]string{"error": err.Error()})
}
