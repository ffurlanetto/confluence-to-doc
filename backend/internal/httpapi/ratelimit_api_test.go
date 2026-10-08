package httpapi_test

import (
	"net/http"
	"testing"

	"github.com/ffurlanetto/confluence-to-doc/backend/internal/httpapi"
	"github.com/ffurlanetto/confluence-to-doc/backend/internal/ratelimit"
)

func TestAPIRateLimitPerUser(t *testing.T) {
	a := newAPIWithWorker(t, false, func(d *httpapi.Deps) {
		d.UserLimiter = ratelimit.NewKeyed(1, 2) // two requests, then one a minute
		d.AuthLimiter = ratelimit.NewKeyed(1, 1)
	})
	for i := range 2 {
		if r := a.do("GET", "/api/me", "alice", nil, nil); r.StatusCode != http.StatusOK {
			t.Fatalf("request %d = %d", i+1, r.StatusCode)
		}
	}
	var e apiError
	r := a.do("GET", "/api/me", "alice", nil, &e)
	if r.StatusCode != http.StatusTooManyRequests || e.Error.Code != "rate_limited" || r.Header.Get("Retry-After") == "" {
		t.Fatalf("3rd request = %d %+v retry-after=%q", r.StatusCode, e, r.Header.Get("Retry-After"))
	}
	if r := a.do("GET", "/api/me", "bob", nil, nil); r.StatusCode != http.StatusOK {
		t.Fatalf("another user is not affected: %d", r.StatusCode)
	}

	// Sign-in endpoints are limited per client address.
	if r := a.do("GET", "/auth/login", "", nil, nil); r.StatusCode == http.StatusTooManyRequests {
		t.Fatal("first sign-in attempt refused")
	}
	if r := a.do("GET", "/auth/login", "", nil, nil); r.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("second sign-in attempt = %d, want 429", r.StatusCode)
	}
	// Health probes are never limited.
	for range 3 {
		if r := a.do("GET", "/healthz", "", nil, nil); r.StatusCode != http.StatusOK {
			t.Fatalf("healthz = %d", r.StatusCode)
		}
	}
}
