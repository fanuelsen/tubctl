package web

import (
	"net/http"
	"net/http/httptest"
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
