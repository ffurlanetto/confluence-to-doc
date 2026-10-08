package httpapi_test

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/ffurlanetto/confluence-to-doc/backend/internal/domain"
)

func TestNotificationSettings(t *testing.T) {
	a := newAPIWithWorker(t, false)
	var prefs struct {
		Email                                                       string
		EmailAvailable, NotifyEmail, NotifyExports, HasTeamsWebhook bool
	}
	a.do("GET", "/api/preferences", "alice", nil, &prefs)
	if prefs.Email != "alice@example.com" || prefs.EmailAvailable || !prefs.NotifyEmail || !prefs.NotifyExports || prefs.HasTeamsWebhook {
		t.Fatalf("defaults: %+v", prefs)
	}

	a.do("PUT", "/api/preferences/notifications", "alice", map[string]bool{"email": false, "exports": true}, &prefs)
	if prefs.NotifyEmail || !prefs.NotifyExports {
		t.Fatalf("after update: %+v", prefs)
	}

	var e apiError
	if r := a.do("PUT", "/api/preferences/teams", "alice", map[string]string{"url": "https://169.254.169.254/latest"}, &e); r.StatusCode != http.StatusBadRequest || e.Error.Code != "invalid_teams_url" {
		t.Fatalf("metadata URL accepted: %d %+v", r.StatusCode, e)
	}
	if r := a.do("POST", "/api/preferences/teams/test", "alice", nil, &e); r.StatusCode != http.StatusConflict || e.Error.Code != "teams_missing" {
		t.Fatalf("test without URL: %d %+v", r.StatusCode, e)
	}
	const url = "https://prod-01.westeurope.logic.azure.com/workflows/abc/triggers/manual/paths/invoke?sig=s3cret"
	r := a.do("PUT", "/api/preferences/teams", "alice", map[string]string{"url": url}, &prefs)
	if r.StatusCode != http.StatusOK || !prefs.HasTeamsWebhook {
		t.Fatalf("set teams: %d %+v", r.StatusCode, prefs)
	}
	raw := a.do("GET", "/api/preferences", "alice", nil, nil)
	if body := readBody(raw); strings.Contains(body, "s3cret") || strings.Contains(body, "logic.azure.com") {
		t.Fatal("the Teams URL must never be returned")
	}
	var page auditPage
	a.do("GET", "/api/admin/audit?action=notifications.teams_set", "admin-ann", nil, &page)
	if len(page.Events) != 1 || strings.Contains(strings.Join(eventDetails(page), " "), "s3cret") {
		t.Fatalf("audit: %+v", page.Events)
	}
	if r := a.do("DELETE", "/api/preferences/teams", "alice", nil, nil); r.StatusCode != http.StatusNoContent {
		t.Fatalf("delete teams: %d", r.StatusCode)
	}
}

func TestNotificationInbox(t *testing.T) {
	a := newAPIWithWorker(t, false)
	a.do("GET", "/api/me", "bob", nil, nil) // creates bob
	ctx := context.Background()
	var bobID uuid.UUID
	if err := a.store.Pool().QueryRow(ctx, `SELECT id FROM users WHERE subject = 'bob'`).Scan(&bobID); err != nil {
		t.Fatal(err)
	}
	for _, title := range []string{"first", "second"} {
		n := domain.Notification{ID: uuid.Must(uuid.NewV7()), UserID: bobID, Kind: domain.NotifyExportSucceeded, Title: title, Body: "b"}
		if err := a.store.CreateNotification(ctx, n, nil); err != nil {
			t.Fatal(err)
		}
	}
	var inbox struct {
		Notifications []struct{ Title string }
		Unread        int
	}
	a.do("GET", "/api/notifications", "bob", nil, &inbox)
	if inbox.Unread != 2 || len(inbox.Notifications) != 2 || inbox.Notifications[0].Title != "second" {
		t.Fatalf("inbox: %+v", inbox)
	}
	a.do("GET", "/api/notifications", "carol", nil, &inbox)
	if len(inbox.Notifications) != 0 {
		t.Fatal("notifications are per user")
	}
	if r := a.do("POST", "/api/notifications/read", "bob", nil, nil); r.StatusCode != http.StatusNoContent {
		t.Fatalf("read: %d", r.StatusCode)
	}
	a.do("GET", "/api/notifications", "bob", nil, &inbox)
	if inbox.Unread != 0 {
		t.Fatal("all read")
	}
}
