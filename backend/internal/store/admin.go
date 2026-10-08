package store

import (
	"context"
	"crypto/sha256"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/ffurlanetto/confluence-to-doc/backend/internal/audit"
	"github.com/ffurlanetto/confluence-to-doc/backend/internal/domain"
)

// ------------------------------------------------------------------ queue

// ListAllExports returns the exports of every user in the given statuses
// (all when none), newest first, with their owner's email.
func (s *Store) ListAllExports(ctx context.Context, statuses []domain.ExportStatus, limit int) ([]domain.AdminExport, error) {
	names := make([]string, 0, len(statuses))
	for _, st := range statuses {
		names = append(names, string(st))
	}
	rows, err := s.pool.Query(ctx, `
		SELECT `+prefixed("e", exportColumns)+`, COALESCE(u.email, '')
		  FROM exports e LEFT JOIN users u ON u.id = e.user_id
		 WHERE cardinality($1::text[]) = 0 OR e.status = ANY($1)
		 ORDER BY e.created_at DESC LIMIT $2`, names, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []domain.AdminExport
	for rows.Next() {
		var a domain.AdminExport
		var format, status string
		e := &a.Export
		if err := rows.Scan(&e.ID, &e.UserID, &e.RootPageID, &e.RootTitle, &format, &e.IncludeChildren, &e.Classification, &status,
			&e.Attempts, &e.MaxAttempts, &e.Error, &e.PagesDone, &e.PagesTotal, &e.FileKey, &e.FileSize,
			&e.CreatedAt, &e.StartedAt, &e.FinishedAt, &e.ExpiresAt, &a.OwnerEmail); err != nil {
			return nil, err
		}
		e.Format, e.Status = domain.Format(format), domain.ExportStatus(status)
		out = append(out, a)
	}
	return out, rows.Err()
}

// CancelExport stops a queued or running export: it is marked failed with
// message, and a worker running it loses its lease at its next heartbeat.
func (s *Store) CancelExport(ctx context.Context, id uuid.UUID, message string, retention time.Duration) (*domain.Export, error) {
	e, err := scanExport(s.pool.QueryRow(ctx, `
		UPDATE exports
		   SET status = 'failed', error = $2, finished_at = now(),
		       expires_at = now() + make_interval(secs => $3), locked_by = NULL, locked_until = NULL
		 WHERE id = $1 AND status IN ('queued', 'running')
		RETURNING `+exportColumns, id, message, retention.Seconds()))
	if errors.Is(err, domain.ErrNotFound) {
		return nil, s.exportStateError(ctx, id)
	}
	return e, err
}

// RetryExport puts a failed export back in the queue with fresh attempts.
func (s *Store) RetryExport(ctx context.Context, id uuid.UUID) (*domain.Export, error) {
	e, err := scanExport(s.pool.QueryRow(ctx, `
		UPDATE exports
		   SET status = 'queued', attempts = 0, error = '', run_after = now(), pages_done = 0, pages_total = 0,
		       started_at = NULL, finished_at = NULL, expires_at = NULL
		 WHERE id = $1 AND status = 'failed'
		RETURNING `+exportColumns, id))
	if errors.Is(err, domain.ErrNotFound) {
		return nil, s.exportStateError(ctx, id)
	}
	return e, err
}

// exportStateError tells a missing export from one in the wrong state.
func (s *Store) exportStateError(ctx context.Context, id uuid.UUID) error {
	var exists bool
	if err := s.pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM exports WHERE id = $1)`, id).Scan(&exists); err != nil {
		return err
	}
	if exists {
		return domain.ErrExportState
	}
	return domain.ErrNotFound
}

// ------------------------------------------------------------------ users

// ListUsers returns users whose email or name contains query (all when
// empty), most recently active first.
func (s *Store) ListUsers(ctx context.Context, query string, limit int) ([]domain.UserSummary, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT u.id, u.issuer, u.subject, u.email, u.name, u.is_admin, u.created_at,
		       COALESCE(u.last_login_at, u.created_at), u.blocked_at, u.blocked_reason,
		       count(e.id) FILTER (WHERE e.status IN ('queued', 'running')), count(e.id)
		  FROM users u LEFT JOIN exports e ON e.user_id = u.id
		 WHERE $1 = '' OR u.email ILIKE '%' || $1 || '%' OR u.name ILIKE '%' || $1 || '%'
		 GROUP BY u.id
		 ORDER BY COALESCE(u.last_login_at, u.created_at) DESC
		 LIMIT $2`, escapeLike(query), limit)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (domain.UserSummary, error) {
		var u domain.UserSummary
		err := row.Scan(&u.ID, &u.Issuer, &u.Subject, &u.Email, &u.Name, &u.IsAdmin, &u.CreatedAt,
			&u.LastActiveAt, &u.BlockedAt, &u.BlockedReason, &u.ActiveExports, &u.TotalExports)
		return u, err
	})
}

