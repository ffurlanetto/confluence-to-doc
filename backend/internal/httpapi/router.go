// Package httpapi exposes the REST API consumed by the React SPA, the
// authentication endpoints and, optionally, the SPA static files.
package httpapi

import (
	"context"
	"io"
	"net/http"
	"net/netip"
	"net/url"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/google/uuid"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"

	"github.com/ffurlanetto/confluence-to-doc/backend/internal/confluence"
	"github.com/ffurlanetto/confluence-to-doc/backend/internal/domain"
	"github.com/ffurlanetto/confluence-to-doc/backend/internal/export"
)

// Authenticator is implemented by auth.Authenticator (and by fakes in tests).
type Authenticator interface {
	Login(http.ResponseWriter, *http.Request)
	Callback(http.ResponseWriter, *http.Request)
	Logout(http.ResponseWriter, *http.Request)
	BackchannelLogout(http.ResponseWriter, *http.Request)
	Middleware(http.Handler) http.Handler
}

type Accounts interface {
	ConfluenceURL() *url.URL
	Preferences(ctx context.Context, userID uuid.UUID) (*domain.Preferences, error)
	SetPAT(ctx context.Context, userID uuid.UUID, pat string) (*confluence.User, error)
	ClearPAT(ctx context.Context, userID uuid.UUID) error
	SetDefaultFormat(ctx context.Context, userID uuid.UUID, f domain.Format) error
	Client(ctx context.Context, userID uuid.UUID) (*confluence.Client, error)
}

type Exports interface {
	Create(ctx context.Context, req export.CreateRequest) (*domain.Export, error)
	List(ctx context.Context, userID uuid.UUID) ([]*domain.Export, error)
	Get(ctx context.Context, id, userID uuid.UUID) (*domain.Export, error)
	Delete(ctx context.Context, id, userID uuid.UUID) error
	Open(ctx context.Context, id, userID uuid.UUID) (*domain.Export, io.ReadSeekCloser, error)
}

// Auditor records security events (implemented by audit.Recorder).
type Auditor interface {
	Record(ctx context.Context, e domain.AuditEvent)
}

// Limiter is implemented by ratelimit.Keyed.
type Limiter interface {
	Allow(key string) (bool, time.Duration)
}

// AuditLog reads the audit trail back.
type AuditLog interface {
	ListAuditEvents(ctx context.Context, f domain.AuditFilter) ([]domain.AuditEvent, error)
}

// Notifications are the user's messages and channel settings (implemented
// by notify.Service).
type Notifications interface {
	List(ctx context.Context, userID uuid.UUID) ([]domain.Notification, int, error)
	MarkRead(ctx context.Context, userID uuid.UUID) error
	EmailAvailable() bool
	SetPreferences(ctx context.Context, userID uuid.UUID, email, exports bool) error
	SetTeamsWebhook(ctx context.Context, userID uuid.UUID, rawURL string) error
	ClearTeamsWebhook(ctx context.Context, userID uuid.UUID) error
	TestTeams(ctx context.Context, userID uuid.UUID) error
}

// Lifecycle deletes accounts (implemented by account.Lifecycle).
type Lifecycle interface {
	DeleteAccount(ctx context.Context, u *domain.User) error
}

type Deps struct {
	Auth          Authenticator
	Accounts      Accounts
	Exports       Exports
	Audit         Auditor
	Lifecycle     Lifecycle
	Notifications Notifications
	AuditLog      AuditLog
	// TrustedProxies may set X-Forwarded-For (see config.TrustedProxies).
	TrustedProxies []netip.Prefix
	// UserLimiter bounds API requests per user, AuthLimiter sign-in requests
	// per client address. Nil means no limit.
	UserLimiter Limiter
	AuthLimiter Limiter
	Ready       func(context.Context) error
	PublicURL   *url.URL
	StaticDir   string
	Retention   time.Duration
	MaxPages    int
	// DocumentTemplate is the company Word template file name, empty when
	// documents use the built-in styling.
	DocumentTemplate string
	// Classifications are the levels offered when exporting (empty: no
	// choice); DefaultClassification applies when the user picks none.
	Classifications       []domain.Classification
	DefaultClassification string
}

func NewRouter(d Deps) http.Handler {
	r := chi.NewRouter()
	r.Use(middleware.RequestID, traceRoute, accessLog, middleware.Recoverer,
		securityHeaders(d.PublicURL.Scheme == "https"), requestInfo(d.TrustedProxies))

	r.Get("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})
	r.Get("/readyz", func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
		defer cancel()
		if err := d.Ready(ctx); err != nil {
			writeJSON(w, http.StatusServiceUnavailable, map[string]string{"status": "unavailable"})
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"status": "ready"})
	})

	r.Route("/auth", func(r chi.Router) {
		r.Use(limitBy(d.AuthLimiter, clientKey))
		r.Get("/login", d.Auth.Login)
		r.Get("/callback", d.Auth.Callback)
		r.With(csrfProtect(d.PublicURL)).Post("/logout", d.Auth.Logout)
		// Called by the identity provider, server to server: the signed
		// logout token is the proof, not a same-origin request.
		r.Post("/backchannel-logout", d.Auth.BackchannelLogout)
	})

	h := &handlers{d: d}
	r.Route("/api", func(r chi.Router) {
		r.Use(csrfProtect(d.PublicURL), d.Auth.Middleware, limitBy(d.UserLimiter, userKey), middleware.NoCache)
		r.Use(func(next http.Handler) http.Handler {
			return http.MaxBytesHandler(next, 64<<10) // JSON payloads are tiny
		})

		r.Get("/me", h.me)
		r.Delete("/me", h.deleteMe)
		r.Get("/preferences", h.getPreferences)
		r.Put("/preferences", h.putPreferences)
		r.Put("/preferences/pat", h.putPAT)
		r.Put("/preferences/notifications", h.putNotificationPreferences)
		r.Put("/preferences/teams", h.putTeams)
		r.Delete("/preferences/teams", h.deleteTeams)
		r.Post("/preferences/teams/test", h.testTeams)
		r.Get("/notifications", h.listNotifications)
		r.Post("/notifications/read", h.readNotifications)
		r.Delete("/preferences/pat", h.deletePAT)

		r.Get("/confluence/pages", h.searchPages)
		r.Get("/confluence/pages/{pageID}", h.getPage)
		r.Get("/confluence/pages/{pageID}/children", h.getChildren)

		r.Get("/exports", h.listExports)
		r.Post("/exports", h.createExport)
		r.Get("/exports/{exportID}", h.getExport)
		r.Delete("/exports/{exportID}", h.deleteExport)
		r.Get("/exports/{exportID}/download", h.download)

		r.Route("/admin", func(r chi.Router) {
			r.Use(h.requireAdmin)
			r.Get("/audit", h.listAudit)
		})
	})

	if d.StaticDir != "" {
		r.NotFound(spaHandler(d.StaticDir).ServeHTTP)
	}
	// otelhttp opens the server span and records the request metric; the route
	// pattern is filled in by traceRoute once chi has matched.
	return otelhttp.NewHandler(r, "http.server")
}
