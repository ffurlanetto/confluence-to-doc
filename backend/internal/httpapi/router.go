// Package httpapi exposes the REST API consumed by the React SPA, the
// authentication endpoints and, optionally, the SPA static files.
package httpapi

import (
	"context"
	"io"
	"net/http"
	"net/url"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/google/uuid"

	"github.com/ffurlanetto/confluence-to-doc/backend/internal/confluence"
	"github.com/ffurlanetto/confluence-to-doc/backend/internal/domain"
	"github.com/ffurlanetto/confluence-to-doc/backend/internal/export"
)

// Authenticator is implemented by auth.Authenticator (and by fakes in tests).
type Authenticator interface {
	Login(http.ResponseWriter, *http.Request)
	Callback(http.ResponseWriter, *http.Request)
	Logout(http.ResponseWriter, *http.Request)
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

type Deps struct {
	Auth      Authenticator
	Accounts  Accounts
	Exports   Exports
	Ready     func(context.Context) error
	PublicURL *url.URL
	StaticDir string
	Retention time.Duration
	MaxPages  int
	// DocumentTemplate is the company Word template file name, empty when
	// documents use the built-in styling.
	DocumentTemplate string
}

func NewRouter(d Deps) http.Handler {
	r := chi.NewRouter()
	r.Use(middleware.RequestID, accessLog, middleware.Recoverer,
		securityHeaders(d.PublicURL.Scheme == "https"))

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
		r.Get("/login", d.Auth.Login)
		r.Get("/callback", d.Auth.Callback)
		r.With(csrfProtect(d.PublicURL)).Post("/logout", d.Auth.Logout)
	})

	h := &handlers{d: d}
	r.Route("/api", func(r chi.Router) {
		r.Use(csrfProtect(d.PublicURL), d.Auth.Middleware, middleware.NoCache)
		r.Use(func(next http.Handler) http.Handler {
			return http.MaxBytesHandler(next, 64<<10) // JSON payloads are tiny
		})

		r.Get("/me", h.me)
		r.Get("/preferences", h.getPreferences)
		r.Put("/preferences", h.putPreferences)
		r.Put("/preferences/pat", h.putPAT)
		r.Delete("/preferences/pat", h.deletePAT)

		r.Get("/confluence/pages", h.searchPages)
		r.Get("/confluence/pages/{pageID}", h.getPage)
		r.Get("/confluence/pages/{pageID}/children", h.getChildren)

		r.Get("/exports", h.listExports)
		r.Post("/exports", h.createExport)
		r.Get("/exports/{exportID}", h.getExport)
		r.Delete("/exports/{exportID}", h.deleteExport)
		r.Get("/exports/{exportID}/download", h.download)
	})

	if d.StaticDir != "" {
		r.NotFound(spaHandler(d.StaticDir).ServeHTTP)
	}
	return r
}
