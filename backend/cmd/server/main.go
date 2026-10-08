// Command server runs the Confluence-to-document exporter: the HTTP API (and
// SPA), the export workers and the retention janitor. APP_ROLE selects which
// components run so the API and workers can be scaled independently.
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/ffurlanetto/confluence-to-doc/backend/internal/account"
	"github.com/ffurlanetto/confluence-to-doc/backend/internal/audit"
	"github.com/ffurlanetto/confluence-to-doc/backend/internal/auth"
	"github.com/ffurlanetto/confluence-to-doc/backend/internal/config"
	"github.com/ffurlanetto/confluence-to-doc/backend/internal/converter"
	"github.com/ffurlanetto/confluence-to-doc/backend/internal/crypto"
	"github.com/ffurlanetto/confluence-to-doc/backend/internal/docx"
	"github.com/ffurlanetto/confluence-to-doc/backend/internal/export"
	"github.com/ffurlanetto/confluence-to-doc/backend/internal/httpapi"
	"github.com/ffurlanetto/confluence-to-doc/backend/internal/observability"
	"github.com/ffurlanetto/confluence-to-doc/backend/internal/ratelimit"
	"github.com/ffurlanetto/confluence-to-doc/backend/internal/storage"
	"github.com/ffurlanetto/confluence-to-doc/backend/internal/store"
)

// version is set at build time with -ldflags "-X main.version=...".
var version = "dev"

