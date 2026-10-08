package ratelimit

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestKeyed(t *testing.T) {
	now := time.Date(2026, 10, 8, 9, 0, 0, 0, time.UTC)
	k := NewKeyed(60, 3) // one per second, bursts of three
	k.now = func() time.Time { return now }

	for i := range 3 {
		if ok, _ := k.Allow("alice"); !ok {
			t.Fatalf("request %d within the burst refused", i+1)
		}
	}
	ok, wait := k.Allow("alice")
	if ok || wait <= 0 || wait > time.Second {
		t.Fatalf("4th request: ok=%v wait=%v", ok, wait)
	}
	if ok, _ := k.Allow("bob"); !ok {
		t.Fatal("buckets are per key")
	}
	now = now.Add(time.Second)
	if ok, _ := k.Allow("alice"); !ok {
		t.Fatal("a token comes back after a second")
	}

	now = now.Add(time.Hour)
	k.Allow("carol")
	if _, kept := k.buckets["alice"]; kept {
		t.Error("full buckets are forgotten")
	}
}

type fakeStore struct {
	answers []bool
	err     error
	calls   int
}

func (f *fakeStore) TakeToken(context.Context, string, float64, float64) (bool, time.Duration, error) {
	f.calls++
	if f.err != nil {
		return false, 0, f.err
	}
	ok := f.answers[0]
	f.answers = f.answers[1:]
	return ok, 300 * time.Millisecond, nil
}

func TestSharedWaitsForAToken(t *testing.T) {
	store := &fakeStore{answers: []bool{false, false, true}}
	s := NewShared(store, "confluence", 10, 20)
	var slept []time.Duration
	s.sleep = func(_ context.Context, d time.Duration) error { slept = append(slept, d); return nil }
	if err := s.Wait(context.Background()); err != nil {
		t.Fatal(err)
	}
	if store.calls != 3 || len(slept) != 2 || slept[0] != 300*time.Millisecond {
		t.Fatalf("calls=%d slept=%v", store.calls, slept)
	}
}

func TestSharedFailsOpen(t *testing.T) {
	s := NewShared(&fakeStore{err: errors.New("db down")}, "confluence", 10, 20)
	if err := s.Wait(context.Background()); err != nil {
		t.Fatalf("an unavailable limiter must not block requests: %v", err)
	}
}

func TestSharedHonoursCancellation(t *testing.T) {
	s := NewShared(&fakeStore{answers: []bool{false, false}}, "confluence", 10, 20)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := s.Wait(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("want context.Canceled, got %v", err)
	}
}
