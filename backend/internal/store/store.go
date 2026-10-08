// Package store is the PostgreSQL persistence layer. The exports table
// doubles as a durable job queue: workers claim jobs with
// SELECT ... FOR UPDATE SKIP LOCKED and hold them with a renewable lease, so
// several worker processes can run safely and a crashed worker's jobs are
// picked up again once its lease expires.
package store

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/ffurlanetto/confluence-to-doc/backend/internal/domain"
)

type Store struct {
	pool *pgxpool.Pool
}

func Open(ctx context.Context, dsn string) (*Store, error) {
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		return nil, fmt.Errorf("parsing DATABASE_URL: %w", err)
	}
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, err
	}
	pingCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if err := pool.Ping(pingCtx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("connecting to database: %w", err)
	}
	return &Store{pool: pool}, nil
}

func (s *Store) Close() { s.pool.Close() }

// Pool exposes the connection pool, for tests that set up or check state the
// store's own methods do not cover (backdating a sign-in, a database rule).
func (s *Store) Pool() *pgxpool.Pool { return s.pool }

func (s *Store) Ping(ctx context.Context) error { return s.pool.Ping(ctx) }

// ---------------------------------------------------------------- users

func (s *Store) UpsertUser(ctx context.Context, issuer, subject, email, name string) (*domain.User, error) {
	u := &domain.User{}
	err := s.pool.QueryRow(ctx, `
		INSERT INTO users (id, issuer, subject, email, name) VALUES ($1, $2, $3, $4, $5)
		ON CONFLICT (issuer, subject) DO UPDATE
		   SET email = EXCLUDED.email, name = EXCLUDED.name, updated_at = now()
		RETURNING id, issuer, subject, email, name, is_admin, created_at, blocked_at, blocked_reason`,
		uuid.Must(uuid.NewV7()), issuer, subject, email, name,
	).Scan(&u.ID, &u.Issuer, &u.Subject, &u.Email, &u.Name, &u.IsAdmin, &u.CreatedAt, &u.BlockedAt, &u.BlockedReason)
	return u, err
}

// GetUser returns a user by id.
func (s *Store) GetUser(ctx context.Context, id uuid.UUID) (*domain.User, error) {
	u := &domain.User{}
	err := s.pool.QueryRow(ctx, `
		SELECT id, issuer, subject, email, name, is_admin, created_at, blocked_at, blocked_reason FROM users WHERE id = $1`, id).
		Scan(&u.ID, &u.Issuer, &u.Subject, &u.Email, &u.Name, &u.IsAdmin, &u.CreatedAt, &u.BlockedAt, &u.BlockedReason)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, domain.ErrNotFound
	}
	return u, err
}

// RecordLogin stores the outcome of a successful login: the admin flag the
// identity provider granted this time, and the login time.
func (s *Store) RecordLogin(ctx context.Context, userID uuid.UUID, isAdmin bool) error {
	_, err := s.pool.Exec(ctx,
		`UPDATE users SET is_admin = $2, last_login_at = now(), inactivity_warned_at = NULL WHERE id = $1`, userID, isAdmin)
	return err
}

// ------------------------------------------------------------- sessions

func (s *Store) CreateSession(ctx context.Context, tokenHash []byte, sess domain.Session) error {
	_, err := s.pool.Exec(ctx, `
		INSERT INTO sessions (token_hash, user_id, expires_at, sid, refresh_token, revalidate_at)
		VALUES ($1, $2, $3, $4, $5, $6)`,
		tokenHash, sess.UserID, sess.ExpiresAt, sess.SID, sess.RefreshToken, sess.RevalidateAt)
	return err
}