func main() {
	// `server healthcheck` lets container runtimes probe the app without curl.
	if len(os.Args) > 1 && os.Args[1] == "healthcheck" {
		os.Exit(healthcheck())
	}
	if err := run(); err != nil {
		slog.Error("fatal", "err", err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("invalid configuration:\n%w", err)
	}
	setupLogger(cfg)
	slog.Info("starting", "version", version, "role", cfg.Role)

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	telemetry, err := observability.Setup(ctx, observability.Config{
		Endpoint:       cfg.Telemetry.Endpoint,
		Protocol:       cfg.Telemetry.Protocol,
		ServiceName:    cfg.Telemetry.ServiceName,
		ServiceVersion: version,
		SampleRatio:    cfg.Telemetry.SampleRatio,
		MetricInterval: cfg.Telemetry.MetricInterval,
		MetricPrefix:   cfg.Telemetry.MetricPrefix,
	})
	if err != nil {
		return err
	}
	defer func() {
		// Flush with a context of its own: ctx is already cancelled here. The
		// timeout stays short so an unreachable collector cannot hold up the
		// shutdown, which already budgets time for in-flight servers and jobs.
		flushCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := telemetry.Shutdown(flushCtx); err != nil {
			slog.Error("flushing telemetry", "err", err)
		}
	}()

	db, err := store.Open(ctx, cfg.DatabaseURL)
	if err != nil {
		return err
	}
	defer db.Close()
	if err := db.Migrate(ctx); err != nil {
		return fmt.Errorf("migrating database: %w", err)
	}

	sealer, err := crypto.NewKeyRing(cfg.EncryptionKeys)
	if err != nil {
		return err
	}
	blobs, err := newBlobStore(ctx, cfg)
	if err != nil {
		return err
	}
	accounts := account.NewService(db, sealer, cfg.ConfluenceBaseURL, cfg.ConfluenceTimeout)
	if cfg.ConfluenceRateLimit > 0 {
		// One budget for the whole deployment, however many instances run.
		accounts.WithLimiter(ratelimit.NewShared(db, "confluence", cfg.ConfluenceRateLimit, float64(cfg.ConfluenceRateBurst)))
	}
	auditor := audit.New(db, nil)

	// The company Word template is validated at startup: a broken template
	// must stop the process, not every export.
	var template *docx.Template
	if path := cfg.Export.WordTemplatePath; path != "" {
		template, err = docx.LoadTemplate(path)
		if err != nil {
			return err
		}
		slog.Info("document template: company Word template", "file", template.Name(),
			"default_paragraph_style", template.DefaultParagraphStyle(), "styles", len(template.Styles()))
	} else {
		slog.Info("document template: built-in styling")
	}

	var wg sync.WaitGroup
	var notify func()

	if cfg.Role.RunsWorker() {
		conv := converter.LibreOffice{Binary: cfg.Export.SofficePath, Template: template}
		pool := export.NewPool(db, accounts, conv, blobs, export.WorkerConfig{
			Concurrency:       cfg.Export.WorkerConcurrency,
			PollInterval:      cfg.Export.PollInterval,
			JobTimeout:        cfg.Export.JobTimeout,
			Lease:             time.Minute,
			Retention:         cfg.Export.Retention,
			MaxPages:          cfg.Export.MaxPages,
			MaxImageBytes:     cfg.Export.MaxImageBytes,
			ConfluenceWorkers: cfg.Export.ConfluenceWorkers,
			UseTemplateStyles: template != nil,
			Classification:    cfg.Export.Classification,
			Classifications:   cfg.Export.Classifications,
			ShutdownGrace:     20 * time.Second,
			Audit:             auditor,
		})
		notify = pool.Notify
		janitor := export.NewJanitor(db, blobs, cfg.Export.JanitorInterval).WithAudit(auditor, db, cfg.AuditRetention).
			WithKeyRotation(accounts)
		wg.Add(2)
		go func() { defer wg.Done(); pool.Run(ctx) }()
		go func() { defer wg.Done(); janitor.Run(ctx) }()
	}

	if telemetry.Enabled {
		if err := observability.RegisterQueueDepth(db.QueueStats); err != nil {
			return fmt.Errorf("registering queue metrics: %w", err)
		}
	}

	var servers []*http.Server
	if cfg.Role.RunsAPI() {
		authenticator, err := connectOIDC(ctx, cfg, db, sealer, auditor)
		if err != nil {
			return err
		}
		router := httpapi.NewRouter(httpapi.Deps{
			Auth:                  authenticator,
			Accounts:              accounts,
			Exports:               export.NewService(db, blobs, export.Limits{MaxAttempts: cfg.Export.MaxAttempts, MaxActivePerUser: cfg.Export.MaxActivePerUser}, notify),
			Audit:                 auditor,
			AuditLog:              db,
			TrustedProxies:        cfg.TrustedProxies,
			UserLimiter:           keyedLimiter(cfg.APIRateLimit, cfg.APIRateBurst),
			AuthLimiter:           keyedLimiter(cfg.AuthRateLimit, max(cfg.AuthRateLimit/3, 1)),
			Ready:                 db.Ping,
			PublicURL:             cfg.PublicURL,
			StaticDir:             cfg.StaticDir,
			Retention:             cfg.Export.Retention,
			MaxPages:              cfg.Export.MaxPages,
			DocumentTemplate:      templateName(template),
			Classifications:       cfg.Export.Classifications,
			DefaultClassification: cfg.Export.Classification,
		})
		servers = append(servers, newServer(cfg.HTTPAddr, router))
	} else {
		// A worker serves no API, but an orchestrator still needs somewhere to
		// send liveness and readiness probes.
		servers = append(servers, newServer(cfg.HTTPAddr, healthHandler(db.Ping)))
	}

	errCh := make(chan error, len(servers))
	for _, s := range servers {
		slog.Info("listening", "addr", s.Addr)
		go func() {
			if err := s.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
				errCh <- err
			}
		}()
	}

	select {
	case <-ctx.Done():
		slog.Info("shutdown requested")
	case err = <-errCh:
		slog.Error("server error, shutting down", "err", err)
		stop()
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	defer cancel()
	for _, s := range servers {
		_ = s.Shutdown(shutdownCtx)
	}
	wg.Wait() // workers release or finish their jobs
	slog.Info("stopped")
	return err
}

func healthcheck() int {
	// Always probe the loopback interface; only the port comes from HTTP_ADDR.
	port := "8080"
	if _, p, err := net.SplitHostPort(os.Getenv("HTTP_ADDR")); err == nil && p != "" {
		port = p
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	url := "http://" + net.JoinHostPort("127.0.0.1", port) + "/healthz"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil) //nolint:gosec // G704: see below
	if err != nil {
		return 1
	}
	resp, err := http.DefaultClient.Do(req) //nolint:gosec // G704: loopback-only URL, port from operator config
	if err != nil {
		return 1
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return 1
	}
	return 0
}

// newBlobStore is the storage feature flag: S3 when a bucket is configured,
// local disk otherwise.
// healthHandler exposes the probe endpoints on their own, for processes that
// do not run the API.
func healthHandler(ready func(context.Context) error) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		writeStatus(w, http.StatusOK, "ok")
	})
	mux.HandleFunc("GET /readyz", func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
		defer cancel()
		if err := ready(ctx); err != nil {
			writeStatus(w, http.StatusServiceUnavailable, "unavailable")
			return
		}
		writeStatus(w, http.StatusOK, "ready")
	})
	return mux
}

