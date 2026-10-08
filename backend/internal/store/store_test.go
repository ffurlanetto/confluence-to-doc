package store_test

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/ffurlanetto/confluence-to-doc/backend/internal/domain"
	"github.com/ffurlanetto/confluence-to-doc/backend/internal/store"
	"github.com/ffurlanetto/confluence-to-doc/backend/internal/testutil"
)

func newExport(userID uuid.UUID) *domain.Export {
	return &domain.Export{
		ID: uuid.Must(uuid.NewV7()), UserID: userID, RootPageID: "1", RootTitle: "Root",
		Format: domain.FormatPDF, IncludeChildren: true, MaxAttempts: 2,
	}
}

func TestUsersSessionsPreferences(t *testing.T) {
	s := testutil.NewStore(t)
	ctx := context.Background()

	u, err := s.UpsertUser(ctx, "iss", "sub", "a@b.c", "Alice")
	if err != nil {
		t.Fatal(err)
	}
	u2, err := s.UpsertUser(ctx, "iss", "sub", "new@b.c", "Alice B")
	if err != nil || u2.ID != u.ID || u2.Email != "new@b.c" {
		t.Fatalf("upsert must update in place: %+v %v", u2, err)
	}

	hash := []byte("0123456789abcdef0123456789abcdef")
	if err := s.CreateSession(ctx, hash, u.ID, time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	got, _, err := s.SessionUser(ctx, hash)
	if err != nil || got.ID != u.ID {
		t.Fatalf("session lookup: %+v %v", got, err)
	}
	expired := []byte("expired-expired-expired-expired!")
	_ = s.CreateSession(ctx, expired, u.ID, time.Now().Add(-time.Minute))
	if _, _, err := s.SessionUser(ctx, expired); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("expired session must not resolve, got %v", err)
	}
	if n, err := s.DeleteExpiredSessions(ctx); err != nil || n != 1 {
		t.Fatalf("purge: %d %v", n, err)
	}

	p, err := s.GetPreferences(ctx, u.ID)
	if err != nil || p.HasPAT() || p.DefaultFormat != domain.FormatPDF {
		t.Fatalf("defaults: %+v %v", p, err)
	}
	if err := s.SetPAT(ctx, u.ID, []byte("cipher")); err != nil {
		t.Fatal(err)
	}
	if err := s.SetDefaultFormat(ctx, u.ID, domain.FormatDOCX); err != nil {
		t.Fatal(err)
	}
	p, _ = s.GetPreferences(ctx, u.ID)
	if !p.HasPAT() || p.PATUpdatedAt == nil || p.DefaultFormat != domain.FormatDOCX {
		t.Fatalf("after update: %+v", p)
	}
	if err := s.SetPAT(ctx, u.ID, nil); err != nil {
		t.Fatal(err)
	}
	p, _ = s.GetPreferences(ctx, u.ID)
	if p.HasPAT() || p.PATUpdatedAt != nil {
		t.Fatalf("PAT should be cleared: %+v", p)
	}
}

func TestExportQueueLifecycle(t *testing.T) {
	s := testutil.NewStore(t)
	ctx := context.Background()
	u, _ := s.UpsertUser(ctx, "iss", "queue-user", "", "")
	other, _ := s.UpsertUser(ctx, "iss", "other-user", "", "")

	e := newExport(u.ID)
	if err := s.CreateExport(ctx, e, 2); err != nil {
		t.Fatal(err)
	}
	if e.Status != domain.StatusQueued {
		t.Fatalf("status = %s", e.Status)
	}
	if err := s.CreateExport(ctx, newExport(u.ID), 2); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateExport(ctx, newExport(u.ID), 2); !errors.Is(err, domain.ErrTooManyActive) {
		t.Fatalf("want ErrTooManyActive, got %v", err)
	}

	// Ownership is enforced.
	if _, err := s.GetExport(ctx, e.ID, other.ID); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("other user must not see export, got %v", err)
	}

	// FIFO claim.
	job, err := s.ClaimNext(ctx, "w1", time.Minute)
	if err != nil || job.ID != e.ID || job.Attempts != 1 || job.Status != domain.StatusRunning {
		t.Fatalf("claim: %+v %v", job, err)
	}
	if err := s.Heartbeat(ctx, job.ID, "w2", time.Minute, 1, 2); !errors.Is(err, store.ErrLeaseLost) {
		t.Fatalf("foreign worker heartbeat must fail, got %v", err)
	}
	if err := s.Heartbeat(ctx, job.ID, "w1", time.Minute, 1, 2); err != nil {
		t.Fatal(err)
	}

	// Retryable failure goes back to the queue, then fails for good.
	if err := s.FailExport(ctx, job.ID, "w1", "transient", true, 0, time.Hour); err != nil {
		t.Fatal(err)
	}
	got, _ := s.GetExport(ctx, job.ID, u.ID)
	if got.Status != domain.StatusQueued || got.Error != "transient" {
		t.Fatalf("after retryable failure: %+v", got)
	}
	job, _ = s.ClaimNext(ctx, "w1", time.Minute)
	if job.ID != e.ID || job.Attempts != 2 {
		t.Fatalf("re-claim: %+v", job)
	}
	if err := s.FailExport(ctx, job.ID, "w1", "still broken", true, 0, time.Hour); err != nil {
		t.Fatal(err)
	}
	got, _ = s.GetExport(ctx, job.ID, u.ID)
	if got.Status != domain.StatusFailed || got.ExpiresAt == nil {
		t.Fatalf("max attempts reached, want failed: %+v", got)
	}

	// Second job succeeds.
	job2, err := s.ClaimNext(ctx, "w1", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.CompleteExport(ctx, job2.ID, "w1", "f.pdf", 42, 3, time.Hour); err != nil {
		t.Fatal(err)
	}
	got, _ = s.GetExport(ctx, job2.ID, u.ID)
	if got.Status != domain.StatusSucceeded || got.FileSize != 42 || got.Downloadable(time.Now()) != nil {
		t.Fatalf("after completion: %+v", got)
	}
	if !errors.Is(got.Downloadable(time.Now().Add(2*time.Hour)), domain.ErrExportExpired) {
		t.Fatal("must be expired after retention")
	}

	// Queue now empty.
	if _, err := s.ClaimNext(ctx, "w1", time.Minute); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("want empty queue, got %v", err)
	}
	st, err := s.QueueStats(ctx)
	if err != nil || st.Queued != 0 || st.Running != 0 {
		t.Fatalf("stats: %+v %v", st, err)
	}
}