// SessionUser returns the user owning a valid (non-expired) session. A
// blocked account has no valid session.
func (s *Store) SessionUser(ctx context.Context, tokenHash []byte) (*domain.User, *domain.Session, error) {
	u := &domain.User{}
	sess := &domain.Session{}
	err := s.pool.QueryRow(ctx, `
		SELECT u.id, u.issuer, u.subject, u.email, u.name, u.is_admin, u.created_at,
		       s.expires_at, s.sid, s.refresh_token, s.revalidate_at
		  FROM sessions s JOIN users u ON u.id = s.user_id
		 WHERE s.token_hash = $1 AND s.expires_at > now() AND u.blocked_at IS NULL`, tokenHash,
	).Scan(&u.ID, &u.Issuer, &u.Subject, &u.Email, &u.Name, &u.IsAdmin, &u.CreatedAt,
		&sess.ExpiresAt, &sess.SID, &sess.RefreshToken, &sess.RevalidateAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil, domain.ErrNotFound
	}
	if err != nil {
		return nil, nil, err
	}
	sess.UserID = u.ID
	return u, sess, nil
}

// ClaimRevalidation takes the session's re-check for the caller, pushing the
// next one to next, so that concurrent requests do not refresh the same token
// twice (providers that rotate refresh tokens revoke a reused one). It reports
// false when the re-check is not due or another request took it.
func (s *Store) ClaimRevalidation(ctx context.Context, tokenHash []byte, next time.Time) (bool, error) {
	tag, err := s.pool.Exec(ctx, `
		UPDATE sessions SET revalidate_at = $2
		 WHERE token_hash = $1 AND revalidate_at <= now() AND expires_at > now()`, tokenHash, next)
	return tag.RowsAffected() == 1, err
}

// StoreRefreshToken keeps the refresh token the provider issued in exchange
// for the previous one.
func (s *Store) StoreRefreshToken(ctx context.Context, tokenHash, refreshToken []byte) error {
	_, err := s.pool.Exec(ctx, `UPDATE sessions SET refresh_token = $2 WHERE token_hash = $1`, tokenHash, refreshToken)
	return err
}

// DeleteSessionsBySID ends the sessions opened under an identity provider
// session; DeleteSessionsBySubject ends every session of a user.
func (s *Store) DeleteSessionsBySID(ctx context.Context, sid string) (int64, error) {
	if sid == "" {
		return 0, nil
	}
	tag, err := s.pool.Exec(ctx, `DELETE FROM sessions WHERE sid = $1`, sid)
	return tag.RowsAffected(), err
}

func (s *Store) DeleteSessionsBySubject(ctx context.Context, issuer, subject string) (int64, error) {
	tag, err := s.pool.Exec(ctx, `
		DELETE FROM sessions WHERE user_id = (SELECT id FROM users WHERE issuer = $1 AND subject = $2)`, issuer, subject)
	return tag.RowsAffected(), err
}

func (s *Store) DeleteSession(ctx context.Context, tokenHash []byte) error {
	_, err := s.pool.Exec(ctx, `DELETE FROM sessions WHERE token_hash = $1`, tokenHash)
	return err
}

func (s *Store) DeleteExpiredSessions(ctx context.Context) (int64, error) {
	tag, err := s.pool.Exec(ctx, `DELETE FROM sessions WHERE expires_at <= now()`)
	return tag.RowsAffected(), err
}

// ---------------------------------------------------------- preferences

func (s *Store) GetPreferences(ctx context.Context, userID uuid.UUID) (*domain.Preferences, error) {
	p := &domain.Preferences{UserID: userID, DefaultFormat: domain.FormatPDF, NotifyEmail: true, NotifyExports: true}
	var format string
	err := s.pool.QueryRow(ctx, `
		SELECT encrypted_pat, pat_updated_at, pat_expires_at, default_format, notify_email, notify_exports, teams_webhook
		  FROM user_preferences WHERE user_id = $1`, userID,
	).Scan(&p.EncryptedPAT, &p.PATUpdatedAt, &p.PATExpiresAt, &format, &p.NotifyEmail, &p.NotifyExports, &p.EncryptedTeamsWebhook)
	if errors.Is(err, pgx.ErrNoRows) {
		return p, nil
	}
	if err != nil {
		return nil, err
	}
	p.DefaultFormat = domain.Format(format)
	return p, nil
}

