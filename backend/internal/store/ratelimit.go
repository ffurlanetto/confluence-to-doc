package store

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
)

// TakeToken takes one token from the named bucket, refilled at rate tokens per
// second up to burst. The row lock makes it atomic across instances; the
// bucket is created full on first use.
func (s *Store) TakeToken(ctx context.Context, name string, rate, burst float64) (bool, time.Duration, error) {
	var available float64
	err := s.pool.QueryRow(ctx, `
		WITH cur AS (
			SELECT LEAST($3::float8, tokens + EXTRACT(EPOCH FROM clock_timestamp() - updated_at)::float8 * $2::float8) AS t
			  FROM rate_limits WHERE name = $1 FOR UPDATE)
		UPDATE rate_limits r
		   SET tokens = CASE WHEN cur.t >= 1 THEN cur.t - 1 ELSE cur.t END, updated_at = clock_timestamp()
		  FROM cur WHERE r.name = $1
		RETURNING cur.t`, name, rate, burst).Scan(&available)
	if errors.Is(err, pgx.ErrNoRows) {
		if _, err := s.pool.Exec(ctx, `INSERT INTO rate_limits (name, tokens) VALUES ($1, $2) ON CONFLICT DO NOTHING`,
			name, burst); err != nil {
			return false, 0, err
		}
		return s.TakeToken(ctx, name, rate, burst)
	}
	if err != nil {
		return false, 0, err
	}
	if available >= 1 {
		return true, 0, nil
	}
	return false, time.Duration((1 - available) / rate * float64(time.Second)), nil
}
