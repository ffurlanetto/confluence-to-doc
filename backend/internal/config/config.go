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
	"regexp"
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
	// Telemetry configures OpenTelemetry; it is enabled by an OTLP endpoint.
	Telemetry TelemetryConfig
	// S3 configures object storage for generated documents. It is enabled
	// when S3_BUCKET is set; otherwise documents are stored on local disk.
	S3 S3Config
}

// TelemetryConfig mirrors the standard OpenTelemetry environment variables we
// act on. Everything else (headers, TLS, compression, timeouts) is read
// directly by the OTLP exporters.
type TelemetryConfig struct {
	Endpoint       string
	Protocol       string
	ServiceName    string
	SampleRatio    float64
	MetricInterval time.Duration
	// MetricPrefix is prepended to every metric name (empty = no prefix). It
	// is not an OTEL_* variable because the specification defines none.
	MetricPrefix string
}

// Enabled is the telemetry feature flag: an OTLP endpoint turns it on.
func (c TelemetryConfig) Enabled() bool { return c.Endpoint != "" }

type S3Config struct {
	Bucket string
	Region string
	// Endpoint targets an S3-compatible service (MinIO, Ceph, Garage...);
	// empty means AWS S3.
	Endpoint string
	// Static credentials; when empty, the AWS default credential chain is
	// used (environment, shared config, IAM role / IRSA...).
	AccessKeyID     string
	SecretAccessKey string
	Prefix          string
	UsePathStyle    bool
}

// Enabled is the storage feature flag: S3 is used iff a bucket is configured.
func (c S3Config) Enabled() bool { return c.Bucket != "" }

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
	// WordTemplatePath points at a company Word template (.docx/.dotx)
	// applied to every generated document. Empty means default styling.
	WordTemplatePath string
}

// metricPrefixPattern keeps prefixes portable across metric backends; the
// trailing separator added by metricPrefix is allowed here too.
var metricPrefixPattern = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9._-]*$`)

// metricPrefix appends a separator when the operator did not write one, so
// TELEMETRY_METRIC_PREFIX=acme yields "acme.exports.created" rather than
// "acmeexports.created".
func metricPrefix(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" || strings.HasSuffix(raw, ".") || strings.HasSuffix(raw, "_") || strings.HasSuffix(raw, "-") {
		return raw
	}
	return raw + "."
}

// Load reads the configuration from the environment.
func Load() (*Config, error) { return load(os.Getenv) }

func load(getenv func(string) string) (*Config, error) {
	e := &envReader{getenv: getenv}

	cfg := &Config{
		Role:        Role(e.str("APP_ROLE", string(RoleAll))),
		HTTPAddr:    e.str("HTTP_ADDR", ":8080"),
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
			WordTemplatePath:  e.str("WORD_TEMPLATE_PATH", ""),
		},
	}
	cfg.Telemetry = TelemetryConfig{
		Endpoint:       strings.TrimRight(e.str("OTEL_EXPORTER_OTLP_ENDPOINT", ""), "/"),
		Protocol:       e.str("OTEL_EXPORTER_OTLP_PROTOCOL", "grpc"),
		ServiceName:    e.str("OTEL_SERVICE_NAME", "confluence-to-doc"),
		SampleRatio:    e.float("OTEL_TRACES_SAMPLER_ARG", 1),
		MetricInterval: e.duration("OTEL_METRIC_EXPORT_INTERVAL", time.Minute),
		MetricPrefix:   metricPrefix(e.str("TELEMETRY_METRIC_PREFIX", "")),
	}
	cfg.S3 = S3Config{
		Bucket:          e.str("S3_BUCKET", ""),
		Region:          e.str("S3_REGION", "us-east-1"),
		Endpoint:        strings.TrimRight(e.str("S3_ENDPOINT", ""), "/"),
		AccessKeyID:     e.str("S3_ACCESS_KEY_ID", ""),
		SecretAccessKey: e.str("S3_SECRET_ACCESS_KEY", ""),
		Prefix:          e.str("S3_PREFIX", "exports/"),
	}
	// Path-style addressing is what most S3-compatible services expect.
	cfg.S3.UsePathStyle = e.bool("S3_FORCE_PATH_STYLE", cfg.S3.Endpoint != "")
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
	if c.Telemetry.Enabled() {
		switch c.Telemetry.Protocol {
		case "grpc", "http/protobuf":
		default:
			errs = append(errs, fmt.Errorf("OTEL_EXPORTER_OTLP_PROTOCOL: invalid value %q (want grpc or http/protobuf)", c.Telemetry.Protocol))
		}
		if u, err := url.Parse(c.Telemetry.Endpoint); err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			errs = append(errs, fmt.Errorf("OTEL_EXPORTER_OTLP_ENDPOINT: invalid absolute http(s) URL %q", c.Telemetry.Endpoint))
		}
		if c.Telemetry.SampleRatio < 0 || c.Telemetry.SampleRatio > 1 {
			errs = append(errs, errors.New("OTEL_TRACES_SAMPLER_ARG must be between 0 and 1"))
		}
	}
	if p := c.Telemetry.MetricPrefix; p != "" && !metricPrefixPattern.MatchString(p) {
		errs = append(errs, fmt.Errorf("TELEMETRY_METRIC_PREFIX: %q must start with a letter and use only letters, digits, '.', '_' or '-'", p))
	}
	if c.S3.Enabled() {
		if (c.S3.AccessKeyID == "") != (c.S3.SecretAccessKey == "") {
			errs = append(errs, errors.New("S3_ACCESS_KEY_ID and S3_SECRET_ACCESS_KEY must be set together"))
		}
		if c.S3.Endpoint != "" {
			if u, err := url.Parse(c.S3.Endpoint); err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
				errs = append(errs, fmt.Errorf("S3_ENDPOINT: invalid absolute http(s) URL %q", c.S3.Endpoint))
			}
		}
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

func (e *envReader) float(key string, def float64) float64 {
	v := e.str(key, "")
	if v == "" {
		return def
	}
	f, err := strconv.ParseFloat(v, 64)
	if err != nil {
		e.errs = append(e.errs, fmt.Errorf("%s: invalid number %q", key, v))
		return def
	}
	return f
}

func (e *envReader) bool(key string, def bool) bool {
	v := e.str(key, "")
	if v == "" {
		return def
	}
	b, err := strconv.ParseBool(v)
	if err != nil {
		e.errs = append(e.errs, fmt.Errorf("%s: invalid boolean %q", key, v))
		return def
	}
	return b
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