// SetPAT stores (encryptedPAT != nil) or clears (nil) the user's token, with
// its expiry when Confluence reported one.
func (s *Store) SetPAT(ctx context.Context, userID uuid.UUID, encryptedPAT []byte, expiresAt *time.Time) error {
	_, err := s.pool.Exec(ctx, `
		INSERT INTO user_preferences (user_id, encrypted_pat, pat_updated_at, pat_expires_at)
		VALUES ($1, $2, CASE WHEN $2::bytea IS NULL THEN NULL ELSE now() END, $3)
		ON CONFLICT (user_id) DO UPDATE
		   SET encrypted_pat = EXCLUDED.encrypted_pat, pat_updated_at = EXCLUDED.pat_updated_at,
		       pat_expires_at = EXCLUDED.pat_expires_at, pat_expiry_notified_at = NULL, updated_at = now()`,
		userID, encryptedPAT, expiresAt)
	return err
}

// EncryptedPAT is a stored token, as read for re-encryption.
type EncryptedPAT struct {
	UserID    uuid.UUID
	Encrypted []byte
}

// PATsNotUnderKey returns tokens that were not encrypted with keyID, i.e. not
// in the v2 layout naming that key (see internal/crypto), for users ordered
// after the cursor `after`.
func (s *Store) PATsNotUnderKey(ctx context.Context, keyID string, after uuid.UUID, limit int) ([]EncryptedPAT, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT user_id, encrypted_pat FROM user_preferences
		 WHERE encrypted_pat IS NOT NULL AND user_id > $3
		   AND NOT (get_byte(encrypted_pat, 0) = 2
		            AND substring(encrypted_pat FROM 3 FOR get_byte(encrypted_pat, 1)) = convert_to($1, 'UTF8'))
		 ORDER BY user_id LIMIT $2`, keyID, limit, after)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (EncryptedPAT, error) {
		var p EncryptedPAT
		err := row.Scan(&p.UserID, &p.Encrypted)
		return p, err
	})
}

// ReplacePAT swaps a stored token for its re-encrypted form, unless the user
// changed it in the meantime. It reports whether the row was updated.
func (s *Store) ReplacePAT(ctx context.Context, userID uuid.UUID, old, replacement []byte) (bool, error) {
	tag, err := s.pool.Exec(ctx, `UPDATE user_preferences SET encrypted_pat = $3 WHERE user_id = $1 AND encrypted_pat = $2`,
		userID, old, replacement)
	return tag.RowsAffected() == 1, err
}

func (s *Store) SetDefaultFormat(ctx context.Context, userID uuid.UUID, f domain.Format) error {
	_, err := s.pool.Exec(ctx, `
		INSERT INTO user_preferences (user_id, default_format) VALUES ($1, $2)
		ON CONFLICT (user_id) DO UPDATE SET default_format = EXCLUDED.default_format, updated_at = now()`,
		userID, string(f))
	return err
}

// -------------------------------------------------------------- exports

const exportColumns = `id, user_id, root_page_id, root_title, format, include_children, classification, status,
	attempts, max_attempts, error, pages_done, pages_total, file_key, file_size,
	created_at, started_at, finished_at, expires_at`

func scanExport(row pgx.Row) (*domain.Export, error) {
	e := &domain.Export{}
	var format, status string
	err := row.Scan(&e.ID, &e.UserID, &e.RootPageID, &e.RootTitle, &format, &e.IncludeChildren, &e.Classification, &status,
		&e.Attempts, &e.MaxAttempts, &e.Error, &e.PagesDone, &e.PagesTotal, &e.FileKey, &e.FileSize,
		&e.CreatedAt, &e.StartedAt, &e.FinishedAt, &e.ExpiresAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, domain.ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	e.Format, e.Status = domain.Format(format), domain.ExportStatus(status)
	return e, nil
}

// CreateExport enqueues an export unless the user already has maxActive
// queued or running exports.
func (s *Store) CreateExport(ctx context.Context, e *domain.Export, maxActive int) error {
	return pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		// Serialise concurrent creations for the same user so the check below is race-free.
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1::text, 0))`, e.UserID); err != nil {
			return err
		}
		var active int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM exports WHERE user_id = $1 AND status IN ('queued', 'running')`,
			e.UserID).Scan(&active); err != nil {
			return err
		}
		if active >= maxActive {
			return domain.ErrTooManyActive
		}
		row := tx.QueryRow(ctx, `
			INSERT INTO exports (id, user_id, root_page_id, root_title, format, include_children, classification, status, max_attempts)
			VALUES ($1, $2, $3, $4, $5, $6, $7, 'queued', $8)
			RETURNING `+exportColumns,
			e.ID, e.UserID, e.RootPageID, e.RootTitle, string(e.Format), e.IncludeChildren, e.Classification, e.MaxAttempts)
		created, err := scanExport(row)
		if err != nil {
			return err
		}
		*e = *created
		return nil
	})
}

func (s *Store) ListExports(ctx context.Context, userID uuid.UUID, limit int) ([]*domain.Export, error) {
	rows, err := s.pool.Query(ctx, `SELECT `+exportColumns+` FROM exports WHERE user_id = $1 ORDER BY created_at DESC LIMIT $2`,
		userID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*domain.Export
	for rows.Next() {
		e, err := scanExport(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// GetExport returns an export owned by userID (ownership is part of the query
// so another user's export is indistinguishable from a missing one).
func (s *Store) GetExport(ctx context.Context, id, userID uuid.UUID) (*domain.Export, error) {
	return scanExport(s.pool.QueryRow(ctx, `SELECT `+exportColumns+` FROM exports WHERE id = $1 AND user_id = $2`, id, userID))
}

// DeleteExport removes an export and returns it so the caller can delete the
// file. Deleting a running export cancels it: the worker loses its lease.
func (s *Store) DeleteExport(ctx context.Context, id, userID uuid.UUID) (*domain.Export, error) {
	return scanExport(s.pool.QueryRow(ctx, `DELETE FROM exports WHERE id = $1 AND user_id = $2 RETURNING `+exportColumns, id, userID))
}

// ClaimNext atomically takes the oldest runnable job (queued, or running with
// an expired lease) and leases it to workerID.
func (s *Store) ClaimNext(ctx context.Context, workerID string, lease time.Duration) (*domain.Export, error) {
	return scanExport(s.pool.QueryRow(ctx, `
		WITH next AS (
			SELECT id FROM exports
			 WHERE (status = 'queued' AND run_after <= now())
			    OR (status = 'running' AND locked_until < now())
			 ORDER BY created_at
			 LIMIT 1
			 FOR UPDATE SKIP LOCKED
		)
		UPDATE exports e
		   SET status = 'running', attempts = e.attempts + 1, locked_by = $1,
		       locked_until = now() + make_interval(secs => $2), started_at = COALESCE(e.started_at, now())
		  FROM next WHERE e.id = next.id
		RETURNING `+prefixed("e", exportColumns),
		workerID, lease.Seconds()))
}

// ErrLeaseLost means the job was deleted or reclaimed by another worker.
var ErrLeaseLost = errors.New("job lease lost")

func affectOne(tag interface{ RowsAffected() int64 }, err error) error {
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrLeaseLost
	}
	return nil
}

// Heartbeat extends the lease and records progress.
func (s *Store) Heartbeat(ctx context.Context, id uuid.UUID, workerID string, lease time.Duration, pagesDone, pagesTotal int) error {
	return affectOne(s.pool.Exec(ctx, `
		UPDATE exports SET locked_until = now() + make_interval(secs => $3), pages_done = $4, pages_total = $5
		 WHERE id = $1 AND locked_by = $2 AND status = 'running'`, id, workerID, lease.Seconds(), pagesDone, pagesTotal))
}

func (s *Store) CompleteExport(ctx context.Context, id uuid.UUID, workerID, fileKey string, size int64, pages int, retention time.Duration) error {
	return affectOne(s.pool.Exec(ctx, `
		UPDATE exports SET status = 'succeeded', file_key = $3, file_size = $4, pages_done = $5, pages_total = $5,
		       error = '', finished_at = now(), expires_at = now() + make_interval(secs => $6),
		       locked_by = NULL, locked_until = NULL
		 WHERE id = $1 AND locked_by = $2 AND status = 'running'`, id, workerID, fileKey, size, pages, retention.Seconds()))
}

// FailExport re-queues the job after retryDelay when retry is true and
// attempts remain, otherwise marks it failed (kept visible for retention).
func (s *Store) FailExport(ctx context.Context, id uuid.UUID, workerID, message string, retry bool, retryDelay, retention time.Duration) error {
	return affectOne(s.pool.Exec(ctx, `
		UPDATE exports
		   SET status = CASE WHEN $4 AND attempts < max_attempts THEN 'queued' ELSE 'failed' END,
		       run_after = now() + make_interval(secs => $5),
		       finished_at = CASE WHEN $4 AND attempts < max_attempts THEN NULL ELSE now() END,
		       expires_at = CASE WHEN $4 AND attempts < max_attempts THEN NULL ELSE now() + make_interval(secs => $6) END,
		       error = $3, locked_by = NULL, locked_until = NULL
		 WHERE id = $1 AND locked_by = $2 AND status = 'running'`,
		id, workerID, message, retry, retryDelay.Seconds(), retention.Seconds()))
}

// ReleaseExport puts a running job back in the queue without consuming an
// attempt (used on graceful shutdown).
func (s *Store) ReleaseExport(ctx context.Context, id uuid.UUID, workerID string) error {
	return affectOne(s.pool.Exec(ctx, `
		UPDATE exports SET status = 'queued', attempts = GREATEST(attempts - 1, 0), run_after = now(),
		       locked_by = NULL, locked_until = NULL
		 WHERE id = $1 AND locked_by = $2 AND status = 'running'`, id, workerID))
}

type ExpiredExport struct {
	ID      uuid.UUID
	FileKey string
}

// ListExpired returns finished exports whose retention period is over.
func (s *Store) ListExpired(ctx context.Context, limit int) ([]ExpiredExport, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT id, file_key FROM exports
		 WHERE status IN ('succeeded', 'failed') AND expires_at <= now()
		 ORDER BY expires_at LIMIT $1`, limit)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (ExpiredExport, error) {
		var e ExpiredExport
		err := r.Scan(&e.ID, &e.FileKey)
		return e, err
	})
}

