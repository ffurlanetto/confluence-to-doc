package httpapi_test

import (
	"net/http"
	"testing"
)

func TestDeleteMyAccount(t *testing.T) {
	a := newAPIWithWorker(t, false)
	a.do("PUT", "/api/preferences/pat", "alice", map[string]string{"token": "good-pat"}, nil)
	a.do("POST", "/api/exports", "alice", map[string]any{"pageId": "10", "format": "pdf"}, nil)

	if r := a.do("DELETE", "/api/me", "alice", nil, nil); r.StatusCode != http.StatusNoContent {
		t.Fatalf("delete = %d", r.StatusCode)
	}
	// The next request is a brand-new account: no token, no export.
	var prefs struct{ HasPat bool }
	a.do("GET", "/api/preferences", "alice", nil, &prefs)
	var list struct{ Exports []struct{ ID string } }
	a.do("GET", "/api/exports", "alice", nil, &list)
	if prefs.HasPat || len(list.Exports) != 0 {
		t.Fatalf("data left after deletion: pat=%v exports=%d", prefs.HasPat, len(list.Exports))
	}
	var page auditPage
	a.do("GET", "/api/admin/audit?action=account.delete", "admin-ann", nil, &page)
	if len(page.Events) != 1 || page.Events[0].ActorEmail != "alice@example.com" {
		t.Fatalf("deletion not audited: %+v", page.Events)
	}
}