func TestStaleLeaseIsReclaimed(t *testing.T) {
	s := testutil.NewStore(t)
	ctx := context.Background()
	u, _ := s.UpsertUser(ctx, "iss", "lease-user", "", "")
	e := newExport(u.ID)
	_ = s.CreateExport(ctx, e, 5)

	if _, err := s.ClaimNext(ctx, "dead-worker", -time.Second); err != nil { // lease already expired
		t.Fatal(err)
	}
	job, err := s.ClaimNext(ctx, "w2", time.Minute)
	if err != nil || job.ID != e.ID || job.Attempts != 2 {
		t.Fatalf("stale job must be reclaimed: %+v %v", job, err)
	}
	if err := s.CompleteExport(ctx, e.ID, "dead-worker", "x", 1, 1, time.Hour); !errors.Is(err, store.ErrLeaseLost) {
		t.Fatalf("zombie worker must not complete the job, got %v", err)
	}
}

func TestExpiryAndDeletion(t *testing.T) {
	s := testutil.NewStore(t)
	ctx := context.Background()
	u, _ := s.UpsertUser(ctx, "iss", "expiry-user", "", "")
	e := newExport(u.ID)
	_ = s.CreateExport(ctx, e, 5)
	job, _ := s.ClaimNext(ctx, "w", time.Minute)
	_ = s.CompleteExport(ctx, job.ID, "w", "k.pdf", 1, 1, -time.Second) // already expired

	expired, err := s.ListExpired(ctx, 10)
	if err != nil || len(expired) != 1 || expired[0].FileKey != "k.pdf" {
		t.Fatalf("list expired: %+v %v", expired, err)
	}
	if err := s.MarkExpired(ctx, e.ID); err != nil {
		t.Fatal(err)
	}
	got, _ := s.GetExport(ctx, e.ID, u.ID)
	if got.Status != domain.StatusExpired || got.FileKey != "" {
		t.Fatalf("after expiry: %+v", got)
	}

	deleted, err := s.DeleteExport(ctx, e.ID, u.ID)
	if err != nil || deleted.ID != e.ID {
		t.Fatalf("delete: %+v %v", deleted, err)
	}
	if _, err := s.DeleteExport(ctx, e.ID, u.ID); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("second delete: %v", err)
	}
}