func (s *Store) MarkExpired(ctx context.Context, id uuid.UUID) error {
	_, err := s.pool.Exec(ctx, `UPDATE exports SET status = 'expired', file_key = '', file_size = 0 WHERE id = $1`, id)
	return err
}

// PurgeExpired deletes history rows of exports expired for longer than keep.
func (s *Store) PurgeExpired(ctx context.Context, keep time.Duration) (int64, error) {
	tag, err := s.pool.Exec(ctx, `DELETE FROM exports WHERE status = 'expired' AND expires_at < now() - make_interval(secs => $1)`,
		keep.Seconds())
	return tag.RowsAffected(), err
}

func (s *Store) QueueStats(ctx context.Context) (domain.QueueStats, error) {
	var st domain.QueueStats
	err := s.pool.QueryRow(ctx, `
		SELECT count(*) FILTER (WHERE status = 'queued'), count(*) FILTER (WHERE status = 'running')
		  FROM exports WHERE status IN ('queued', 'running')`).Scan(&st.Queued, &st.Running)
	return st, err
}

// prefixed qualifies a comma-separated column list with a table alias.
func prefixed(alias, cols string) string {
	parts := strings.Split(cols, ",")
	for i, c := range parts {
		parts[i] = alias + "." + strings.TrimSpace(c)
	}
	return strings.Join(parts, ", ")
}