// BlockUser blocks an account: its sessions are deleted and its pending
// exports cancelled with message. It returns the user and how many exports
// were cancelled.
func (s *Store) BlockUser(ctx context.Context, id uuid.UUID, reason, message string, retention time.Duration) (*domain.User, int64, error) {
	var cancelled int64
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `UPDATE users SET blocked_at = COALESCE(blocked_at, now()), blocked_reason = $2 WHERE id = $1`,
			id, reason)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return domain.ErrNotFound
		}
		if _, err := tx.Exec(ctx, `DELETE FROM sessions WHERE user_id = $1`, id); err != nil {
			return err
		}
		tag, err = tx.Exec(ctx, `
			UPDATE exports
			   SET status = 'failed', error = $2, finished_at = now(),
			       expires_at = now() + make_interval(secs => $3), locked_by = NULL, locked_until = NULL
			 WHERE user_id = $1 AND status IN ('queued', 'running')`, id, message, retention.Seconds())
		cancelled = tag.RowsAffected()
		return err
	})
	if err != nil {
		return nil, 0, err
	}
	u, err := s.GetUser(ctx, id)
	return u, cancelled, err
}

// UnblockUser lets the account sign in again.
func (s *Store) UnblockUser(ctx context.Context, id uuid.UUID) (*domain.User, error) {
	tag, err := s.pool.Exec(ctx, `UPDATE users SET blocked_at = NULL, blocked_reason = '' WHERE id = $1`, id)
	if err != nil {
		return nil, err
	}
	if tag.RowsAffected() == 0 {
		return nil, domain.ErrNotFound
	}
	return s.GetUser(ctx, id)
}

// ------------------------------------------------------------------ usage

// usageTopUsers is how many of the most active users Usage returns.
const usageTopUsers = 10

// Usage summarises the exports finished since from. It reads the audit
// trail, which outlives the exports themselves (kept one year by default).
func (s *Store) Usage(ctx context.Context, from time.Time) (*domain.Usage, error) {
	u := &domain.Usage{From: from, ByFormat: map[domain.Format]int{}}
	done, failed := audit.ActionExportComplete, audit.ActionExportFail
	err := s.pool.QueryRow(ctx, `
		SELECT count(*) FILTER (WHERE action = $2), count(*) FILTER (WHERE action = $3),
		       COALESCE(sum((details->>'pages')::bigint) FILTER (WHERE action = $2), 0),
		       COALESCE(sum((details->>'size')::bigint) FILTER (WHERE action = $2), 0),
		       count(DISTINCT actor_id)
		  FROM audit_events WHERE action IN ($2, $3) AND occurred_at >= $1`, from, done, failed,
	).Scan(&u.Succeeded, &u.Failed, &u.Pages, &u.Bytes, &u.Users)
	if err != nil {
		return nil, err
	}

	rows, err := s.pool.Query(ctx, `
		SELECT details->>'format', count(*) FROM audit_events
		 WHERE action = $2 AND occurred_at >= $1 AND details ? 'format'
		 GROUP BY 1 ORDER BY 1`, from, done)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var format string
		var n int
		if err := rows.Scan(&format, &n); err != nil {
			rows.Close()
			return nil, err
		}
		u.ByFormat[domain.Format(format)] = n
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}

	rows, err = s.pool.Query(ctx, `
		SELECT date_trunc('day', occurred_at, 'UTC'),
		       count(*) FILTER (WHERE action = $2), count(*) FILTER (WHERE action = $3)
		  FROM audit_events WHERE action IN ($2, $3) AND occurred_at >= $1
		 GROUP BY 1 ORDER BY 1`, from, done, failed)
	if err != nil {
		return nil, err
	}
	if u.Daily, err = pgx.CollectRows(rows, func(row pgx.CollectableRow) (domain.DailyUsage, error) {
		var d domain.DailyUsage
		err := row.Scan(&d.Day, &d.Succeeded, &d.Failed)
		d.Day = d.Day.UTC()
		return d, err
	}); err != nil {
		return nil, err
	}

	rows, err = s.pool.Query(ctx, `
		SELECT a.actor_id, COALESCE(u.email, max(a.actor_email), ''), count(*),
		       COALESCE(sum((a.details->>'pages')::bigint), 0)
		  FROM audit_events a LEFT JOIN users u ON u.id = a.actor_id
		 WHERE a.action = $2 AND a.occurred_at >= $1 AND a.actor_id IS NOT NULL
		 GROUP BY a.actor_id, u.email
		 ORDER BY count(*) DESC, a.actor_id LIMIT $3`, from, done, usageTopUsers)
	if err != nil {
		return nil, err
	}
	if u.TopUsers, err = pgx.CollectRows(rows, func(row pgx.CollectableRow) (domain.UserUsage, error) {
		var t domain.UserUsage
		err := row.Scan(&t.UserID, &t.Email, &t.Exports, &t.Pages)
		return t, err
	}); err != nil {
		return nil, err
	}
	return u, nil
}

