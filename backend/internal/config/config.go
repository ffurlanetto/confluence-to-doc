// Package config loads and validates the application configuration from
// environment variables (12-factor style). Every setting has a single source
// of truth here; the rest of the code receives typed values.
package config

import (
	"encoding/base64"
	"errors"
	"fmt"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

// Role selects which components a process runs, so API and workers can be
// scaled independently while sharing a single binary.
type Role string

const (
	RoleAll    Role = "all"
	RoleAPI    Role = "api"
	RoleWorker Role = "worker"
)

// RunsAPI reports whether the HTTP API must be started.
func (r Role) RunsAPI() bool { return r == RoleAll || r == RoleAPI }

// RunsWorker reports whether export workers must be started.
func (r Role) RunsWorker() bool { return r == RoleAll || r == RoleWorker }

type Config struct {
	Role     Role
	HTTPAddr string
	// MetricsAddr serves /metrics on a separate, non-public listener ("" disables it).
	MetricsAddr string
	// PublicURL is the externally visible base URL of the application
	// (used to build the OAuth2 redirect URI and to check request origins).
	PublicURL *url.URL
	StaticDir string
	LogLevel  string
	LogFormat string

	DatabaseURL string

	OIDC OIDCConfig

	SessionTTL time.Duration
	// EncryptionKey is a 32-byte AES-256 key used to encrypt PATs at rest.
	EncryptionKey []byte

	ConfluenceBaseURL *url.URL
	ConfluenceTimeout time.Duration

	Export ExportConfig
}

type OIDCConfig struct {
	IssuerURL    string
	ClientID     string
	ClientSecret string
	Scopes       []string
}

type ExportConfig struct {
	StorageDir string
	// Retention is how long a generated document stays downloadable.
	Retention         time.Duration
	WorkerConcurrency int
	PollInterval      time.Duration
	JobTimeout        time.Duration
	MaxAttempts       int
	MaxPages          int
	MaxImageBytes     int64
	// MaxActivePerUser caps queued+running exports per user (fair usage).
	MaxActivePerUser  int
	JanitorInterval   time.Duration
	SofficePath       string
	ConfluenceWorkers int
}

// Load reads the configuration from the environment.
func Load() (*Config, error) { return load(os.Getenv) }

func load(getenv func(string) string) (*Config, error) {
	e := &envReader{getenv: getenv}

	cfg := &Config{
		Role:        Role(e.str("APP_ROLE", string(RoleAll))),
		HTTPAddr:    e.str("HTTP_ADDR", ":8080"),
		MetricsAddr: e.str("METRICS_ADDR", ":9090"),
		StaticDir:   e.str("STATIC_DIR", ""),
		LogLevel:    e.str("LOG_LEVEL", "info"),
		LogFormat:   e.str("LOG_FORMAT", "json"),
		DatabaseURL: e.required("DATABASE_URL"),
		OIDC: OIDCConfig{
			IssuerURL:    e.required("OIDC_ISSUER_URL"),
			ClientID:     e.required("OIDC_CLIENT_ID"),
			ClientSecret: e.required("OIDC_CLIENT_SECRET"),
			Scopes:       e.list("OIDC_SCOPES", []string{"openid", "profile", "email"}),
		},
		SessionTTL:        e.duration("SESSION_TTL", 12*time.Hour),
		ConfluenceTimeout: e.duration("CONFLUENCE_TIMEOUT", 30*time.Second),
		Export: ExportConfig{
			StorageDir:        e.str("EXPORT_STORAGE_DIR", "./data/exports"),
			Retention:         e.duration("EXPORT_RETENTION", 48*time.Hour),
			WorkerConcurrency: e.int("EXPORT_WORKER_CONCURRENCY", 2),
			PollInterval:      e.duration("EXPORT_POLL_INTERVAL", 2*time.Second),
			JobTimeout:        e.duration("EXPORT_JOB_TIMEOUT", 15*time.Minute),
			MaxAttempts:       e.int("EXPORT_MAX_ATTEMPTS", 3),
			MaxPages:          e.int("EXPORT_MAX_PAGES", 500),
			MaxImageBytes:     int64(e.int("EXPORT_MAX_IMAGE_BYTES", 10<<20)),
			MaxActivePerUser:  e.int("EXPORT_MAX_ACTIVE_PER_USER", 5),
			JanitorInterval:   e.duration("EXPORT_JANITOR_INTERVAL", 10*time.Minute),
			SofficePath:       e.str("SOFFICE_PATH", "soffice"),
			ConfluenceWorkers: e.int("CONFLUENCE_FETCH_CONCURRENCY", 4),
		},
	}
	cfg.PublicURL = e.url("PUBLIC_URL", "http://localhost:8080")
	cfg.ConfluenceBaseURL = e.url("CONFLUENCE_BASE_URL", "")
	cfg.EncryptionKey = e.base64Key("ENCRYPTION_KEY", 32)

	if len(e.errs) > 0 {
		return nil, errors.Join(e.errs...)
	}
	if err := cfg.validate(); err != nil {
		return nil, err
	}
	return cfg, nil
}

func (c *Config) validate() error {
	var errs []error
	switch c.Role {
	case RoleAll, RoleAPI, RoleWorker:
	default:
		errs = append(errs, fmt.Errorf("APP_ROLE: invalid value %q (want all, api or worker)", c.Role))
	}
	if c.Export.WorkerConcurrency < 1 {
		errs = append(errs, errors.New("EXPORT_WORKER_CONCURRENCY must be >= 1"))
	}
	if c.Export.MaxAttempts < 1 {
		errs = append(errs, errors.New("EXPORT_MAX_ATTEMPTS must be >= 1"))
	}
	if c.Export.MaxPages < 1 {
		errs = append(errs, errors.New("EXPORT_MAX_PAGES must be >= 1"))
	}
	if c.Export.Retention <= 0 {
		errs = append(errs, errors.New("EXPORT_RETENTION must be > 0"))
	}
	if c.Export.ConfluenceWorkers < 1 {
		errs = append(errs, errors.New("CONFLUENCE_FETCH_CONCURRENCY must be >= 1"))
	}
	return errors.Join(errs...)
}

// SecureCookies reports whether cookies must carry the Secure attribute.
func (c *Config) SecureCookies() bool { return c.PublicURL.Scheme == "https" }

type envReader struct {
	getenv func(string) string
	errs   []error
}

func (e *envReader) str(key, def string) string {
	if v := strings.TrimSpace(e.getenv(key)); v != "" {
		return v
	}
	return def
}

func (e *envReader) required(key string) string {
	v := strings.TrimSpace(e.getenv(key))
	if v == "" {
		e.errs = append(e.errs, fmt.Errorf("%s is required", key))
	}
	return v
}

func (e *envReader) int(key string, def int) int {
	v := e.str(key, "")
	if v == "" {
		return def
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		e.errs = append(e.errs, fmt.Errorf("%s: invalid integer %q", key, v))
		return def
	}
	return n
}

func (e *envReader) duration(key string, def time.Duration) time.Duration {
	v := e.str(key, "")
	if v == "" {
		return def
	}
	d, err := time.ParseDuration(v)
	if err != nil {
		e.errs = append(e.errs, fmt.Errorf("%s: invalid duration %q", key, v))
		return def
	}
	return d
}

func (e *envReader) list(key string, def []string) []string {
	v := e.str(key, "")
	if v == "" {
		return def
	}
	return strings.FieldsFunc(v, func(r rune) bool { return r == ',' || r == ' ' })
}

func (e *envReader) url(key, def string) *url.URL {
	v := e.str(key, def)
	if v == "" {
		e.errs = append(e.errs, fmt.Errorf("%s is required", key))
		return nil
	}
	u, err := url.Parse(strings.TrimRight(v, "/"))
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		e.errs = append(e.errs, fmt.Errorf("%s: invalid absolute http(s) URL %q", key, v))
		return nil
	}
	return u
}

func (e *envReader) base64Key(key string, size int) []byte {
	v := e.required(key)
	if v == "" {
		return nil
	}
	b, err := base64.StdEncoding.DecodeString(v)
	if err != nil || len(b) != size {
		e.errs = append(e.errs, fmt.Errorf("%s must be %d random bytes, base64-encoded (e.g. `openssl rand -base64 %d`)", key, size, size))
		return nil
	}
	return b
}
