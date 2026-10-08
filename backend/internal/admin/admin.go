// Package admin implements the administration console's use cases: the
// export queue of every user, accounts, and usage figures. The Word template
// is managed by doctemplate, the audit trail read straight from the store.
package admin

import (
	"context"
	"time"

	"github.com/google/uuid"

	"github.com/ffurlanetto/confluence-to-doc/backend/internal/domain"
	"github.com/ffurlanetto/confluence-to-doc/backend/internal/export"
)

type Repo interface {
	ListAllExports(ctx context.Context, statuses []domain.ExportStatus, limit int) ([]domain.AdminExport, error)
	CancelExport(ctx context.Context, id uuid.UUID, message string, retention time.Duration) (*domain.Export, error)
	RetryExport(ctx context.Context, id uuid.UUID) (*domain.Export, error)
	ListUsers(ctx context.Context, query string, limit int) ([]domain.UserSummary, error)
	BlockUser(ctx context.Context, id uuid.UUID, reason, message string, retention time.Duration) (*domain.User, int64, error)
	UnblockUser(ctx context.Context, id uuid.UUID) (*domain.User, error)
	Usage(ctx context.Context, from time.Time) (*domain.Usage, error)
}

// Notifier tells a user their export was stopped (implemented by
// notify.Service).
type Notifier interface {
	ExportFailed(ctx context.Context, job *domain.Export, reason string)
}

const (
	// QueueLimit bounds the exports listed at once.
	QueueLimit = 200
	// UserLimit bounds the users listed at once; a search narrows them.
	UserLimit = 200
	// MaxUsageDays is the longest period usage is computed over: the audit
	// trail it is read from is kept one year by default.
	MaxUsageDays = 366
	// MaxReasonLength bounds the reason given for blocking an account.
	MaxReasonLength = 500
)

type Service struct {
	repo      Repo
	notify    Notifier
	wake      func()
	retention time.Duration
	now       func() time.Time
}

// NewService returns the service. retention is how long a cancelled export
// stays listed; wake, which may be nil, wakes local workers after a retry.
func NewService(repo Repo, notify Notifier, retention time.Duration, wake func()) *Service {
	return &Service{repo: repo, notify: notify, wake: wake, retention: retention, now: time.Now}
}

// Queue lists exports of every user in the given statuses (all when none).
func (s *Service) Queue(ctx context.Context, statuses []domain.ExportStatus) ([]domain.AdminExport, error) {
	return s.repo.ListAllExports(ctx, statuses, QueueLimit)
}

// Cancel stops a queued or running export and tells its owner.
func (s *Service) Cancel(ctx context.Context, id uuid.UUID) (*domain.Export, error) {
	e, err := s.repo.CancelExport(ctx, id, export.MessageCancelled, s.retention)
	if err != nil {
		return nil, err
	}
	if s.notify != nil {
		s.notify.ExportFailed(ctx, e, export.MessageCancelled)
	}
	return e, nil
}

// Retry puts a failed export back in the queue.
func (s *Service) Retry(ctx context.Context, id uuid.UUID) (*domain.Export, error) {
	e, err := s.repo.RetryExport(ctx, id)
	if err != nil {
		return nil, err
	}
	if s.wake != nil {
		s.wake()
	}
	return e, nil
}

// Users lists accounts whose email or name contains query.
func (s *Service) Users(ctx context.Context, query string) ([]domain.UserSummary, error) {
	return s.repo.ListUsers(ctx, query, UserLimit)
}

// Block blocks an account, ends its sessions and cancels its pending
// exports. An administrator cannot block themselves.
func (s *Service) Block(ctx context.Context, actor *domain.User, id uuid.UUID, reason string) (*domain.User, int64, error) {
	if actor != nil && actor.ID == id {
		return nil, 0, domain.ErrSelfAction
	}
	return s.repo.BlockUser(ctx, id, reason, export.MessageAccountBlocked, s.retention)
}

// Unblock lets the account sign in again.
func (s *Service) Unblock(ctx context.Context, id uuid.UUID) (*domain.User, error) {
	return s.repo.UnblockUser(ctx, id)
}

// Usage summarises the last days days, counted in whole UTC days including
// today.
func (s *Service) Usage(ctx context.Context, days int) (*domain.Usage, error) {
	days = min(max(days, 1), MaxUsageDays)
	today := s.now().UTC().Truncate(24 * time.Hour)
	return s.repo.Usage(ctx, today.AddDate(0, 0, -(days-1)))
}
