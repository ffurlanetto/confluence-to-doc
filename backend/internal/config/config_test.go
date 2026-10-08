package config

import (
	"strings"
	"testing"
	"time"
)

func baseEnv() map[string]string {
	return map[string]string{
		"DATABASE_URL":        "postgres://localhost/db",
		"OIDC_ISSUER_URL":     "https://idp.example.com",
		"OIDC_CLIENT_ID":      "client",
		"OIDC_CLIENT_SECRET":  "secret",
		"CONFLUENCE_BASE_URL": "https://confluence.example.com/",
		"ENCRYPTION_KEY":      "MDEyMzQ1Njc4OWFiY2RlZjAxMjM0NTY3ODlhYmNkZWY=",
	}
}

func getter(m map[string]string) func(string) string {
	return func(k string) string { return m[k] }
}

func TestLoadDefaults(t *testing.T) {
	cfg, err := load(getter(baseEnv()))
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if cfg.Export.Retention != 48*time.Hour {
		t.Errorf("retention = %v, want 48h", cfg.Export.Retention)
	}
	if got := cfg.ConfluenceBaseURL.String(); got != "https://confluence.example.com" {
		t.Errorf("confluence URL = %q, want trailing slash trimmed", got)
	}
	if cfg.Role != RoleAll || !cfg.Role.RunsAPI() || !cfg.Role.RunsWorker() {
		t.Errorf("unexpected role %q", cfg.Role)
	}
	if cfg.SecureCookies() {
		t.Error("default public URL is http, cookies should not be Secure")
	}
	if len(cfg.EncryptionKeys) != 1 || cfg.EncryptionKeys[0].ID != "default" || len(cfg.EncryptionKeys[0].Secret) != 32 {
		t.Errorf("encryption keys = %+v", cfg.EncryptionKeys)
	}
}

func TestLoadReportsAllErrors(t *testing.T) {
	env := baseEnv()
	delete(env, "DATABASE_URL")
	env["ENCRYPTION_KEY"] = "short"
	env["EXPORT_MAX_PAGES"] = "abc"
	env["APP_ROLE"] = "banana"

	_, err := load(getter(env))
	if err == nil {
		t.Fatal("expected an error")
	}
	for _, want := range []string{"DATABASE_URL", "ENCRYPTION_KEY", "EXPORT_MAX_PAGES"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not mention %s", err, want)
		}
	}
}

func TestLoadRejectsInvalidRole(t *testing.T) {
	env := baseEnv()
	env["APP_ROLE"] = "banana"
	if _, err := load(getter(env)); err == nil || !strings.Contains(err.Error(), "APP_ROLE") {
		t.Fatalf("expected APP_ROLE error, got %v", err)
	}
}

func TestLoadRejectsRelativeURL(t *testing.T) {
	env := baseEnv()
	env["CONFLUENCE_BASE_URL"] = "confluence.local"
	if _, err := load(getter(env)); err == nil || !strings.Contains(err.Error(), "CONFLUENCE_BASE_URL") {
		t.Fatalf("expected URL error, got %v", err)
	}
}

func TestS3DisabledByDefault(t *testing.T) {
	cfg, err := load(getter(baseEnv()))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.S3.Enabled() {
		t.Fatal("S3 must be disabled when S3_BUCKET is not set")
	}
}

func TestS3Config(t *testing.T) {
	env := baseEnv()
	env["S3_BUCKET"] = "exports"
	env["S3_ENDPOINT"] = "http://minio:9000/"
	cfg, err := load(getter(env))
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.S3.Enabled() || cfg.S3.Endpoint != "http://minio:9000" || !cfg.S3.UsePathStyle || cfg.S3.Prefix != "exports/" {
		t.Fatalf("unexpected S3 config %+v", cfg.S3)
	}

	env["S3_FORCE_PATH_STYLE"] = "false"
	cfg, _ = load(getter(env))
	if cfg.S3.UsePathStyle {
		t.Error("S3_FORCE_PATH_STYLE=false must win over the endpoint default")
	}
}

