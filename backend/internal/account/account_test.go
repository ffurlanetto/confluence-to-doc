package account_test

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/ffurlanetto/confluence-to-doc/backend/internal/account"
	"github.com/ffurlanetto/confluence-to-doc/backend/internal/confluence/fake"
	"github.com/ffurlanetto/confluence-to-doc/backend/internal/crypto"
	"github.com/ffurlanetto/confluence-to-doc/backend/internal/domain"
	"github.com/ffurlanetto/confluence-to-doc/backend/internal/storage"
	"github.com/ffurlanetto/confluence-to-doc/backend/internal/testutil"
)

func key(b byte) []byte { return bytes.Repeat([]byte{b}, 32) }

func TestKeyRotation(t *testing.T) {
	s := testutil.NewStore(t)
	ctx := context.Background()
	srv := httptest.NewServer(fake.New("pat"))
	t.Cleanup(srv.Close)
	confURL, _ := url.Parse(srv.URL)

	oldSealer, _ := crypto.NewSealer(key(1))
	before := account.NewService(s, oldSealer, confURL, 5*time.Second)
	var users []*domain.User
	for _, sub := range []string{"a", "b", "c"} {
		u, _ := s.UpsertUser(ctx, "iss", sub, "", "")
		if _, err := before.SetPAT(ctx, u.ID, "pat"); err != nil {
			t.Fatal(err)
		}
		users = append(users, u)
	}

	// The new key is added in front; the old one stays until rotation is done.
	ring, _ := crypto.NewKeyRing([]crypto.Key{{ID: "2026-10", Secret: key(2)}, {ID: crypto.DefaultKeyID, Secret: key(1)}})
	after := account.NewService(s, ring, confURL, 5*time.Second)
	rotated, unreadable, err := after.RotateKeys(ctx)
	if err != nil || rotated != 3 || unreadable != 0 {
		t.Fatalf("first pass: rotated=%d unreadable=%d err=%v", rotated, unreadable, err)
	}
	if rotated, _, _ = after.RotateKeys(ctx); rotated != 0 {
		t.Fatalf("second pass re-encrypted %d tokens again", rotated)
	}

	// The old key can now go: every token opens with the new one alone.
	newOnly, _ := crypto.NewKeyRing([]crypto.Key{{ID: "2026-10", Secret: key(2)}})
	final := account.NewService(s, newOnly, confURL, 5*time.Second)
	for _, u := range users {
		if _, err := final.Client(ctx, u.ID); err != nil {
			t.Fatalf("user %s: %v", u.Subject, err)
		}
	}

	// A token written by a key that left the ring is reported, not lost: it
	// stays in place in case the key comes back, and the user is told to
	// enter it again.
	stranger, _ := crypto.NewSealer(key(9))
	orphan, _ := s.UpsertUser(ctx, "iss", "orphan", "", "")
	if _, err := account.NewService(s, stranger, confURL, 5*time.Second).SetPAT(ctx, orphan.ID, "pat"); err != nil {
		t.Fatal(err)
	}
	if _, err := final.Client(ctx, orphan.ID); !errors.Is(err, domain.ErrPATUnreadable) {
		t.Fatalf("want ErrPATUnreadable, got %v", err)
	}
	if rotated, unreadable, err = final.RotateKeys(ctx); rotated != 0 || unreadable != 1 || err != nil {
		t.Fatalf("orphan pass: rotated=%d unreadable=%d err=%v", rotated, unreadable, err)
	}
}

type recordedEvents struct{ list []domain.AuditEvent }

func (r *recordedEvents) Record(_ context.Context, e domain.AuditEvent) { r.list = append(r.list, e) }

type warnings struct{ users []string }

func (w *warnings) InactiveAccountWarning(_ context.Context, u domain.User, _ time.Time) error {
	w.users = append(w.users, u.Subject)
	return nil
}