// --------------------------------------------------------- Word template

// DocumentTemplateVersion is the checksum of the uploaded template, cheap to
// read before every export; domain.ErrNotFound when none is uploaded.
func (s *Store) DocumentTemplateVersion(ctx context.Context) ([]byte, error) {
	var sum []byte
	err := s.pool.QueryRow(ctx, `SELECT sha256 FROM document_template WHERE id = 1`).Scan(&sum)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, domain.ErrNotFound
	}
	return sum, err
}

// GetDocumentTemplate returns the uploaded template, or domain.ErrNotFound.
func (s *Store) GetDocumentTemplate(ctx context.Context, withContent bool) (*domain.DocumentTemplate, error) {
	t := &domain.DocumentTemplate{}
	err := s.pool.QueryRow(ctx, `
		SELECT t.name, CASE WHEN $1 THEN t.content ELSE ''::bytea END, t.sha256, t.uploaded_by,
		       COALESCE(u.email, ''), t.uploaded_at
		  FROM document_template t LEFT JOIN users u ON u.id = t.uploaded_by
		 WHERE t.id = 1`, withContent,
	).Scan(&t.Name, &t.Content, &t.SHA256, &t.UploadedBy, &t.UploadedByEmail, &t.UploadedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, domain.ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return t, nil
}

// PutDocumentTemplate replaces the uploaded template.
func (s *Store) PutDocumentTemplate(ctx context.Context, name string, content []byte, uploadedBy uuid.UUID) (*domain.DocumentTemplate, error) {
	sum := sha256.Sum256(content)
	_, err := s.pool.Exec(ctx, `
		INSERT INTO document_template (id, name, content, sha256, uploaded_by, uploaded_at)
		VALUES (1, $1, $2, $3, $4, now())
		ON CONFLICT (id) DO UPDATE
		   SET name = EXCLUDED.name, content = EXCLUDED.content, sha256 = EXCLUDED.sha256,
		       uploaded_by = EXCLUDED.uploaded_by, uploaded_at = EXCLUDED.uploaded_at`,
		name, content, sum[:], uploadedBy)
	if err != nil {
		return nil, err
	}
	return s.GetDocumentTemplate(ctx, false)
}

// DeleteDocumentTemplate removes the uploaded template; documents go back to
// WORD_TEMPLATE_PATH, or the built-in styling. domain.ErrNotFound when none.
func (s *Store) DeleteDocumentTemplate(ctx context.Context) error {
	tag, err := s.pool.Exec(ctx, `DELETE FROM document_template WHERE id = 1`)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return domain.ErrNotFound
	}
	return nil
}
