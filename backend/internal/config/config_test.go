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
	if len(cfg.EncryptionKey) != 32 {
		t.Errorf("key length = %d", len(cfg.EncryptionKey))
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