func TestS3ConfigValidation(t *testing.T) {
	cases := map[string]map[string]string{
		"S3_ACCESS_KEY_ID":    {"S3_ACCESS_KEY_ID": "key"}, // secret missing
		"S3_ENDPOINT":         {"S3_ENDPOINT": "minio:9000"},
		"S3_FORCE_PATH_STYLE": {"S3_FORCE_PATH_STYLE": "maybe"},
	}
	for want, extra := range cases {
		env := baseEnv()
		env["S3_BUCKET"] = "exports"
		for k, v := range extra {
			env[k] = v
		}
		if _, err := load(getter(env)); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%v: want error mentioning %s, got %v", extra, want, err)
		}
	}
}

func TestTelemetryDisabledByDefault(t *testing.T) {
	cfg, err := load(getter(baseEnv()))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Telemetry.Enabled() {
		t.Fatal("telemetry must be off without an OTLP endpoint")
	}
	if cfg.Telemetry.ServiceName != "confluence-to-doc" || cfg.Telemetry.SampleRatio != 1 {
		t.Fatalf("unexpected telemetry defaults: %+v", cfg.Telemetry)
	}
}

func TestTelemetryConfig(t *testing.T) {
	env := baseEnv()
	env["OTEL_EXPORTER_OTLP_ENDPOINT"] = "http://collector:4317/"
	env["OTEL_EXPORTER_OTLP_PROTOCOL"] = "http/protobuf"
	env["OTEL_SERVICE_NAME"] = "c2d-prod"
	env["OTEL_TRACES_SAMPLER_ARG"] = "0.25"
	env["OTEL_METRIC_EXPORT_INTERVAL"] = "15s"

	cfg, err := load(getter(env))
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.Telemetry.Enabled() || cfg.Telemetry.Endpoint != "http://collector:4317" {
		t.Fatalf("endpoint = %q", cfg.Telemetry.Endpoint)
	}
	if cfg.Telemetry.Protocol != "http/protobuf" || cfg.Telemetry.ServiceName != "c2d-prod" {
		t.Fatalf("unexpected telemetry config: %+v", cfg.Telemetry)
	}
	if cfg.Telemetry.SampleRatio != 0.25 || cfg.Telemetry.MetricInterval != 15*time.Second {
		t.Fatalf("unexpected telemetry config: %+v", cfg.Telemetry)
	}
}

func TestTelemetryConfigValidation(t *testing.T) {
	cases := map[string]map[string]string{
		"OTEL_EXPORTER_OTLP_PROTOCOL": {"OTEL_EXPORTER_OTLP_PROTOCOL": "carrier-pigeon"},
		"OTEL_EXPORTER_OTLP_ENDPOINT": {"OTEL_EXPORTER_OTLP_ENDPOINT": "collector:4317"},
		"OTEL_TRACES_SAMPLER_ARG":     {"OTEL_TRACES_SAMPLER_ARG": "42"},
	}
	for want, extra := range cases {
		env := baseEnv()
		env["OTEL_EXPORTER_OTLP_ENDPOINT"] = "http://collector:4317"
		for k, v := range extra {
			env[k] = v
		}
		if _, err := load(getter(env)); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%v: want an error mentioning %s, got %v", extra, want, err)
		}
	}
}

func TestMetricPrefixNormalisation(t *testing.T) {
	cases := map[string]string{
		"":       "",
		"acme":   "acme.", // a separator is added when the operator omits it
		"acme.":  "acme.", // an explicit separator is kept as written
		"acme_":  "acme_", // including the Prometheus-style underscore
		"acme-":  "acme-",
		" acme ": "acme.", // surrounding blanks are ignored
	}
	for raw, want := range cases {
		env := baseEnv()
		env["TELEMETRY_METRIC_PREFIX"] = raw
		cfg, err := load(getter(env))
		if err != nil {
			t.Fatalf("prefix %q: %v", raw, err)
		}
		if cfg.Telemetry.MetricPrefix != want {
			t.Errorf("prefix %q normalised to %q, want %q", raw, cfg.Telemetry.MetricPrefix, want)
		}
	}
}

