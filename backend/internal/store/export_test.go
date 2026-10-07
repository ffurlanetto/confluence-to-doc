package store

import "github.com/jackc/pgx/v5/pgxpool"

// Pool exposes the connection pool to tests that check database-level rules.
func (s *Store) Pool() *pgxpool.Pool { return s.pool }
