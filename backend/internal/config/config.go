// Package config loads and validates the application configuration from
// environment variables (12-factor style). Every setting has a single source
// of truth here; the rest of the code receives typed values.
package config

import (
	"encoding/base64"
	"errors"
	"fmt"
	"net/netip"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/ffurlanetto/confluence-to-doc/backend/internal/crypto"
	"github.com/ffurlanetto/confluence-to-doc/backend/internal/domain"
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
	// SessionRevalidateInterval is how often a session is re-checked with
	// the identity provider (0 = never).
	SessionRevalidateInterval time.Duration
	// AuditRetention is how long audit events are kept in the database.
	AuditRetention time.Duration
	// AccountRetention deletes accounts unused for that long (0 = never),
	// warning their owner AccountDeletionNotice beforehand.
	AccountRetention      time.Duration
	AccountDeletionNotice time.Duration
	// TrustedProxies are the reverse proxies whose X-Forwarded-For header is
	// believed when recording a client's address. Empty trusts nobody.
	TrustedProxies []netip.Prefix
	// EncryptionKeys encrypt PATs at rest: the first encrypts, all decrypt.
	// From ENCRYPTION_KEYS, or a single ENCRYPTION_KEY named "default".
	EncryptionKeys []crypto.Key

	ConfluenceBaseURL *url.URL
	ConfluenceTimeout time.Duration
	// ConfluenceRateLimit is the request budget, per second, that all
	// instances together may spend on Confluence (0 = unlimited).
	ConfluenceRateLimit float64
	ConfluenceRateBurst int

	// APIRateLimit and AuthRateLimit are requests per minute, per user on the
	// API and per client address on the sign-in endpoints (0 = unlimited).
	APIRateLimit  int
	APIRateBurst  int
	AuthRateLimit int

	Export ExportConfig
	// Telemetry configures OpenTelemetry; it is enabled by an OTLP endpoint.
	Telemetry TelemetryConfig
	// SMTP sends notifications by email; enabled by SMTP_HOST.
	SMTP SMTPConfig
	// TeamsWebhookHosts are the host suffixes a user's Teams workflow URL
	// may point to.
	TeamsWebhookHosts []string
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

type SMTPConfig struct {
	Host     string
	Port     int
	Username string
	Password string
	From     string
	Security string // starttls | tls | none
}

// Enabled is the email feature flag: a relay host turns it on.
func (c SMTPConfig) Enabled() bool { return c.Host != "" }

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
	// GroupsClaim names the claim carrying groups or roles (dotted path for
	// nested claims); AdminGroups are the values granting the admin role.
	GroupsClaim string
	AdminGroups []string
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
	// Language is the documents' default language, a BCP 47 tag; empty keeps
	// the Word template's (or en-US).
	Language string
	// WordTemplatePath points at a company Word template (.docx/.dotx)
	// applied to every generated document. Empty means default styling.
	WordTemplatePath string
	// Classification is the default classification: written to the subject
	// property of every document whose requester chose none.
	Classification string
	// Classifications are the levels a user may pick when exporting; the
	// marked ones put a watermark on every page. Empty disables the choice.
	Classifications []domain.Classification
}

// defaultClassifications is a common four-level scheme; most companies
// replace it with their own through DOCUMENT_CLASSIFICATIONS.
const defaultClassifications = "Public, Internal, Confidential:watermark, Restricted:watermark"

// maxClassificationLabel keeps the label printable in a footer and a watermark.
const maxClassificationLabel = 40

// languageTag accepts the usual BCP 47 shapes: fr, fr-FR, zh-Hant-TW.
var languageTag = regexp.MustCompile(`^[A-Za-z]{2,3}(-[A-Za-z0-9]{2,8})*$`)

