package store_test

import (
	"bytes"
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/ffurlanetto/confluence-to-doc/backend/internal/audit"
	"github.com/ffurlanetto/confluence-to-doc/backend/internal/domain"
	"github.com/ffurlanetto/confluence-to-doc/backend/internal/testutil"
)

func TestAdminQueueCancelAndRetry(t *testing.T) {
	s := testutil.NewStore(t)
	ctx := context.Background()
	ann, _ := s.UpsertUser(ctx, "iss", "ann", "ann@example.com", "Ann")
	bob, _ := s.UpsertUser(ctx, "iss", "bob", "bob@example.com", "Bob")
	queued, running := newExport(ann.ID), newExport(bob.ID)
	for _, e := range []*domain.Export{queued, running} {
		if err := s.CreateExport(ctx, e, 5); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.ClaimNext(ctx, "w1", time.Minute); err != nil { // takes the oldest: queued
		t.Fatal(err)
	}

	all, err := s.ListAllExports(ctx, nil, 10)
	if err != nil || len(all) != 2 {
		t.Fatalf("ListAllExports = %d, %v", len(all), err)
	}
	if all[0].ID != running.ID || all[0].OwnerEmail != "bob@example.com" {
		t.Fatalf("newest first with its owner, got %+v", all[0])
	}
	active, _ := s.ListAllExports(ctx, []domain.ExportStatus{domain.StatusRunning}, 10)
	if len(active) != 1 || active[0].ID != queued.ID {
		t.Fatalf("status filter = %+v", active)
	}

	cancelled, err := s.CancelExport(ctx, queued.ID, "Cancelled.", time.Hour)
	if err != nil || cancelled.Status != domain.StatusFailed || cancelled.Error != "Cancelled." || cancelled.ExpiresAt == nil {
		t.Fatalf("CancelExport = %+v, %v", cancelled, err)
	}
	// The worker that held it notices at its next heartbeat.
	if err := s.Heartbeat(ctx, queued.ID, "w1", time.Minute, 1, 2); err == nil {
		t.Fatal("the worker kept its lease on a cancelled export")
	}
	if _, err := s.CancelExport(ctx, queued.ID, "again", time.Hour); !errors.Is(err, domain.ErrExportState) {
		t.Fatalf("cancelling a finished export: %v, want ErrExportState", err)
	}
	if _, err := s.CancelExport(ctx, uuid.New(), "x", time.Hour); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("cancelling a missing export: %v, want ErrNotFound", err)
	}

	retried, err := s.RetryExport(ctx, queued.ID)
	if err != nil || retried.Status != domain.StatusQueued || retried.Attempts != 0 || retried.Error != "" || retried.ExpiresAt != nil {
		t.Fatalf("RetryExport = %+v, %v", retried, err)
	}
	if _, err := s.RetryExport(ctx, running.ID); !errors.Is(err, domain.ErrExportState) {
		t.Fatalf("retrying a queued export: %v, want ErrExportState", err)
	}
}

func TestBlockUser(t *testing.T) {
	s := testutil.NewStore(t)
	ctx := context.Background()
	u, _ := s.UpsertUser(ctx, "iss", "sub", "eve@example.com", "Eve")
	hash := []byte("0123456789abcdef0123456789abcdef")
	if err := s.CreateSession(ctx, hash, domain.Session{UserID: u.ID, ExpiresAt: time.Now().Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	e := newExport(u.ID)
	if err := s.CreateExport(ctx, e, 5); err != nil {
		t.Fatal(err)
	}

	blocked, cancelled, err := s.BlockUser(ctx, u.ID, "Leaked documents", "Blocked.", time.Hour)
	if err != nil || !blocked.Blocked() || blocked.BlockedReason != "Leaked documents" || cancelled != 1 {
		t.Fatalf("BlockUser = %+v, %d, %v", blocked, cancelled, err)
	}
	if _, _, err := s.SessionUser(ctx, hash); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("session of a blocked user: %v, want ErrNotFound", err)
	}
	// A new session (opened by a stale flow) does not resolve either.
	_ = s.CreateSession(ctx, hash, domain.Session{UserID: u.ID, ExpiresAt: time.Now().Add(time.Hour)})
	if _, _, err := s.SessionUser(ctx, hash); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("new session of a blocked user: %v, want ErrNotFound", err)
	}
	if got, _ := s.GetExport(ctx, e.ID, u.ID); got.Status != domain.StatusFailed || got.Error != "Blocked." {
		t.Fatalf("pending export = %+v, want cancelled", got)
	}
	if again, _ := s.UpsertUser(ctx, "iss", "sub", "eve@example.com", "Eve"); !again.Blocked() {
		t.Fatal("UpsertUser must report the block, which sign-in checks")
	}

	list, err := s.ListUsers(ctx, "EVE", 10)
	if err != nil || len(list) != 1 || !list[0].Blocked() || list[0].TotalExports != 1 || list[0].ActiveExports != 0 {
		t.Fatalf("ListUsers = %+v, %v", list, err)
	}
	if none, _ := s.ListUsers(ctx, "%", 10); len(none) != 0 {
		t.Fatalf("the search must escape LIKE wildcards, got %d users", len(none))
	}

	if u2, err := s.UnblockUser(ctx, u.ID); err != nil || u2.Blocked() || u2.BlockedReason != "" {
		t.Fatalf("UnblockUser = %+v, %v", u2, err)
	}
	if _, _, err := s.SessionUser(ctx, hash); err != nil {
		t.Fatalf("session after unblocking: %v", err)
	}
	if _, _, err := s.BlockUser(ctx, uuid.New(), "x", "x", time.Hour); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("blocking a missing user: %v", err)
	}
}

