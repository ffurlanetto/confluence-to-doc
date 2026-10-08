// Package ratelimit bounds request rates with token buckets: a bucket shared
// by every instance through PostgreSQL (the budget the whole deployment may
// spend on Confluence), and in-memory buckets per key (requests per user or
// per client address on one API instance).
package ratelimit

import (
	"context"
	"log/slog"
	"sync"
	"time"
)

// SharedStore takes a token from a bucket every instance sees.
type SharedStore interface {
	// TakeToken reports whether a token was taken from the named bucket,
	// refilled at rate tokens per second up to burst, and otherwise how long
	// until one is available.
	TakeToken(ctx context.Context, name string, rate, burst float64) (ok bool, wait time.Duration, err error)
}

// Shared is a rate limit common to all instances: workers and API pods draw
// from the same budget, however many of them are running.
type Shared struct {
	store SharedStore
	name  string
	rate  float64
	burst float64
	sleep func(context.Context, time.Duration) error

	mu         sync.Mutex
	lastFailed time.Time
}

// NewShared returns a limiter allowing rate requests per second overall, with
// bursts up to burst.
func NewShared(store SharedStore, name string, rate, burst float64) *Shared {
	return &Shared{store: store, name: name, rate: rate, burst: max(burst, 1), sleep: sleep}
}

// maxPause bounds a single wait, so a bucket refilled by another instance is
// noticed promptly.
const maxPause = time.Second

// Wait blocks until a token is available or ctx ends. If the database cannot
// be reached it lets the request through: failing every export because the
// limiter is unavailable would be worse than an unthrottled minute, and the
// database being down stops exports anyway.
func (s *Shared) Wait(ctx context.Context) error {
	for {
		ok, wait, err := s.store.TakeToken(ctx, s.name, s.rate, s.burst)
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			s.reportFailure(ctx, err)
			return nil
		}
		if ok {
			return nil
		}
		if err := s.sleep(ctx, min(max(wait, 10*time.Millisecond), maxPause)); err != nil {
			return err
		}
	}
}

func (s *Shared) reportFailure(ctx context.Context, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if time.Since(s.lastFailed) < time.Minute {
		return
	}
	s.lastFailed = time.Now()
	slog.WarnContext(ctx, "rate limit unavailable, letting requests through", "limit", s.name, "err", err)
}

func sleep(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// Keyed holds one in-memory token bucket per key (a user, a client address).
// Buckets live on one instance: with N API instances behind a load balancer,
// a client gets up to N times the limit, which is fine for its purpose of
// stopping a runaway script or a flood of bogus logins.
type Keyed struct {
	rate  float64 // tokens per second
	burst float64
	now   func() time.Time

	mu      sync.Mutex
	buckets map[string]*bucket
	swept   time.Time
}

type bucket struct {
	tokens float64
	at     time.Time
}

// NewKeyed allows perMinute requests per minute per key, with bursts of burst.
func NewKeyed(perMinute, burst int) *Keyed {
	return &Keyed{rate: float64(perMinute) / 60, burst: float64(max(burst, 1)), now: time.Now, buckets: map[string]*bucket{}}
}

// Allow takes a token for key, or reports how long until one is available.
func (k *Keyed) Allow(key string) (bool, time.Duration) {
	k.mu.Lock()
	defer k.mu.Unlock()
	now := k.now()
	k.sweep(now)
	b, ok := k.buckets[key]
	if !ok {
		b = &bucket{tokens: k.burst, at: now}
		k.buckets[key] = b
	}
	b.tokens = min(k.burst, b.tokens+now.Sub(b.at).Seconds()*k.rate)
	b.at = now
	if b.tokens >= 1 {
		b.tokens--
		return true, 0
	}
	return false, time.Duration((1 - b.tokens) / k.rate * float64(time.Second))
}

// sweep forgets the buckets that have refilled completely: they behave like
// new ones, and the map stays as small as the set of active clients.
func (k *Keyed) sweep(now time.Time) {
	if now.Sub(k.swept) < time.Minute {
		return
	}
	k.swept = now
	for key, b := range k.buckets {
		if b.tokens+now.Sub(b.at).Seconds()*k.rate >= k.burst {
			delete(k.buckets, key)
		}
	}
}
