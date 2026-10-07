package audit_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/ffurlanetto/confluence-to-doc/backend/internal/audit"
	"github.com/ffurlanetto/confluence-to-doc/backend/internal/domain"
)

type memStore struct {
	events []domain.AuditEvent
	err    error
}

func (m *memStore) InsertAuditEvent(_ context.Context, e domain.AuditEvent) error {
	if m.err != nil {
		return m.err
	}
	m.events = append(m.events, e)
	return nil
}

func logLines(t *testing.T, buf *bytes.Buffer) []map[string]any {
	t.Helper()
	var out []map[string]any
	for _, line := range strings.Split(strings.TrimSpace(buf.String()), "\n") {
		var m map[string]any
		if err := json.Unmarshal([]byte(line), &m); err != nil {
			t.Fatalf("log line is not JSON: %q", line)
		}
		out = append(out, m)
	}
	return out
}

func TestRecordWritesStoreAndLog(t *testing.T) {
	var buf bytes.Buffer
	store := &memStore{}
	r := audit.New(store, slog.New(slog.NewJSONHandler(&buf, nil)))

	actor := uuid.New()
	ctx := audit.WithRequest(context.Background(), audit.RequestInfo{
		ClientIP: "203.0.113.7", UserAgent: strings.Repeat("x", 2000), RequestID: "req-1",
	})
	r.Record(ctx, domain.AuditEvent{
		ActorID: &actor, ActorEmail: "alice@example.com", Action: audit.ActionExportDownload,
		TargetType: audit.TargetExport, TargetID: "e-1", Details: map[string]any{"format": "pdf"},
	})

	if len(store.events) != 1 {
		t.Fatalf("stored %d events", len(store.events))
	}
	e := store.events[0]
	if e.ID == uuid.Nil || e.OccurredAt.IsZero() || e.Outcome != domain.AuditSuccess {
		t.Errorf("event not completed: %+v", e)
	}
	if e.ClientIP != "203.0.113.7" || e.RequestID != "req-1" || len(e.UserAgent) != 512 {
		t.Errorf("request metadata: ip=%q req=%q ua=%d", e.ClientIP, e.RequestID, len(e.UserAgent))
	}

	lines := logLines(t, &buf)
	if len(lines) != 1 {
		t.Fatalf("want one log line, got %d", len(lines))
	}
	want := map[string]any{
		"event.category": "audit", "event.action": "export.download", "event.outcome": "success",
		"user.email": "alice@example.com", "user.id": actor.String(), "source.ip": "203.0.113.7",
		"target.id": "e-1", "http.request.id": "req-1",
	}
	for k, v := range want {
		if lines[0][k] != v {
			t.Errorf("log %s = %v, want %v", k, lines[0][k], v)
		}
	}
}

func TestRecordStillLogsWhenTheStoreFails(t *testing.T) {
	var buf bytes.Buffer
	r := audit.New(&memStore{err: errors.New("db down")}, slog.New(slog.NewJSONHandler(&buf, nil)))
	r.Record(context.Background(), domain.AuditEvent{Action: audit.ActionLogin})

	lines := logLines(t, &buf)
	if len(lines) != 2 {
		t.Fatalf("want the event and the error, got %d lines", len(lines))
	}
	if lines[1]["level"] != "ERROR" || lines[1]["event.action"] != "auth.login" || lines[1]["error.message"] != "db down" {
		t.Fatalf("failure line does not carry the event: %v", lines[1])
	}
}