func TestUsageFromAuditTrail(t *testing.T) {
	s := testutil.NewStore(t)
	ctx := context.Background()
	ann, _ := s.UpsertUser(ctx, "iss", "ann", "ann@example.com", "Ann")
	bob, _ := s.UpsertUser(ctx, "iss", "bob", "bob@example.com", "Bob")
	now := time.Now().UTC()
	record := func(actor uuid.UUID, action string, at time.Time, details map[string]any) {
		t.Helper()
		if err := s.InsertAuditEvent(ctx, domain.AuditEvent{
			ID: uuid.Must(uuid.NewV7()), OccurredAt: at, ActorID: &actor, Action: action, Outcome: domain.AuditSuccess,
			Details: details,
		}); err != nil {
			t.Fatal(err)
		}
	}
	record(ann.ID, audit.ActionExportComplete, now, map[string]any{"pages": 10, "size": 1000, "format": "pdf"})
	record(ann.ID, audit.ActionExportComplete, now.Add(-24*time.Hour), map[string]any{"pages": 5, "size": 500, "format": "docx"})
	record(bob.ID, audit.ActionExportFail, now, map[string]any{"format": "pdf"})
	record(bob.ID, audit.ActionExportComplete, now.Add(-40*24*time.Hour), map[string]any{"pages": 99, "size": 9, "format": "pdf"})
	record(bob.ID, audit.ActionExportDownload, now, map[string]any{})

	u, err := s.Usage(ctx, now.Add(-7*24*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if u.Succeeded != 2 || u.Failed != 1 || u.Pages != 15 || u.Bytes != 1500 || u.Users != 2 {
		t.Fatalf("totals = %+v", u)
	}
	if u.ByFormat[domain.FormatPDF] != 1 || u.ByFormat[domain.FormatDOCX] != 1 {
		t.Fatalf("by format = %v", u.ByFormat)
	}
	if len(u.Daily) != 2 || u.Daily[1].Succeeded != 1 || u.Daily[1].Failed != 1 {
		t.Fatalf("daily = %+v", u.Daily)
	}
	if len(u.TopUsers) != 1 || u.TopUsers[0].Email != "ann@example.com" || u.TopUsers[0].Exports != 2 || u.TopUsers[0].Pages != 15 {
		t.Fatalf("top users = %+v", u.TopUsers)
	}
}

func TestDocumentTemplateStorage(t *testing.T) {
	s := testutil.NewStore(t)
	ctx := context.Background()
	u, _ := s.UpsertUser(ctx, "iss", "adm", "adm@example.com", "Admin")
	if _, err := s.DocumentTemplateVersion(ctx); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("no template: %v", err)
	}
	if err := s.DeleteDocumentTemplate(ctx); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("deleting none: %v", err)
	}
	first, err := s.PutDocumentTemplate(ctx, "a.dotx", []byte("one"), u.ID)
	if err != nil || first.Name != "a.dotx" || first.UploadedByEmail != "adm@example.com" || len(first.Content) != 0 {
		t.Fatalf("Put = %+v, %v", first, err)
	}
	if _, err := s.PutDocumentTemplate(ctx, "b.dotx", []byte("two"), u.ID); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetDocumentTemplate(ctx, true)
	if err != nil || got.Name != "b.dotx" || string(got.Content) != "two" {
		t.Fatalf("Get = %+v, %v", got, err)
	}
	v, _ := s.DocumentTemplateVersion(ctx)
	if !bytes.Equal(v, got.SHA256) || bytes.Equal(v, first.SHA256) {
		t.Fatal("the version must follow the content")
	}
	if err := s.DeleteDocumentTemplate(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetDocumentTemplate(ctx, false); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("after delete: %v", err)
	}
}
