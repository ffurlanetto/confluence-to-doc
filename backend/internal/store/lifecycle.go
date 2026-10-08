package store

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/ffurlanetto/confluence-to-doc/backend/internal/domain"
)

// DeleteUser removes a user and, by cascade, their token, preferences,
// sessions and exports. It returns the storage keys of the files the exports
// held, which the caller deletes. A worker generating an export for the user
// loses its lease and discards what it produced.
func (s *Store) DeleteUser(ctx context.Context, id uuid.UUID) ([]string, error) {
	var keys []string
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT file_key FROM exports WHERE user_id = $1 AND file_key <> ''`, id)
		if err != nil {
			return err
		}
		keys, err = pgx.CollectRows(rows, pgx.RowTo[string])
		if err != nil {
			return err
		}
		tag, err := tx.Exec(ctx, `DELETE FROM users WHERE id = $1`, id)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return domain.ErrNotFound
		}
		return nil
	})
	return keys, err
}

const userColumns = `id, issuer, subject, email, name, is_admin, created_at, COALESCE(last_login_at, created_at)`

func scanUser(row pgx.CollectableRow) (domain.User, error) {
	var u domain.User
	err := row.Scan(&u.ID, &u.Issuer, &u.Subject, &u.Email, &u.Name, &u.IsAdmin, &u.CreatedAt, &u.LastActiveAt)
	return u, err
}

// InactiveUsers returns users whose last sign-in (or creation) is before
// cutoff and who were warned before warnedBefore, oldest first. Requiring the
// warning makes sure nobody is deleted without notice, including accounts
// that were already past the retention when it was introduced.
func (s *Store) InactiveUsers(ctx context.Context, cutoff, warnedBefore time.Time, limit int) ([]domain.User, error) {
	rows, err := s.pool.Query(ctx, `SELECT `+userColumns+` FROM users
		 WHERE COALESCE(last_login_at, created_at) < $1 AND inactivity_warned_at < $2
		 ORDER BY COALESCE(last_login_at, created_at) LIMIT $3`, cutoff, warnedBefore, limit)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, scanUser)
}

// UsersToWarn returns inactive users not yet warned of their deletion.
func (s *Store) UsersToWarn(ctx context.Context, cutoff time.Time, limit int) ([]domain.User, error) {
	rows, err := s.pool.Query(ctx, `SELECT `+userColumns+` FROM users
		 WHERE COALESCE(last_login_at, created_at) < $1 AND inactivity_warned_at IS NULL
		 ORDER BY COALESCE(last_login_at, created_at) LIMIT $2`, cutoff, limit)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, scanUser)
}

// MarkWarned records that the user was told their account will be deleted.
func (s *Store) MarkWarned(ctx context.Context, id uuid.UUID) error {
	_, err := s.pool.Exec(ctx, `UPDATE users SET inactivity_warned_at = now() WHERE id = $1`, id)
	return err
}