func TestAuditEvents(t *testing.T) {
	s := testutil.NewStore(t)
	ctx := context.Background()
	alice, bob := uuid.New(), uuid.New()

	var ids []uuid.UUID
	for i, ev := range []domain.AuditEvent{
		{ActorID: &alice, ActorEmail: "alice@example.com", Action: "auth.login", Outcome: domain.AuditSuccess},
		{ActorID: &bob, ActorEmail: "bob@example.com", Action: "export.create", Outcome: domain.AuditSuccess,
			TargetType: "export", TargetID: "x", Details: map[string]any{"format": "pdf", "pages": 3}},
		{ActorID: &alice, ActorEmail: "alice@example.com", Action: "export.create", Outcome: domain.AuditSuccess},
		{Action: "export.expire", Outcome: domain.AuditSuccess},
	} {
		ev.ID = uuid.Must(uuid.NewV7())
		ev.OccurredAt = time.Now().Add(time.Duration(i) * time.Millisecond)
		if err := s.InsertAuditEvent(ctx, ev); err != nil {
			t.Fatal(err)
		}
		ids = append(ids, ev.ID)
	}

	all, err := s.ListAuditEvents(ctx, domain.AuditFilter{})
	if err != nil || len(all) != 4 || all[0].ID != ids[3] {
		t.Fatalf("list all: %d events, err %v", len(all), err)
	}
	if all[2].Details["format"] != "pdf" || all[2].Details["pages"] != float64(3) || all[3].ActorID == nil || *all[3].ActorID != alice {
		t.Errorf("round trip lost data: %+v", all[2])
	}
	if all[0].ActorID != nil {
		t.Error("system event must have no actor")
	}

	byActor, _ := s.ListAuditEvents(ctx, domain.AuditFilter{Actor: "ALICE"})
	byAction, _ := s.ListAuditEvents(ctx, domain.AuditFilter{Action: "export.create"})
	both, _ := s.ListAuditEvents(ctx, domain.AuditFilter{Actor: "alice", Action: "export.create"})
	wildcard, _ := s.ListAuditEvents(ctx, domain.AuditFilter{Actor: "%"})
	if len(byActor) != 2 || len(byAction) != 2 || len(both) != 1 || len(wildcard) != 0 {
		t.Errorf("filters: actor=%d action=%d both=%d wildcard=%d", len(byActor), len(byAction), len(both), len(wildcard))
	}

	page1, _ := s.ListAuditEvents(ctx, domain.AuditFilter{Limit: 3})
	page2, _ := s.ListAuditEvents(ctx, domain.AuditFilter{Limit: 3, Before: &page1[2].ID})
	if len(page1) != 3 || len(page2) != 1 || page2[0].ID != ids[0] {
		t.Errorf("pagination: %d then %d", len(page1), len(page2))
	}

	future := time.Now().Add(time.Hour)
	if none, _ := s.ListAuditEvents(ctx, domain.AuditFilter{From: &future}); len(none) != 0 {
		t.Errorf("from filter returned %d events", len(none))
	}
}

func TestAuditEventsAreAppendOnly(t *testing.T) {
	s := testutil.NewStore(t)
	ctx := context.Background()
	ev := domain.AuditEvent{ID: uuid.Must(uuid.NewV7()), OccurredAt: time.Now().Add(-400 * 24 * time.Hour),
		Action: "auth.login", Outcome: domain.AuditSuccess}
	if err := s.InsertAuditEvent(ctx, ev); err != nil {
		t.Fatal(err)
	}
	if err := s.InsertAuditEvent(ctx, domain.AuditEvent{ID: uuid.Must(uuid.NewV7()), OccurredAt: time.Now(),
		Action: "auth.login", Outcome: domain.AuditSuccess}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Pool().Exec(ctx, `UPDATE audit_events SET action = 'tampered'`); err == nil ||
		!strings.Contains(err.Error(), "append-only") {
		t.Fatalf("update must be refused, got %v", err)
	}
	n, err := s.PurgeAuditEvents(ctx, 365*24*time.Hour)
	if err != nil || n != 1 {
		t.Fatalf("purge: n=%d err=%v", n, err)
	}
}

func TestRecordLogin(t *testing.T) {
	s := testutil.NewStore(t)
	ctx := context.Background()
	u, _ := s.UpsertUser(ctx, "iss", "admin-sub", "a@b.c", "A")
	if u.IsAdmin {
		t.Fatal("new users are not admins")
	}
	if err := s.RecordLogin(ctx, u.ID, true); err != nil {
		t.Fatal(err)
	}
	hash := []byte("admin-session-hash-000000000000")
	_ = s.CreateSession(ctx, hash, u.ID, time.Now().Add(time.Hour))
	got, _, err := s.SessionUser(ctx, hash)
	if err != nil || !got.IsAdmin {
		t.Fatalf("session user admin=%v err=%v", got != nil && got.IsAdmin, err)
	}
}

func TestTakeTokenIsSharedAndAtomic(t *testing.T) {
	s := testutil.NewStore(t)
	ctx := context.Background()

	// 20 concurrent callers against a bucket of 5 that refills slowly: exactly
	// 5 get a token, whatever the interleaving.
	var granted atomic.Int32
	var wg sync.WaitGroup
	for range 20 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ok, _, err := s.TakeToken(ctx, "test", 0.01, 5)
			if err != nil {
				t.Error(err)
			}
			if ok {
				granted.Add(1)
			}
		}()
	}
	wg.Wait()
	if n := granted.Load(); n != 5 {
		t.Fatalf("granted %d tokens, want the burst of 5", n)
	}
	ok, wait, err := s.TakeToken(ctx, "test", 0.01, 5)
	if ok || err != nil || wait < 90*time.Second {
		t.Fatalf("empty bucket: ok=%v wait=%v err=%v", ok, wait, err)
	}
	if ok, _, _ := s.TakeToken(ctx, "other", 1, 1); !ok {
		t.Fatal("buckets are independent")
	}
}