// minAuditRetention keeps an investigation possible: a security incident is
// often noticed weeks after the fact.
const minAuditRetention = 30 * 24 * time.Hour

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
			GroupsClaim:  e.str("OIDC_GROUPS_CLAIM", "groups"),
			AdminGroups:  e.csv("OIDC_ADMIN_GROUPS"),
		},
		SessionTTL:                e.duration("SESSION_TTL", 12*time.Hour),
		SessionRevalidateInterval: e.duration("SESSION_REVALIDATE_INTERVAL", 15*time.Minute),
		AuditRetention:            e.duration("AUDIT_RETENTION", 365*24*time.Hour),
		AccountRetention:          e.duration("ACCOUNT_RETENTION", 180*24*time.Hour),
		AccountDeletionNotice:     e.duration("ACCOUNT_DELETION_NOTICE", 15*24*time.Hour),
		TrustedProxies:            e.prefixes("TRUSTED_PROXIES"),
		ConfluenceTimeout:         e.duration("CONFLUENCE_TIMEOUT", 30*time.Second),
		ConfluenceRateLimit:       e.float("CONFLUENCE_RATE_LIMIT", 10),
		ConfluenceRateBurst:       e.int("CONFLUENCE_RATE_BURST", 20),
		APIRateLimit:              e.int("API_RATE_LIMIT", 300),
		APIRateBurst:              e.int("API_RATE_BURST", 60),
		AuthRateLimit:             e.int("AUTH_RATE_LIMIT", 30),
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
			Language:          e.str("DOCUMENT_LANGUAGE", ""),
			Classification:    e.str("DOCUMENT_CLASSIFICATION", ""),
			Classifications:   e.classifications("DOCUMENT_CLASSIFICATIONS", defaultClassifications),
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
	cfg.SMTP = SMTPConfig{
		Host:     e.str("SMTP_HOST", ""),
		Port:     e.int("SMTP_PORT", 587),
		Username: e.str("SMTP_USERNAME", ""),
		Password: e.str("SMTP_PASSWORD", ""),
		From:     e.str("SMTP_FROM", ""),
		Security: e.str("SMTP_SECURITY", "starttls"),
	}
	cfg.TeamsWebhookHosts = e.csv("TEAMS_WEBHOOK_HOSTS")
	if len(cfg.TeamsWebhookHosts) == 0 {
		cfg.TeamsWebhookHosts = []string{"logic.azure.com", "powerplatform.com", "webhook.office.com"}
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
	cfg.EncryptionKeys = e.encryptionKeys()

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
	if l := c.Export.Language; l != "" && !languageTag.MatchString(l) {
		errs = append(errs, fmt.Errorf("DOCUMENT_LANGUAGE: %q is not a language tag such as en-GB or fr-FR", l))
	}
	if c.ConfluenceRateLimit < 0 || c.APIRateLimit < 0 || c.AuthRateLimit < 0 {
		errs = append(errs, errors.New("CONFLUENCE_RATE_LIMIT, API_RATE_LIMIT and AUTH_RATE_LIMIT must be >= 0 (0 disables the limit)"))
	}
	if c.ConfluenceRateBurst < 1 || c.APIRateBurst < 1 {
		errs = append(errs, errors.New("CONFLUENCE_RATE_BURST and API_RATE_BURST must be >= 1"))
	}
	if c.AccountRetention < 0 || (c.AccountRetention > 0 && (c.AccountDeletionNotice <= 0 || c.AccountDeletionNotice >= c.AccountRetention)) {
		errs = append(errs, errors.New("ACCOUNT_DELETION_NOTICE must be > 0 and shorter than ACCOUNT_RETENTION (0 keeps accounts forever)"))
	}
	if c.AuditRetention < minAuditRetention {
		errs = append(errs, fmt.Errorf("AUDIT_RETENTION must be at least %s", minAuditRetention))
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
	if c.SMTP.Enabled() {
		switch c.SMTP.Security {
		case "starttls", "tls", "none":
		default:
			errs = append(errs, fmt.Errorf("SMTP_SECURITY: invalid value %q (want starttls, tls or none)", c.SMTP.Security))
		}
		if c.SMTP.From == "" {
			errs = append(errs, errors.New("SMTP_FROM is required when SMTP_HOST is set"))
		}
		if c.SMTP.Port < 1 || c.SMTP.Port > 65535 {
			errs = append(errs, errors.New("SMTP_PORT must be a TCP port"))
		}
		if c.SMTP.Username != "" && c.SMTP.Security == "none" {
			errs = append(errs, errors.New("SMTP_USERNAME needs SMTP_SECURITY starttls or tls: credentials are not sent in clear"))
		}
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

// csv splits a comma-separated list, keeping inner spaces: group names such
// as "Confluence Export Admins" are legitimate.
func (e *envReader) csv(key string) []string {
	var out []string
	for _, item := range strings.Split(e.str(key, ""), ",") {
		if item = strings.TrimSpace(item); item != "" {
			out = append(out, item)
		}
	}
	return out
}

// classifications reads "Label[:watermark], ..."; "none" disables the choice.
func (e *envReader) classifications(key, def string) []domain.Classification {
	raw := e.str(key, def)
	if strings.EqualFold(raw, "none") {
		return nil
	}
	var out []domain.Classification
	seen := map[string]bool{}
	for _, item := range strings.Split(raw, ",") {
		item = strings.TrimSpace(item)
		if item == "" {
			continue
		}
		label, flag, hasFlag := strings.Cut(item, ":")
		label, flag = strings.TrimSpace(label), strings.TrimSpace(flag)
		switch {
		case label == "" || len(label) > maxClassificationLabel:
			e.errs = append(e.errs, fmt.Errorf("%s: label %q must be 1 to %d characters", key, label, maxClassificationLabel))
			continue
		case hasFlag && !strings.EqualFold(flag, "watermark"):
			e.errs = append(e.errs, fmt.Errorf("%s: unknown option %q for %q (only \"watermark\")", key, flag, label))
			continue
		case seen[strings.ToLower(label)]:
			e.errs = append(e.errs, fmt.Errorf("%s: %q is listed twice", key, label))
			continue
		}
		seen[strings.ToLower(label)] = true
		out = append(out, domain.Classification{Label: label, Watermark: hasFlag})
	}
	return out
}

// prefixes reads a list of CIDR ranges or single addresses.
func (e *envReader) prefixes(key string) []netip.Prefix {
	var out []netip.Prefix
	for _, item := range e.csv(key) {
		if !strings.Contains(item, "/") {
			addr, err := netip.ParseAddr(item)
			if err != nil {
				e.errs = append(e.errs, fmt.Errorf("%s: invalid address %q", key, item))
				continue
			}
			out = append(out, netip.PrefixFrom(addr, addr.BitLen()))
			continue
		}
		p, err := netip.ParsePrefix(item)
		if err != nil {
			e.errs = append(e.errs, fmt.Errorf("%s: invalid CIDR range %q", key, item))
			continue
		}
		out = append(out, p.Masked())
	}
	return out
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

// encryptionKeys reads ENCRYPTION_KEYS ("id:base64, id:base64", newest
// first) or, for a single key, ENCRYPTION_KEY.
func (e *envReader) encryptionKeys() []crypto.Key {
	ring, single := e.str("ENCRYPTION_KEYS", ""), e.str("ENCRYPTION_KEY", "")
	switch {
	case ring != "" && single != "":
		e.errs = append(e.errs, errors.New("set ENCRYPTION_KEYS or ENCRYPTION_KEY, not both: name the old key \"default\" in ENCRYPTION_KEYS"))
		return nil
	case ring == "":
		if k := e.base64Key("ENCRYPTION_KEY", 32); k != nil {
			return []crypto.Key{{ID: crypto.DefaultKeyID, Secret: k}}
		}
		return nil
	}
	var keys []crypto.Key
	for _, item := range e.csv("ENCRYPTION_KEYS") {
		id, encoded, ok := strings.Cut(item, ":")
		secret, err := base64.StdEncoding.DecodeString(strings.TrimSpace(encoded))
		if !ok || err != nil || len(secret) != 32 {
			e.errs = append(e.errs, fmt.Errorf("ENCRYPTION_KEYS: %q must be id:<32 random bytes, base64>", strings.TrimSpace(id)))
			continue
		}
		keys = append(keys, crypto.Key{ID: strings.TrimSpace(id), Secret: secret})
	}
	if len(e.errs) == 0 {
		// Ids, duplicates and reused secrets are checked where the ring is built.
		if _, err := crypto.NewKeyRing(keys); err != nil {
			e.errs = append(e.errs, fmt.Errorf("ENCRYPTION_KEYS: %w", err))
		}
	}
	return keys
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
