package httpapi

import (
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"

	"github.com/ffurlanetto/confluence-to-doc/backend/internal/metrics"
)

// securityHeaders applies a strict baseline suitable for an SPA served from
// the same origin as its API.
func securityHeaders(secure bool) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			h := w.Header()
			h.Set("Content-Security-Policy", "default-src 'self'; img-src 'self' data:; style-src 'self'; script-src 'self'; "+
				"connect-src 'self'; object-src 'none'; base-uri 'self'; form-action 'self'; frame-ancestors 'none'")
			h.Set("X-Content-Type-Options", "nosniff")
			h.Set("X-Frame-Options", "DENY")
			h.Set("Referrer-Policy", "same-origin")
			h.Set("Permissions-Policy", "camera=(), microphone=(), geolocation=()")
			h.Set("Cross-Origin-Opener-Policy", "same-origin")
			if secure {
				h.Set("Strict-Transport-Security", "max-age=63072000; includeSubDomains")
			}
			next.ServeHTTP(w, r)
		})
	}
}

// csrfHeader must accompany every state-changing request. Browsers cannot
// send a custom header cross-origin without a CORS preflight (which we never
// allow), so together with SameSite cookies this defeats CSRF.
const csrfHeader = "X-CSRF-Protection"

func csrfProtect(publicURL *url.URL) func(http.Handler) http.Handler {
	origin := publicURL.Scheme + "://" + publicURL.Host
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch r.Method {
			case http.MethodGet, http.MethodHead, http.MethodOptions:
				next.ServeHTTP(w, r)
				return
			}
			if r.Header.Get(csrfHeader) != "1" {
				writeError(w, r, http.StatusForbidden, "csrf", "missing "+csrfHeader+" header")
				return
			}
			if o := r.Header.Get("Origin"); o != "" && !strings.EqualFold(o, origin) {
				writeError(w, r, http.StatusForbidden, "csrf", "cross-origin request refused")
				return
			}
			if r.Header.Get("Sec-Fetch-Site") == "cross-site" {
				writeError(w, r, http.StatusForbidden, "csrf", "cross-site request refused")
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// accessLog logs one structured line per request and records latency metrics.
func accessLog(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		ww := middleware.NewWrapResponseWriter(w, r.ProtoMajor)
		next.ServeHTTP(ww, r)

		route := chi.RouteContext(r.Context()).RoutePattern()
		if route == "" {
			route = "unmatched"
		}
		status := ww.Status()
		if status == 0 {
			status = http.StatusOK
		}
		dur := time.Since(start)
		metrics.HTTPRequests.WithLabelValues(route, r.Method, strconv.Itoa(status)).Observe(dur.Seconds())

		level := slog.LevelInfo
		switch {
		case status >= 500:
			level = slog.LevelError
		case route == "/healthz" || route == "/readyz" || strings.HasPrefix(route, "/assets/"):
			level = slog.LevelDebug
		}
		slog.Log(r.Context(), level, "http request",
			"method", r.Method, "route", route, "path", r.URL.Path, "status", status,
			"duration_ms", dur.Milliseconds(), "bytes", ww.BytesWritten(),
			"request_id", middleware.GetReqID(r.Context()))
	})
}
