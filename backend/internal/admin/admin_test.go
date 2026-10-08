package admin_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/ffurlanetto/confluence-to-doc/backend/internal/admin"
	"github.com/ffurlanetto/confluence-to-doc/backend/internal/domain"
	"github.com/ffurlanetto/confluence-to-doc/backend/internal/export"
)

type repo struct {
	admin.Repo // methods a test does not need panic
	cancelled  string
	blocked    uuid.UUID
	from       time.Time
}

func (r *repo) CancelExport(_ context.Context, id uuid.UUID, message string, _ time.Duration) (*domain.Export, error) {
	r.cancelled = message
	return &domain.Export{ID: id, Status: domain.StatusFailed, Error: message}, nil
}

func (r *repo) RetryExport(_ context.Context, id uuid.UUID) (*domain.Export, error) {
	return &domain.Export{ID: id, Status: domain.StatusQueued}, nil
}

func (r *repo) BlockUser(_ context.Context, id uuid.UUID, _, _ string, _ time.Duration) (*domain.User, int64, error) {
	r.blocked = id
	return &domain.User{ID: id}, 0, nil
}

func (r *repo) Usage(_ context.Context, from time.Time) (*domain.Usage, error) {
	r.from = from
	return &domain.Usage{From: from}, nil
}

type notifier struct{ reasons []string }

func (n *notifier) ExportFailed(_ context.Context, _ *domain.Export, reason string) {
	n.reasons = append(n.reasons, reason)
}

func TestCancelTellsTheOwner(t *testing.T) {
	r, n := &repo{}, &notifier{}
	s := admin.NewService(r, n, time.Hour, nil)
	if _, err := s.Cancel(context.Background(), uuid.New()); err != nil {
		t.Fatal(err)
	}
	if r.cancelled != export.MessageCancelled || len(n.reasons) != 1 || n.reasons[0] != export.MessageCancelled {
		t.Fatalf("cancelled with %q, owner told %v", r.cancelled, n.reasons)
	}
}

func TestRetryWakesTheWorkers(t *testing.T) {
	woken := false
	s := admin.NewService(&repo{}, nil, time.Hour, func() { woken = true })
	if _, err := s.Retry(context.Background(), uuid.New()); err != nil || !woken {
		t.Fatalf("Retry: %v, workers woken: %v", err, woken)
	}
}

func TestAnAdministratorCannotBlockThemselves(t *testing.T) {
	r := &repo{}
	s := admin.NewService(r, nil, time.Hour, nil)
	me := &domain.User{ID: uuid.New()}
	if _, _, err := s.Block(context.Background(), me, me.ID, "oops"); !errors.Is(err, domain.ErrSelfAction) {
		t.Fatalf("self block: %v, want ErrSelfAction", err)
	}
	if r.blocked != uuid.Nil {
		t.Fatal("the repository was called")
	}
	other := uuid.New()
	if _, _, err := s.Block(context.Background(), me, other, "left"); err != nil || r.blocked != other {
		t.Fatalf("blocking someone else: %v", err)
	}
}

func TestUsageCountsWholeDaysIncludingToday(t *testing.T) {
	for days, want := range map[int]int{1: 0, 7: 6, 0: 0, 1000: admin.MaxUsageDays - 1} {
		r := &repo{}
		if _, err := admin.NewService(r, nil, time.Hour, nil).Usage(context.Background(), days); err != nil {
			t.Fatal(err)
		}
		today := time.Now().UTC().Truncate(24 * time.Hour)
		if got := int(today.Sub(r.from).Hours() / 24); got != want || !r.from.Equal(r.from.Truncate(24*time.Hour)) {
			t.Errorf("Usage(%d) starts %d days before today at %v, want %d at midnight UTC", days, got, r.from, want)
		}
	}
}
