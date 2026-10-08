package store

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/ffurlanetto/confluence-to-doc/backend/internal/domain"
)

// CreateNotification stores a notification and a pending delivery per channel.
func (s *Store) CreateNotification(ctx context.Context, n domain.Notification, channels []domain.Channel) error {
	return pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `INSERT INTO notifications (id, user_id, kind, title, body, link)
			VALUES ($1, $2, $3, $4, $5, $6)`, n.ID, n.UserID, string(n.Kind), n.Title, n.Body, n.Link); err != nil {
			return err
		}
		for _, c := range channels {
			if _, err := tx.Exec(ctx, `INSERT INTO notification_deliveries (notification_id, channel) VALUES ($1, $2)`,
				n.ID, string(c)); err != nil {
				return err
			}
		}
		return nil
	})
}

// ClaimDeliveries takes up to limit due deliveries and pushes their next
// attempt lease ahead, so that another dispatcher does not send them too. The
// recipient is read now: an email changed or a Teams URL removed since the
// notification was created is honoured.
func (s *Store) ClaimDeliveries(ctx context.Context, limit int, lease time.Duration) ([]domain.Delivery, error) {
	rows, err := s.pool.Query(ctx, `
		WITH due AS (
			SELECT notification_id, channel FROM notification_deliveries
			 WHERE status = 'pending' AND next_attempt_at <= now()
			 ORDER BY next_attempt_at LIMIT $1 FOR UPDATE SKIP LOCKED)
		UPDATE notification_deliveries d
		   SET next_attempt_at = now() + make_interval(secs => $2), attempts = d.attempts + 1
		  FROM due, notifications n
		  JOIN users u ON u.id = n.user_id
		  LEFT JOIN user_preferences p ON p.user_id = n.user_id
		 WHERE d.notification_id = due.notification_id AND d.channel = due.channel AND n.id = d.notification_id
		RETURNING n.id, n.user_id, n.kind, n.title, n.body, n.link, n.created_at,
		          d.channel, d.attempts, u.email, p.teams_webhook`, limit, lease.Seconds())
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (domain.Delivery, error) {
		var d domain.Delivery
		var kind, channel string
		err := row.Scan(&d.Notification.ID, &d.Notification.UserID, &kind, &d.Notification.Title, &d.Notification.Body,
			&d.Notification.Link, &d.Notification.CreatedAt, &channel, &d.Attempts, &d.Email, &d.EncryptedTeamsWebhook)
		d.Notification.Kind, d.Channel = domain.NotificationKind(kind), domain.Channel(channel)
		return d, err
	})
}

// FinishDelivery records the outcome of an attempt: sent, retried at
// retryAt, or given up for good when retryAt is nil.
func (s *Store) FinishDelivery(ctx context.Context, id uuid.UUID, channel domain.Channel, sendErr error, retryAt *time.Time) error {
	switch {
	case sendErr == nil:
		_, err := s.pool.Exec(ctx, `UPDATE notification_deliveries SET status = 'sent', sent_at = now(), last_error = ''
			WHERE notification_id = $1 AND channel = $2`, id, string(channel))
		return err
	case retryAt != nil:
		_, err := s.pool.Exec(ctx, `UPDATE notification_deliveries SET next_attempt_at = $3, last_error = $4
			WHERE notification_id = $1 AND channel = $2`, id, string(channel), *retryAt, truncate(sendErr.Error(), 500))
		return err
	default:
		_, err := s.pool.Exec(ctx, `UPDATE notification_deliveries SET status = 'failed', last_error = $3
			WHERE notification_id = $1 AND channel = $2`, id, string(channel), truncate(sendErr.Error(), 500))
		return err
	}
}

// ListNotifications returns the user's latest notifications and how many of
// all of them are unread.
func (s *Store) ListNotifications(ctx context.Context, userID uuid.UUID, limit int) ([]domain.Notification, int, error) {
	rows, err := s.pool.Query(ctx, `SELECT id, user_id, kind, title, body, link, created_at, read_at
		FROM notifications WHERE user_id = $1 ORDER BY created_at DESC LIMIT $2`, userID, limit)
	if err != nil {
		return nil, 0, err
	}
	list, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (domain.Notification, error) {
		var n domain.Notification
		var kind string
		err := row.Scan(&n.ID, &n.UserID, &kind, &n.Title, &n.Body, &n.Link, &n.CreatedAt, &n.ReadAt)
		n.Kind = domain.NotificationKind(kind)
		return n, err
	})
	if err != nil {
		return nil, 0, err
	}
	var unread int
	err = s.pool.QueryRow(ctx, `SELECT count(*) FROM notifications WHERE user_id = $1 AND read_at IS NULL`, userID).Scan(&unread)
	return list, unread, err
}

// MarkNotificationsRead marks all the user's notifications read.
func (s *Store) MarkNotificationsRead(ctx context.Context, userID uuid.UUID) error {
	_, err := s.pool.Exec(ctx, `UPDATE notifications SET read_at = now() WHERE user_id = $1 AND read_at IS NULL`, userID)
	return err
}

// PurgeNotifications deletes notifications older than keep.
func (s *Store) PurgeNotifications(ctx context.Context, keep time.Duration) (int64, error) {
	tag, err := s.pool.Exec(ctx, `DELETE FROM notifications WHERE created_at < now() - make_interval(secs => $1)`, keep.Seconds())
	return tag.RowsAffected(), err
}

// SetNotificationPreferences stores the user's notification choices.
func (s *Store) SetNotificationPreferences(ctx context.Context, userID uuid.UUID, email, exports bool) error {
	_, err := s.pool.Exec(ctx, `
		INSERT INTO user_preferences (user_id, notify_email, notify_exports) VALUES ($1, $2, $3)
		ON CONFLICT (user_id) DO UPDATE
		   SET notify_email = EXCLUDED.notify_email, notify_exports = EXCLUDED.notify_exports, updated_at = now()`,
		userID, email, exports)
	return err
}

// SetTeamsWebhook stores (encrypted != nil) or removes the Teams workflow URL.
func (s *Store) SetTeamsWebhook(ctx context.Context, userID uuid.UUID, encrypted []byte) error {
	_, err := s.pool.Exec(ctx, `
		INSERT INTO user_preferences (user_id, teams_webhook) VALUES ($1, $2)
		ON CONFLICT (user_id) DO UPDATE SET teams_webhook = EXCLUDED.teams_webhook, updated_at = now()`,
		userID, encrypted)
	return err
}

// PATsExpiringBefore returns users whose token expires before cutoff and who
// were not told yet; MarkPATExpiryNotified records that they were.
func (s *Store) PATsExpiringBefore(ctx context.Context, cutoff time.Time, limit int) ([]domain.Preferences, error) {
	rows, err := s.pool.Query(ctx, `SELECT user_id, pat_expires_at FROM user_preferences
		 WHERE encrypted_pat IS NOT NULL AND pat_expires_at < $1 AND pat_expiry_notified_at IS NULL
		 ORDER BY pat_expires_at LIMIT $2`, cutoff, limit)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (domain.Preferences, error) {
		var p domain.Preferences
		err := row.Scan(&p.UserID, &p.PATExpiresAt)
		return p, err
	})
}

func (s *Store) MarkPATExpiryNotified(ctx context.Context, userID uuid.UUID) error {
	_, err := s.pool.Exec(ctx, `UPDATE user_preferences SET pat_expiry_notified_at = now() WHERE user_id = $1`, userID)
	return err
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}
