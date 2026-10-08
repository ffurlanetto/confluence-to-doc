package account_test

import (
	"bytes"
	"context"
	"errors"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"github.com/ffurlanetto/confluence-to-doc/backend/internal/account"
	"github.com/ffurlanetto/confluence-to-doc/backend/internal/confluence/fake"
	"github.com/ffurlanetto/confluence-to-doc/backend/internal/crypto"
	"github.com/ffurlanetto/confluence-to-doc/backend/internal/domain"
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