func TestAccountLifecycle(t *testing.T) {
	s := testutil.NewStore(t)
	ctx := context.Background()
	blobs, _ := storage.NewLocal(t.TempDir())
	events, warned := &recordedEvents{}, &warnings{}
	l := account.NewLifecycle(s, blobs, events, warned)
	l.Retention, l.Notice = 180*24*time.Hour, 15*24*time.Hour

	newUser := func(sub string, lastActive time.Duration) *domain.User {
		u, _ := s.UpsertUser(ctx, "iss", sub, sub+"@example.com", sub)
		if _, err := s.Pool().Exec(ctx, `UPDATE users SET last_login_at = now() - make_interval(secs => $2) WHERE id = $1`,
			u.ID, lastActive.Seconds()); err != nil {
			t.Fatal(err)
		}
		e := &domain.Export{ID: uuid.Must(uuid.NewV7()), UserID: u.ID, RootPageID: "1", RootTitle: "R", Format: domain.FormatPDF, MaxAttempts: 1}
		if err := s.CreateExport(ctx, e, 10); err != nil {
			t.Fatal(err)
		}
		claimed, _ := s.ClaimNext(ctx, "w", time.Minute)
		key := claimed.ID.String() + ".pdf"
		_, _ = blobs.Put(ctx, key, strings.NewReader("doc"))
		if err := s.CompleteExport(ctx, claimed.ID, "w", key, 3, 1, time.Hour); err != nil {
			t.Fatal(err)
		}
		return u
	}
	active := newUser("active", 24*time.Hour)
	soon := newUser("soon", 170*24*time.Hour)
	gone := newUser("gone", 200*24*time.Hour)

	// First pass: both inactive accounts are warned, none is deleted — not
	// even "gone", already past the retention: nobody goes without notice.
	warnedN, deleted, err := l.Purge(ctx)
	if err != nil || warnedN != 2 || deleted != 0 || strings.Join(warned.users, ",") != "gone,soon" {
		t.Fatalf("first pass: warned=%d %v deleted=%d err=%v", warnedN, warned.users, deleted, err)
	}
	if w, d, _ := l.Purge(ctx); w != 0 || d != 0 {
		t.Errorf("second pass warned %d and deleted %d again", w, d)
	}
	// "soon" signs in again, which clears its warning; then the notice
	// period elapses.
	if err := s.RecordLogin(ctx, soon.ID, false); err != nil {
		t.Fatal(err)
	}
	l.SetClock(func() time.Time { return time.Now().Add(16 * 24 * time.Hour) })
	if _, deleted, err = l.Purge(ctx); err != nil || deleted != 1 {
		t.Fatalf("after the notice: deleted=%d err=%v", deleted, err)
	}
	if _, err := s.GetUser(ctx, gone.ID); !errors.Is(err, domain.ErrNotFound) {
		t.Error("the inactive account must be deleted")
	}
	for _, u := range []*domain.User{active, soon} {
		if _, err := s.GetUser(ctx, u.ID); err != nil {
			t.Errorf("%s must be kept: %v", u.Subject, err)
		}
	}
	if len(events.list) != 1 || events.list[0].Action != "account.purge" || events.list[0].Details["files"] != 1 {
		t.Fatalf("purge not audited: %+v", events.list)
	}

	// Deleting on request removes everything, files included.
	list, _ := s.ListExports(ctx, active.ID, 10)
	if err := l.DeleteAccount(ctx, active); err != nil {
		t.Fatal(err)
	}
	if _, err := blobs.Open(ctx, list[0].FileKey); err == nil {
		t.Error("the account's files must be deleted")
	}
	if last := events.list[len(events.list)-1]; last.Action != "account.delete" || *last.ActorID != active.ID {
		t.Errorf("deletion not audited: %+v", last)
	}
	if err := l.DeleteAccount(ctx, active); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("deleting twice: %v", err)
	}
}

func TestSetPATRecordsItsExpiry(t *testing.T) {
	s := testutil.NewStore(t)
	ctx := context.Background()
	token := base64.StdEncoding.EncodeToString([]byte("987654321098:secret"))
	f := fake.New(token)
	f.TokenExpiry = "2026-12-31T10:00:00.000+0000"
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	confURL, _ := url.Parse(srv.URL)
	sealer, _ := crypto.NewSealer(key(1))
	svc := account.NewService(s, sealer, confURL, 5*time.Second)
	u, _ := s.UpsertUser(ctx, "iss", "exp", "", "")
	if _, err := svc.SetPAT(ctx, u.ID, token); err != nil {
		t.Fatal(err)
	}
	p, _ := svc.Preferences(ctx, u.ID)
	if p.PATExpiresAt == nil || !p.PATExpiresAt.Equal(time.Date(2026, 12, 31, 10, 0, 0, 0, time.UTC)) {
		t.Fatalf("expiry = %v", p.PATExpiresAt)
	}
	if err := svc.ClearPAT(ctx, u.ID); err != nil {
		t.Fatal(err)
	}
	if p, _ = svc.Preferences(ctx, u.ID); p.PATExpiresAt != nil {
		t.Error("clearing the token clears its expiry")
	}
}
