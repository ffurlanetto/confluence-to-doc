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