func TestMetricPrefixValidation(t *testing.T) {
	for _, bad := range []string{"1acme", "acme corp", "acme/corp", "acme$"} {
		env := baseEnv()
		env["TELEMETRY_METRIC_PREFIX"] = bad
		if _, err := load(getter(env)); err == nil || !strings.Contains(err.Error(), "TELEMETRY_METRIC_PREFIX") {
			t.Errorf("prefix %q: want a validation error, got %v", bad, err)
		}
	}
}

// The prefix is independent of the OTLP endpoint: it is validated even when
// telemetry is off, so a typo surfaces before the collector is wired up.
func TestMetricPrefixValidatedWithTelemetryDisabled(t *testing.T) {
	env := baseEnv()
	env["TELEMETRY_METRIC_PREFIX"] = "bad prefix"
	if _, err := load(getter(env)); err == nil {
		t.Fatal("expected a validation error with telemetry disabled")
	}
}

func TestAccessAndAuditDefaults(t *testing.T) {
	cfg, err := load(getter(baseEnv()))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.OIDC.GroupsClaim != "groups" || len(cfg.OIDC.AdminGroups) != 0 {
		t.Errorf("groups claim %q, admin groups %v", cfg.OIDC.GroupsClaim, cfg.OIDC.AdminGroups)
	}
	if cfg.AuditRetention != 365*24*time.Hour {
		t.Errorf("audit retention = %v, want one year", cfg.AuditRetention)
	}
	if len(cfg.TrustedProxies) != 0 {
		t.Errorf("no proxy must be trusted by default, got %v", cfg.TrustedProxies)
	}
}

func TestAccessAndAuditConfig(t *testing.T) {
	env := baseEnv()
	env["OIDC_GROUPS_CLAIM"] = "realm_access.roles"
	env["OIDC_ADMIN_GROUPS"] = " Confluence Export Admins , 0b5c6f1e-guid ,"
	env["AUDIT_RETENTION"] = "2160h"
	env["TRUSTED_PROXIES"] = "10.0.0.0/8, 192.168.1.10, fd00::/8"
	cfg, err := load(getter(env))
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(cfg.OIDC.AdminGroups, "|"); got != "Confluence Export Admins|0b5c6f1e-guid" {
		t.Errorf("admin groups = %q", got)
	}
	if cfg.OIDC.GroupsClaim != "realm_access.roles" || cfg.AuditRetention != 90*24*time.Hour {
		t.Errorf("unexpected %q %v", cfg.OIDC.GroupsClaim, cfg.AuditRetention)
	}
	var got []string
	for _, p := range cfg.TrustedProxies {
		got = append(got, p.String())
	}
	if strings.Join(got, " ") != "10.0.0.0/8 192.168.1.10/32 fd00::/8" {
		t.Errorf("trusted proxies = %v", got)
	}
}

func TestAccessAndAuditValidation(t *testing.T) {
	cases := map[string]map[string]string{
		"AUDIT_RETENTION": {"AUDIT_RETENTION": "24h"},
		"TRUSTED_PROXIES": {"TRUSTED_PROXIES": "10.0.0.0/33"},
	}
	for want, extra := range cases {
		env := baseEnv()
		for k, v := range extra {
			env[k] = v
		}
		if _, err := load(getter(env)); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%v: want error mentioning %s, got %v", extra, want, err)
		}
	}
}