func writeStatus(w http.ResponseWriter, code int, status string) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	_, _ = fmt.Fprintf(w, `{"status":%q}`, status)
}

// templateName is the template file name shown in the API, empty when none.
func templateName(t *docx.Template) string {
	if t == nil {
		return ""
	}
	return t.Name()
}

// keyedLimiter returns nil, meaning no limit, when perMinute is 0. The
// explicit nil interface matters: a typed nil would be called.
func keyedLimiter(perMinute, burst int) httpapi.Limiter {
	if perMinute <= 0 {
		return nil
	}
	return ratelimit.NewKeyed(perMinute, burst)
}

func newBlobStore(ctx context.Context, cfg *config.Config) (storage.BlobStore, error) {
	if !cfg.S3.Enabled() {
		slog.Info("document storage: local disk", "dir", cfg.Export.StorageDir)
		return storage.NewLocal(cfg.Export.StorageDir)
	}
	s3cfg := cfg.S3
	slog.Info("document storage: S3", "bucket", s3cfg.Bucket, "prefix", s3cfg.Prefix,
		"endpoint", s3cfg.Endpoint, "region", s3cfg.Region, "static_credentials", s3cfg.AccessKeyID != "")
	checkCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	return storage.NewS3(checkCtx, storage.S3Options{
		Bucket: s3cfg.Bucket, Region: s3cfg.Region, Endpoint: s3cfg.Endpoint,
		AccessKeyID: s3cfg.AccessKeyID, SecretAccessKey: s3cfg.SecretAccessKey,
		Prefix: s3cfg.Prefix, UsePathStyle: s3cfg.UsePathStyle,
	})
}

func newServer(addr string, h http.Handler) *http.Server {
	return &http.Server{
		Addr:              addr,
		Handler:           h,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      5 * time.Minute, // large downloads
		IdleTimeout:       2 * time.Minute,
		MaxHeaderBytes:    64 << 10,
	}
}

// connectOIDC retries discovery so the app can start before its IdP (e.g.
// in docker compose) without crash-looping.
func connectOIDC(ctx context.Context, cfg *config.Config, db *store.Store, sealer *crypto.Sealer, auditor *audit.Recorder) (*auth.Authenticator, error) {
	acfg := auth.Config{
		IssuerURL: cfg.OIDC.IssuerURL, ClientID: cfg.OIDC.ClientID, ClientSecret: cfg.OIDC.ClientSecret,
		Scopes: cfg.OIDC.Scopes, PublicURL: cfg.PublicURL, SessionTTL: cfg.SessionTTL, SecureCookie: cfg.SecureCookies(),
		GroupsClaim: cfg.OIDC.GroupsClaim, AdminGroups: cfg.OIDC.AdminGroups,
	}
	if len(acfg.AdminGroups) == 0 {
		slog.Warn("OIDC_ADMIN_GROUPS is empty: nobody can use the administration pages")
	}
	var lastErr error
	for attempt := range 30 {
		a, err := auth.New(ctx, acfg, db, sealer, auditor)
		if err == nil {
			return a, nil
		}
		lastErr = err
		slog.Warn("OIDC provider not reachable yet", "attempt", attempt+1, "err", err)
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(2 * time.Second):
		}
	}
	return nil, lastErr
}

func setupLogger(cfg *config.Config) {
	var level slog.Level
	if err := level.UnmarshalText([]byte(cfg.LogLevel)); err != nil {
		level = slog.LevelInfo
	}
	opts := &slog.HandlerOptions{Level: level}
	var h slog.Handler
	if strings.EqualFold(cfg.LogFormat, "text") {
		h = slog.NewTextHandler(os.Stdout, opts)
	} else {
		h = slog.NewJSONHandler(os.Stdout, opts)
	}
	// Logs emitted inside a span carry its trace and span ids, so a log line
	// can be followed into the trace and back.
	slog.SetDefault(slog.New(observability.LogHandler{Handler: h}))
}
