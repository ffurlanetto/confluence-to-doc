package httpapi_test

import (
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"
)

type auditPage struct {
	Events []struct {
		Action, Outcome, ActorEmail, TargetID, ClientIP string
		Details                                         map[string]any
	}
	NextCursor string
}

func (p auditPage) actions() []string {
	var out []string
	for _, e := range p.Events {
		out = append(out, e.Action+":"+e.Outcome)
	}
	return out
}

func TestMeReportsAdminRole(t *testing.T) {
	a := newAPIWithWorker(t, false)
	var me struct{ IsAdmin bool }
	a.do("GET", "/api/me", "alice", nil, &me)
	if me.IsAdmin {
		t.Fatal("alice is not an admin")
	}
	a.do("GET", "/api/me", "admin-ann", nil, &me)
	if !me.IsAdmin {
		t.Fatal("admin-ann is an admin")
	}
}

func TestAdminAPIRequiresAdmin(t *testing.T) {
	a := newAPIWithWorker(t, false)
	var e apiError
	if r := a.do("GET", "/api/admin/audit", "bob", nil, &e); r.StatusCode != http.StatusForbidden || e.Error.Code != "forbidden" {
		t.Fatalf("non-admin = %d %+v", r.StatusCode, e)
	}
	var page auditPage
	if r := a.do("GET", "/api/admin/audit?action=admin.access", "admin-ann", nil, &page); r.StatusCode != http.StatusOK {
		t.Fatalf("admin = %d", r.StatusCode)
	}
	if len(page.Events) != 1 || page.Events[0].Outcome != "denied" || page.Events[0].ActorEmail != "bob@example.com" {
		t.Fatalf("the refusal must be audited: %+v", page.Events)
	}
}

func TestAuditTrailOfAnExport(t *testing.T) {
	a := newAPI(t)
	a.do("PUT", "/api/preferences/pat", "alice", map[string]string{"token": "wrong"}, nil)
	a.do("PUT", "/api/preferences/pat", "alice", map[string]string{"token": "good-pat"}, nil)
	var created struct{ ID string }
	a.do("POST", "/api/exports", "alice", map[string]any{"pageId": "10", "format": "pdf"}, &created)
	var got struct{ Status string }
	for deadline := time.Now().Add(10 * time.Second); time.Now().Before(deadline) && got.Status != "succeeded"; {
		a.do("GET", "/api/exports/"+created.ID, "alice", nil, &got)
		time.Sleep(25 * time.Millisecond)
	}
	if got.Status != "succeeded" {
		t.Fatalf("export status %q", got.Status)
	}
	a.do("GET", "/api/exports/"+created.ID+"/download", "alice", nil, nil)

	var page auditPage
	a.do("GET", "/api/admin/audit?actor=alice", "admin-ann", nil, &page)
	want := "export.download:success export.create:success pat.set:success pat.set:failure"
	if got := strings.Join(page.actions(), " "); got != want {
		t.Fatalf("alice's trail = %q, want %q", got, want)
	}
	download := page.Events[0]
	if download.TargetID != created.ID || download.Details["title"] != "Guide utilisateur" || download.ClientIP != "127.0.0.1" {
		t.Errorf("download event: %+v", download)
	}
	if page.Events[3].Details["reason"] != "pat_invalid" {
		t.Errorf("failed PAT reason: %v", page.Events[3].Details)
	}

	// The worker records the completion on the owner's behalf.
	a.do("GET", "/api/admin/audit?action=export.complete", "admin-ann", nil, &page)
	if len(page.Events) != 1 || page.Events[0].TargetID != created.ID || page.Events[0].Details["pages"] != float64(2) {
		t.Fatalf("completion event: %+v", page.Events)
	}

	// Reading the trail is audited too.
	a.do("GET", "/api/admin/audit?action=admin.audit.read", "admin-ann", nil, &page)
	if len(page.Events) < 2 || page.Events[0].ActorEmail != "admin-ann@example.com" {
		t.Fatalf("audit reads not recorded: %+v", page.Events)
	}
}

func TestAuditPaginationAndValidation(t *testing.T) {
	a := newAPIWithWorker(t, false)
	for range 3 {
		a.do("GET", "/api/admin/audit", "bob", nil, nil) // three denied accesses
	}
	var page auditPage
	a.do("GET", "/api/admin/audit?action=admin.access&limit=2", "admin-ann", nil, &page)
	if len(page.Events) != 2 || page.NextCursor == "" {
		t.Fatalf("first page: %d events, cursor %q", len(page.Events), page.NextCursor)
	}
	cursor := page.NextCursor
	page = auditPage{}
	a.do("GET", "/api/admin/audit?action=admin.access&limit=2&before="+cursor, "admin-ann", nil, &page)
	if len(page.Events) != 1 || page.NextCursor != "" {
		t.Fatalf("last page: %d events, cursor %q", len(page.Events), page.NextCursor)
	}

	for _, q := range []string{"from=yesterday", "before=nope", "limit=0", "limit=1000", "actor=" + strings.Repeat("a", 201)} {
		var e apiError
		if r := a.do("GET", "/api/admin/audit?"+q, "admin-ann", nil, &e); r.StatusCode != http.StatusBadRequest || e.Error.Code != "invalid_filter" {
			t.Errorf("%s = %d %+v", q, r.StatusCode, e)
		}
	}
	from := url.QueryEscape(time.Now().Add(time.Hour).Format(time.RFC3339))
	a.do("GET", "/api/admin/audit?from="+from, "admin-ann", nil, &page)
	if len(page.Events) != 0 {
		t.Fatalf("future window returned %d events", len(page.Events))
	}
}
