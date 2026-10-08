package notify

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/google/uuid"

	"github.com/ffurlanetto/confluence-to-doc/backend/internal/crypto"
	"github.com/ffurlanetto/confluence-to-doc/backend/internal/domain"
)

// permanentError marks a failure that retrying cannot fix.
type permanentError struct{ err error }

func (p permanentError) Error() string { return p.err.Error() }
func (p permanentError) Unwrap() error { return p.err }

// Permanent wraps err as a failure that must not be retried.
func Permanent(err error) error { return permanentError{err} }

// IsPermanent reports whether err was marked permanent.
func IsPermanent(err error) bool { return errors.As(err, new(permanentError)) }

// DeliveryRepo is the outbox the dispatcher drains.
type DeliveryRepo interface {
	ClaimDeliveries(ctx context.Context, limit int, lease time.Duration) ([]domain.Delivery, error)
	FinishDelivery(ctx context.Context, id uuid.UUID, channel domain.Channel, sendErr error, retryAt *time.Time) error
}

// EmailSender and TeamsSender deliver one notification.
type EmailSender interface {
	Send(ctx context.Context, address string, n domain.Notification) error
}

type TeamsSender interface {
	Send(ctx context.Context, url string, n domain.Notification) error
}

// Dispatcher sends pending deliveries, with retries.
type Dispatcher struct {
	repo     DeliveryRepo
	sealer   *crypto.Sealer
	email    EmailSender // nil when SMTP is not configured
	teams    TeamsSender
	interval time.Duration
	now      func() time.Time
}

// maxAttempts bounds the retries of one delivery: with the backoff below,
// a message is given up after about a day of failures.
const maxAttempts = 8

func NewDispatcher(repo DeliveryRepo, sealer *crypto.Sealer, email EmailSender, teams TeamsSender, interval time.Duration) *Dispatcher {
	return &Dispatcher{repo: repo, sealer: sealer, email: email, teams: teams, interval: interval, now: time.Now}
}

// Run sends deliveries until ctx is cancelled.
func (d *Dispatcher) Run(ctx context.Context) {
	t := time.NewTicker(d.interval)
	defer t.Stop()
	for {
		d.RunOnce(ctx)
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// RunOnce sends what is due and returns how many deliveries it handled.
func (d *Dispatcher) RunOnce(ctx context.Context) int {
	batch, err := d.repo.ClaimDeliveries(ctx, 50, 5*time.Minute)
	if err != nil {
		if ctx.Err() == nil {
			slog.ErrorContext(ctx, "notifications: claiming deliveries", "err", err)
		}
		return 0
	}
	for _, del := range batch {
		sendCtx, cancel := context.WithTimeout(ctx, time.Minute)
		err := d.send(sendCtx, del)
		cancel()
		var retryAt *time.Time
		if err != nil && !IsPermanent(err) && del.Attempts < maxAttempts {
			next := d.now().Add(backoff(del.Attempts))
			retryAt = &next
		}
		if err != nil {
			slog.WarnContext(ctx, "notification not delivered", "channel", del.Channel, "attempt", del.Attempts,
				"retry", retryAt != nil, "err", err)
		}
		if ferr := d.repo.FinishDelivery(context.WithoutCancel(ctx), del.Notification.ID, del.Channel, err, retryAt); ferr != nil {
			slog.ErrorContext(ctx, "notifications: recording a delivery", "err", ferr)
		}
	}
	return len(batch)
}

func (d *Dispatcher) send(ctx context.Context, del domain.Delivery) error {
	switch del.Channel {
	case domain.ChannelEmail:
		if d.email == nil {
			return Permanent(errors.New("email is not configured on this server"))
		}
		if del.Email == "" {
			return Permanent(errors.New("the user has no email address"))
		}
		return d.email.Send(ctx, del.Email, del.Notification)
	case domain.ChannelTeams:
		if len(del.EncryptedTeamsWebhook) == 0 {
			return Permanent(errors.New("the user removed their Teams workflow"))
		}
		url, err := d.sealer.Open(del.EncryptedTeamsWebhook, teamsAD(del.Notification.UserID))
		if err != nil {
			return Permanent(err)
		}
		return d.teams.Send(ctx, string(url), del.Notification)
	}
	return Permanent(errors.New("unknown channel"))
}

// backoff is 1, 2, 4… minutes, at most 4 hours.
func backoff(attempt int) time.Duration {
	return min(time.Minute<<min(attempt-1, 8), 4*time.Hour)
}
