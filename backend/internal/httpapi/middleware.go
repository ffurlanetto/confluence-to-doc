package httpapi

import (
	"log/slog"
	"math"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	semconv "go.opentelemetry.io/otel/semconv/v1.26.0"
	"go.opentelemetry.io/otel/trace"

	"github.com/ffurlanetto/confluence-to-doc/backend/internal/audit"
	"github.com/ffurlanetto/confluence-to-doc/backend/internal/auth"
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

// traceRoute completes the span and the HTTP metric started by otelhttp with
// the chi route pattern, which is only known once routing has happened.
func traceRoute(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		next.ServeHTTP(w, r)
		route := chi.RouteContext(r.Context()).RoutePattern()
		if route == "" {
			return
		}
		if labeler, ok := otelhttp.LabelerFromContext(r.Context()); ok {
			labeler.Add(semconv.HTTPRoute(route))
		}
		if span := trace.SpanFromContext(r.Context()); span.IsRecording() {
			span.SetName(r.Method + " " + route)
			span.SetAttributes(semconv.HTTPRoute(route))
		}
	})
}

// accessLog logs one structured line per request.
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

// limitBy refuses requests over the limit with 429 and a Retry-After header.
// A nil limiter lets everything through.
func limitBy(l Limiter, key func(*http.Request) string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		if l == nil {
			return next
		}
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ok, wait := l.Allow(key(r))
			if !ok {
				seconds := int(math.Ceil(wait.Seconds()))
				w.Header().Set("Retry-After", strconv.Itoa(max(seconds, 1)))
				slog.WarnContext(r.Context(), "rate limit exceeded", "path", r.URL.Path, "retry_after_s", seconds)
				writeError(w, r, http.StatusTooManyRequests, "rate_limited", "Too many requests. Slow down and try again shortly.")
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// userKey identifies the authenticated user; clientKey the client address
// (behind trusted proxies, the one they report).
func userKey(r *http.Request) string { return "user:" + auth.UserFrom(r.Context()).ID.String() }

func clientKey(r *http.Request) string { return "ip:" + audit.RequestFrom(r.Context()).ClientIP }
