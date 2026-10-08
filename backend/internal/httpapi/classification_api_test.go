package httpapi_test

import (
	"net/http"
	"testing"
)

func TestClassifications(t *testing.T) {
	a := newAPIWithWorker(t, false)
	var prefs struct {
		Classifications []struct {
			Label     string
			Watermark bool
		}
		DefaultClassification string
	}
	a.do("GET", "/api/preferences", "alice", nil, &prefs)
	if len(prefs.Classifications) != 2 || !prefs.Classifications[1].Watermark || prefs.DefaultClassification != "Internal" {
		t.Fatalf("preferences: %+v", prefs)
	}

	a.do("PUT", "/api/preferences/pat", "alice", map[string]string{"token": "good-pat"}, nil)
	var created struct{ ID, Classification string }
	r := a.do("POST", "/api/exports", "alice", map[string]any{"pageId": "10", "format": "pdf", "classification": " confidential "}, &created)
	if r.StatusCode != http.StatusAccepted || created.Classification != "Confidential" {
		t.Fatalf("create: %d %+v", r.StatusCode, created)
	}
	var none struct{ Classification string }
	a.do("POST", "/api/exports", "alice", map[string]any{"pageId": "10", "format": "pdf"}, &none)
	if none.Classification != "" {
		t.Errorf("no choice must stay empty (the default applies at generation): %q", none.Classification)
	}

	var e apiError
	if r := a.do("POST", "/api/exports", "alice", map[string]any{"pageId": "10", "format": "pdf", "classification": "Top secret"}, &e); r.StatusCode != http.StatusBadRequest || e.Error.Code != "invalid_classification" {
		t.Fatalf("unknown classification = %d %+v", r.StatusCode, e)
	}

	var page auditPage
	a.do("GET", "/api/admin/audit?action=export.create", "admin-ann", nil, &page)
	if len(page.Events) != 2 || page.Events[1].Details["classification"] != "Confidential" {
		t.Fatalf("classification not audited: %+v", page.Events)
	}
}
