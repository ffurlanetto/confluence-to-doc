package account

import (
	"context"
	"log/slog"
	"time"

	"github.com/google/uuid"

	"github.com/ffurlanetto/confluence-to-doc/backend/internal/audit"
	"github.com/ffurlanetto/confluence-to-doc/backend/internal/domain"
)

// LifecycleRepo is what deleting and purging accounts needs.
type LifecycleRepo interface {
	DeleteUser(ctx context.Context, id uuid.UUID) ([]string, error)
	InactiveUsers(ctx context.Context, cutoff, warnedBefore time.Time, limit int) ([]domain.User, error)
	UsersToWarn(ctx context.Context, cutoff time.Time, limit int) ([]domain.User, error)
	MarkWarned(ctx context.Context, id uuid.UUID) error
}

// FileDeleter removes generated documents (storage.BlobStore).
type FileDeleter interface {
	Delete(ctx context.Context, key string) error
}

// Auditor records security events (implemented by audit.Recorder).
type Auditor interface {
	Record(ctx context.Context, e domain.AuditEvent)
}

// Notifier tells a user their inactive account is about to be deleted.
type Notifier interface {
	InactiveAccountWarning(ctx context.Context, u domain.User, deletion time.Time) error
}

// Lifecycle deletes accounts: on the user's request, and after a period of
// inactivity (GDPR storage limitation). Deleting an account removes the
// stored token, the preferences, the sessions and every export with its file;
// the audit trail keeps its own retention.
type Lifecycle struct {
	repo   LifecycleRepo
	files  FileDeleter
	audit  Auditor
	notify Notifier
	now    func() time.Time

	// Retention is how long an account may stay unused (0 = forever);
	// Notice is how long before the deletion its owner is warned.
	Retention time.Duration
	Notice    time.Duration
}

func NewLifecycle(repo LifecycleRepo, files FileDeleter, auditor Auditor, notify Notifier) *Lifecycle {
	return &Lifecycle{repo: repo, files: files, audit: auditor, notify: notify, now: time.Now}
}

// SetClock replaces the clock, for tests.
func (l *Lifecycle) SetClock(now func() time.Time) { l.now = now }

// DeleteAccount erases a user's data at their request.
func (l *Lifecycle) DeleteAccount(ctx context.Context, u *domain.User) error {
	keys, err := l.repo.DeleteUser(ctx, u.ID)
	if err != nil {
		return err
	}
	removed := l.deleteFiles(ctx, keys)
	actorID, email := audit.Actor(u)
	l.audit.Record(ctx, domain.AuditEvent{
		ActorID: actorID, ActorEmail: email, Action: audit.ActionAccountDelete, Outcome: domain.AuditSuccess,
		TargetType: audit.TargetUser, TargetID: u.ID.String(), Details: map[string]any{"files": removed},
	})
	return nil
}

// purgeBatch bounds one pass of Purge.
const purgeBatch = 100

// Purge warns the owners of accounts approaching the end of their retention,
// then deletes the accounts that reached it. It returns how many of each.
func (l *Lifecycle) Purge(ctx context.Context) (warned, deleted int, err error) {
	if l.Retention <= 0 {
		return 0, 0, nil
	}
	now := l.now()
	toWarn, err := l.repo.UsersToWarn(ctx, now.Add(-(l.Retention - l.Notice)), purgeBatch)
	if err != nil {
		return 0, 0, err
	}
	for _, u := range toWarn {
		// Never sooner than the notice period, whatever the inactivity.
		deletion := u.LastActiveAt.Add(l.Retention)
		if earliest := now.Add(l.Notice); deletion.Before(earliest) {
			deletion = earliest
		}
		if err := l.notify.InactiveAccountWarning(ctx, u, deletion); err != nil {
			slog.WarnContext(ctx, "warning an inactive user failed", "user_id", u.ID, "err", err)
			continue // try again on the next pass
		}
		if err := l.repo.MarkWarned(ctx, u.ID); err != nil {
			return warned, deleted, err
		}
		warned++
	}

	expired, err := l.repo.InactiveUsers(ctx, now.Add(-l.Retention), now.Add(-l.Notice), purgeBatch)
	if err != nil {
		return warned, deleted, err
	}
	for _, u := range expired {
		keys, err := l.repo.DeleteUser(ctx, u.ID)
		if err != nil {
			return warned, deleted, err
		}
		removed := l.deleteFiles(ctx, keys)
		l.audit.Record(ctx, domain.AuditEvent{
			Action: audit.ActionAccountPurge, Outcome: domain.AuditSuccess,
			TargetType: audit.TargetUser, TargetID: u.ID.String(),
			Details: map[string]any{"email": u.Email, "lastActive": u.LastActiveAt.UTC().Format(time.RFC3339), "files": removed},
		})
		deleted++
	}
	return warned, deleted, nil
}

// deleteFiles removes the documents of a deleted account. A file that cannot
// be removed now is left to the storage's own lifecycle rules: its row is
// gone, so nothing can serve it any more.
func (l *Lifecycle) deleteFiles(ctx context.Context, keys []string) int {
	removed := 0
	for _, k := range keys {
		if err := l.files.Delete(ctx, k); err != nil {
			slog.WarnContext(ctx, "deleting a file of a deleted account", "key", k, "err", err)
			continue
		}
		removed++
	}
	return removed
}

// LogNotifier only logs the warning, for deployments without a notification
// channel: the account is still deleted on time.
type LogNotifier struct{}

func (LogNotifier) InactiveAccountWarning(ctx context.Context, u domain.User, deletion time.Time) error {
	slog.InfoContext(ctx, "inactive account will be deleted", "user_id", u.ID, "deletion", deletion.Format(time.DateOnly))
	return nil
}
