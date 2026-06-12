package web

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestGuardWrite(t *testing.T) {
	s := &Server{AuthToken: "secret", AllowedHosts: []string{"tub.local"}}

	// base request that passes every check
	base := func() *http.Request {
		r := httptest.NewRequest(http.MethodPost, "http://tub.local/api/set", nil)
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("X-Auth-Token", "secret")
		r.Header.Set("Sec-Fetch-Site", "same-origin")
		return r
	}

	cases := []struct {
		name string
		mod  func(*http.Request)
		want bool
	}{
		{"all good (browser same-origin)", func(*http.Request) {}, true},
		{"non-browser client (no Sec-Fetch-Site)", func(r *http.Request) { r.Header.Del("Sec-Fetch-Site") }, true},
		{"cross-site rejected", func(r *http.Request) { r.Header.Set("Sec-Fetch-Site", "cross-site") }, false},
		{"same-site rejected", func(r *http.Request) { r.Header.Set("Sec-Fetch-Site", "same-site") }, false},
		{"non-json content type rejected", func(r *http.Request) { r.Header.Set("Content-Type", "text/plain") }, false},
		{"disallowed host rejected", func(r *http.Request) { r.Host = "evil.example" }, false},
		{"host with port allowed", func(r *http.Request) { r.Host = "tub.local:3000" }, true},
		{"missing token rejected", func(r *http.Request) { r.Header.Del("X-Auth-Token") }, false},
		{"wrong token rejected", func(r *http.Request) { r.Header.Set("X-Auth-Token", "nope") }, false},
		{"bearer token accepted", func(r *http.Request) {
			r.Header.Del("X-Auth-Token")
			r.Header.Set("Authorization", "Bearer secret")
		}, true},
	}

	for _, c := range cases {
		r := base()
		c.mod(r)
		w := httptest.NewRecorder()
		if got := s.guardWrite(w, r); got != c.want {
			t.Errorf("%s: guardWrite = %v (status %d), want %v", c.name, got, w.Code, c.want)
		}
	}
}

// Every response must carry the hardening headers (defense-in-depth for the
// localStorage auth token: no framing, no MIME sniffing, no referrer leaks).
func TestSecurityHeaders(t *testing.T) {
	s := &Server{TimeFormat: "24"}
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/config", nil))

	want := map[string]string{
		"X-Content-Type-Options": "nosniff",
		"X-Frame-Options":        "DENY",
		"Referrer-Policy":        "no-referrer",
	}
	for k, v := range want {
		if got := w.Header().Get(k); got != v {
			t.Errorf("%s = %q, want %q", k, got, v)
		}
	}
	csp := w.Header().Get("Content-Security-Policy")
	if !strings.Contains(csp, "frame-ancestors 'none'") || !strings.Contains(csp, "default-src 'self'") {
		t.Errorf("Content-Security-Policy = %q, want frame-ancestors 'none' and default-src 'self'", csp)
	}
}

// clientIP must come from the socket (RemoteAddr), never from the spoofable
// X-Forwarded-For header — the set log is sold as an audit trail.
func TestClientIPIgnoresForwardedFor(t *testing.T) {
	r := httptest.NewRequest(http.MethodPost, "/api/set", nil)
	r.RemoteAddr = "192.168.1.7:54321"
	r.Header.Set("X-Forwarded-For", "6.6.6.6")
	if got := clientIP(r); got != "192.168.1.7" {
		t.Errorf("clientIP = %q, want %q (socket address, not XFF)", got, "192.168.1.7")
	}

	// IPv6 RemoteAddr must not be mangled by naive colon-splitting.
	r.RemoteAddr = "[fe80::1]:50000"
	if got := clientIP(r); got != "fe80::1" {
		t.Errorf("clientIP = %q, want %q", got, "fe80::1")
	}
}

func TestGuardWriteNoAuthConfigured(t *testing.T) {
	// With no token and no host allowlist, only the browser guards apply.
	s := &Server{}
	r := httptest.NewRequest(http.MethodPost, "http://anything/api/set", nil)
	r.Header.Set("Content-Type", "application/json")
	if !s.guardWrite(httptest.NewRecorder(), r) {
		t.Error("expected pass with browser guards satisfied and no token/host configured")
	}
	r.Header.Set("Content-Type", "text/plain")
	if s.guardWrite(httptest.NewRecorder(), r) {
		t.Error("expected text/plain to be rejected even without token")
	}
}
