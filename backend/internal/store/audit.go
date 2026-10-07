package store

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/ffurlanetto/confluence-to-doc/backend/internal/domain"
)

// maxAuditPage bounds one page of audit events.
const maxAuditPage = 500

// InsertAuditEvent appends an event to the audit trail.
func (s *Store) InsertAuditEvent(ctx context.Context, e domain.AuditEvent) error {
	details := e.Details
	if details == nil {
		details = map[string]any{}
	}
	raw, err := json.Marshal(details)
	if err != nil {
		return fmt.Errorf("encoding audit details: %w", err)
	}
	_, err = s.pool.Exec(ctx, `
		INSERT INTO audit_events (id, occurred_at, actor_id, actor_email, action, outcome,
		                          target_type, target_id, client_ip, user_agent, request_id, details)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12)`,
		e.ID, e.OccurredAt, e.ActorID, e.ActorEmail, e.Action, string(e.Outcome),
		e.TargetType, e.TargetID, e.ClientIP, e.UserAgent, e.RequestID, raw)
	return err
}

// ListAuditEvents returns the events matching f, newest first. Ids are
// UUIDv7, so ordering by id is chronological and makes a stable cursor.
func (s *Store) ListAuditEvents(ctx context.Context, f domain.AuditFilter) ([]domain.AuditEvent, error) {
	var where []string
	var args []any
	add := func(cond string, v any) {
		args = append(args, v)
		where = append(where, fmt.Sprintf(cond, len(args)))
	}
	if f.ActorID != nil {
		add("actor_id = $%d", *f.ActorID)
	}
	if f.Actor != "" {
		add("actor_email ILIKE '%%' || $%d || '%%'", escapeLike(f.Actor))
	}
	if f.Action != "" {
		add("action = $%d", f.Action)
	}
	if f.From != nil {
		add("occurred_at >= $%d", *f.From)
	}
	if f.To != nil {
		add("occurred_at < $%d", *f.To)
	}
	if f.Before != nil {
		add("id < $%d", *f.Before)
	}
	limit := f.Limit
	if limit <= 0 || limit > maxAuditPage {
		limit = maxAuditPage
	}
	q := `SELECT id, occurred_at, actor_id, actor_email, action, outcome, target_type, target_id,
	             client_ip, user_agent, request_id, details
	        FROM audit_events`
	if len(where) > 0 {
		q += " WHERE " + strings.Join(where, " AND ")
	}
	args = append(args, limit)
	q += fmt.Sprintf(" ORDER BY id DESC LIMIT $%d", len(args))

	rows, err := s.pool.Query(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (domain.AuditEvent, error) {
		var e domain.AuditEvent
		var outcome string
		var raw []byte
		err := row.Scan(&e.ID, &e.OccurredAt, &e.ActorID, &e.ActorEmail, &e.Action, &outcome,
			&e.TargetType, &e.TargetID, &e.ClientIP, &e.UserAgent, &e.RequestID, &raw)
		if err != nil {
			return e, err
		}
		e.Outcome = domain.AuditOutcome(outcome)
		if err := json.Unmarshal(raw, &e.Details); err != nil {
			return e, fmt.Errorf("decoding audit details: %w", err)
		}
		return e, nil
	})
}

// PurgeAuditEvents deletes the events older than keep (retention policy).
func (s *Store) PurgeAuditEvents(ctx context.Context, keep time.Duration) (int64, error) {
	tag, err := s.pool.Exec(ctx, `DELETE FROM audit_events WHERE occurred_at < now() - make_interval(secs => $1)`,
		keep.Seconds())
	return tag.RowsAffected(), err
}

// escapeLike makes user input match literally inside a LIKE pattern.
func escapeLike(s string) string {
	return strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(s)
}