func TestClassifications(t *testing.T) {
	cfg, err := load(getter(baseEnv()))
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, c := range cfg.Export.Classifications {
		label := c.Label
		if c.Watermark {
			label += "*"
		}
		got = append(got, label)
	}
	if strings.Join(got, ",") != "Public,Internal,Confidential*,Restricted*" {
		t.Errorf("default classifications = %v", got)
	}

	env := baseEnv()
	env["DOCUMENT_CLASSIFICATIONS"] = " C0 – Public , C3 – Secret : WATERMARK "
	cfg, err = load(getter(env))
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Export.Classifications) != 2 || cfg.Export.Classifications[1].Label != "C3 – Secret" || !cfg.Export.Classifications[1].Watermark {
		t.Errorf("custom classifications = %+v", cfg.Export.Classifications)
	}

	env["DOCUMENT_CLASSIFICATIONS"] = "none"
	if cfg, _ = load(getter(env)); len(cfg.Export.Classifications) != 0 {
		t.Errorf("none must disable the choice: %+v", cfg.Export.Classifications)
	}

	for _, bad := range []string{"Internal, internal", "Secret:stamp", ":watermark", strings.Repeat("x", 41)} {
		env["DOCUMENT_CLASSIFICATIONS"] = bad
		if _, err := load(getter(env)); err == nil || !strings.Contains(err.Error(), "DOCUMENT_CLASSIFICATIONS") {
			t.Errorf("%q: want an error, got %v", bad, err)
		}
	}
}

func TestEncryptionKeyRing(t *testing.T) {
	const k1, k2 = "MDEyMzQ1Njc4OWFiY2RlZjAxMjM0NTY3ODlhYmNkZWY=", "YWJjZGVmMDEyMzQ1Njc4OWFiY2RlZjAxMjM0NTY3ODk="
	env := baseEnv()
	delete(env, "ENCRYPTION_KEY")
	env["ENCRYPTION_KEYS"] = "2026-10:" + k2 + ", default:" + k1
	cfg, err := load(getter(env))
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.EncryptionKeys) != 2 || cfg.EncryptionKeys[0].ID != "2026-10" || cfg.EncryptionKeys[1].ID != "default" {
		t.Fatalf("key ring = %+v", cfg.EncryptionKeys)
	}

	for name, value := range map[string]string{
		"missing id":    k1,
		"bad base64":    "a:not-base64",
		"short key":     "a:c2hvcnQ=",
		"duplicate id":  "a:" + k1 + ",a:" + k2,
		"reused secret": "a:" + k1 + ",b:" + k1,
		"invalid id":    "not valid:" + k1,
	} {
		env["ENCRYPTION_KEYS"] = value
		if _, err := load(getter(env)); err == nil || !strings.Contains(err.Error(), "ENCRYPTION_KEYS") {
			t.Errorf("%s: want an ENCRYPTION_KEYS error, got %v", name, err)
		}
	}

	env["ENCRYPTION_KEYS"] = "default:" + k1
	env["ENCRYPTION_KEY"] = k1
	if _, err := load(getter(env)); err == nil || !strings.Contains(err.Error(), "not both") {
		t.Errorf("both variables set: want an error, got %v", err)
	}
}

func TestRateLimits(t *testing.T) {
	cfg, err := load(getter(baseEnv()))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ConfluenceRateLimit != 10 || cfg.ConfluenceRateBurst != 20 || cfg.APIRateLimit != 300 || cfg.APIRateBurst != 60 || cfg.AuthRateLimit != 30 {
		t.Errorf("defaults: %v %v %v %v %v", cfg.ConfluenceRateLimit, cfg.ConfluenceRateBurst, cfg.APIRateLimit, cfg.APIRateBurst, cfg.AuthRateLimit)
	}
	env := baseEnv()
	env["CONFLUENCE_RATE_LIMIT"] = "0"
	if cfg, err = load(getter(env)); err != nil || cfg.ConfluenceRateLimit != 0 {
		t.Errorf("0 disables the budget: %v %v", cfg, err)
	}
	for k, v := range map[string]string{"CONFLUENCE_RATE_LIMIT": "-1", "API_RATE_BURST": "0", "AUTH_RATE_LIMIT": "-5"} {
		env := baseEnv()
		env[k] = v
		if _, err := load(getter(env)); err == nil {
			t.Errorf("%s=%s: want an error", k, v)
		}
	}
}
