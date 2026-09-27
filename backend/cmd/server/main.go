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

	"github.com/prometheus/client_golang/prometheus/promhttp"

	"github.com/ffurlanetto/confluence-to-doc/backend/internal/account"
	"github.com/ffurlanetto/confluence-to-doc/backend/internal/auth"
	"github.com/ffurlanetto/confluence-to-doc/backend/internal/config"
	"github.com/ffurlanetto/confluence-to-doc/backend/internal/converter"
	"github.com/ffurlanetto/confluence-to-doc/backend/internal/crypto"
	"github.com/ffurlanetto/confluence-to-doc/backend/internal/export"
	"github.com/ffurlanetto/confluence-to-doc/backend/internal/httpapi"
	"github.com/ffurlanetto/confluence-to-doc/backend/internal/metrics"
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

	db, err := store.Open(ctx, cfg.DatabaseURL)
	if err != nil {
		return err
	}
	defer db.Close()
	if err := db.Migrate(ctx); err != nil {
		return fmt.Errorf("migrating database: %w", err)
	}

	sealer, err := crypto.NewSealer(cfg.EncryptionKey)
	if err != nil {
		return err
	}
	blobs, err := storage.NewLocal(cfg.Export.StorageDir)
	if err != nil {
		return err
	}
	accounts := account.NewService(db, sealer, cfg.ConfluenceBaseURL, cfg.ConfluenceTimeout)

	var wg sync.WaitGroup
	var notify func()

	if cfg.Role.RunsWorker() {
		pool := export.NewPool(db, accounts, converter.LibreOffice{Binary: cfg.Export.SofficePath}, blobs, export.WorkerConfig{
			Concurrency:       cfg.Export.WorkerConcurrency,
			PollInterval:      cfg.Export.PollInterval,
			JobTimeout:        cfg.Export.JobTimeout,
			Lease:             time.Minute,
			Retention:         cfg.Export.Retention,
			MaxPages:          cfg.Export.MaxPages,
			MaxImageBytes:     cfg.Export.MaxImageBytes,
			ConfluenceWorkers: cfg.Export.ConfluenceWorkers,
			ShutdownGrace:     20 * time.Second,
		})
		notify = pool.Notify
		janitor := export.NewJanitor(db, blobs, cfg.Export.JanitorInterval)
		wg.Add(2)
		go func() { defer wg.Done(); pool.Run(ctx) }()
		go func() { defer wg.Done(); janitor.Run(ctx) }()
	}

	var servers []*http.Server
	if cfg.MetricsAddr != "" {
		reg := metrics.NewRegistry(db.QueueStats)
		mux := http.NewServeMux()
		mux.Handle("/metrics", promhttp.HandlerFor(reg, promhttp.HandlerOpts{}))
		servers = append(servers, newServer(cfg.MetricsAddr, mux))
	}

	if cfg.Role.RunsAPI() {
		authenticator, err := connectOIDC(ctx, cfg, db, sealer)
		if err != nil {
			return err
		}
		router := httpapi.NewRouter(httpapi.Deps{
			Auth:      authenticator,
			Accounts:  accounts,
			Exports:   export.NewService(db, blobs, export.Limits{MaxAttempts: cfg.Export.MaxAttempts, MaxActivePerUser: cfg.Export.MaxActivePerUser}, notify),
			Ready:     db.Ping,
			PublicURL: cfg.PublicURL,
			StaticDir: cfg.StaticDir,
			Retention: cfg.Export.Retention,
			MaxPages:  cfg.Export.MaxPages,
		})
		servers = append(servers, newServer(cfg.HTTPAddr, router))
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
func connectOIDC(ctx context.Context, cfg *config.Config, db *store.Store, sealer *crypto.Sealer) (*auth.Authenticator, error) {
	acfg := auth.Config{
		IssuerURL: cfg.OIDC.IssuerURL, ClientID: cfg.OIDC.ClientID, ClientSecret: cfg.OIDC.ClientSecret,
		Scopes: cfg.OIDC.Scopes, PublicURL: cfg.PublicURL, SessionTTL: cfg.SessionTTL, SecureCookie: cfg.SecureCookies(),
	}
	var lastErr error
	for attempt := range 30 {
		a, err := auth.New(ctx, acfg, db, sealer)
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
	slog.SetDefault(slog.New(h))
}
