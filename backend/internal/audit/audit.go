// Package audit records the security audit trail: logins, token changes,
// exports, downloads and administrative actions.
//
// Every event is written twice: to the audit_events table, which the admin
// console reads, and as a structured log line tagged event.category=audit,
// which the log pipeline ships to the SIEM. The log line is emitted first and
// independently of the database, so a database outage never leaves a gap in
// the SIEM copy.
package audit

import (
	"context"
	"log/slog"
	"time"

	"github.com/google/uuid"

	"github.com/ffurlanetto/confluence-to-doc/backend/internal/domain"
)

// Actions recorded in the audit trail. They are part of the contract with the
// SIEM: rename one only together with the detection rules that use it.
const (
	ActionLogin  = "auth.login"
	ActionLogout = "auth.logout"
	// ActionSessionRevoked and ActionBackchannelLogout end sessions on the
	// identity provider's word (see internal/auth/lifecycle.go).
	ActionSessionRevoked    = "auth.session_revoked"
	ActionBackchannelLogout = "auth.backchannel_logout"
	ActionPATSet            = "pat.set"
	ActionPATDelete         = "pat.delete"
	ActionPreferences       = "preferences.update"
	ActionExportCreate      = "export.create"
	ActionExportDelete      = "export.delete"
	ActionExportDownload    = "export.download"
	ActionExportComplete    = "export.complete"
	ActionExportFail        = "export.fail"
	ActionExportExpire      = "export.expire"
	ActionAdminAccess       = "admin.access"
	ActionAuditRead         = "admin.audit.read"
	ActionKeyRotation       = "keys.rotate"
	ActionAccountDelete     = "account.delete"
	ActionAccountPurge      = "account.purge"
	ActionTeamsSet          = "notifications.teams_set"
	ActionTeamsDelete       = "notifications.teams_delete"
)

// Target types.
const (
	TargetUser   = "user"
	TargetExport = "export"
)

// maxUserAgent bounds a client-controlled header stored with every event.
const maxUserAgent = 512

// Store persists events.
type Store interface {
	InsertAuditEvent(ctx context.Context, e domain.AuditEvent) error
}

// Recorder writes audit events to the log and the database.
type Recorder struct {
	store Store
	log   *slog.Logger
	now   func() time.Time
}

// New returns a recorder; a nil logger means slog.Default().
func New(store Store, logger *slog.Logger) *Recorder {
	if logger == nil {
		logger = slog.Default()
	}
	return &Recorder{store: store, log: logger, now: time.Now}
}

// Record completes the event (id, time, request metadata) and writes it. It
// never fails the caller: auditing must not turn into a denial of service,
// and a write the database refuses is reported at error level with the full
// event, so the log copy stays complete.
func (r *Recorder) Record(ctx context.Context, e domain.AuditEvent) {
	if e.ID == uuid.Nil {
		e.ID = uuid.Must(uuid.NewV7())
	}
	if e.OccurredAt.IsZero() {
		e.OccurredAt = r.now().UTC()
	}
	if e.Outcome == "" {
		e.Outcome = domain.AuditSuccess
	}
	info := requestFrom(ctx)
	if e.ClientIP == "" {
		e.ClientIP = info.ClientIP
	}
	if e.UserAgent == "" {
		e.UserAgent = info.UserAgent
	}
	if len(e.UserAgent) > maxUserAgent {
		e.UserAgent = e.UserAgent[:maxUserAgent]
	}
	if e.RequestID == "" {
		e.RequestID = info.RequestID
	}

	attrs := logAttrs(e)
	r.log.LogAttrs(ctx, slog.LevelInfo, "audit", attrs...)
	if r.store == nil {
		return
	}
	// The write must happen even if the request was cancelled right after
	// the action it records.
	if err := r.store.InsertAuditEvent(context.WithoutCancel(ctx), e); err != nil {
		r.log.LogAttrs(ctx, slog.LevelError, "audit: storing event failed",
			append(attrs, slog.String("error.message", err.Error()))...)
	}
}

// logAttrs uses Elastic Common Schema names, which most SIEMs map natively.
func logAttrs(e domain.AuditEvent) []slog.Attr {
	attrs := []slog.Attr{
		slog.String("event.category", "audit"),
		slog.String("event.id", e.ID.String()),
		slog.String("event.action", e.Action),
		slog.String("event.outcome", string(e.Outcome)),
		slog.Time("event.created", e.OccurredAt),
	}
	if e.ActorID != nil {
		attrs = append(attrs, slog.String("user.id", e.ActorID.String()))
	}
	if e.ActorEmail != "" {
		attrs = append(attrs, slog.String("user.email", e.ActorEmail))
	}
	if e.TargetType != "" {
		attrs = append(attrs, slog.String("target.type", e.TargetType), slog.String("target.id", e.TargetID))
	}
	if e.ClientIP != "" {
		attrs = append(attrs, slog.String("source.ip", e.ClientIP))
	}
	if e.UserAgent != "" {
		attrs = append(attrs, slog.String("user_agent.original", e.UserAgent))
	}
	if e.RequestID != "" {
		attrs = append(attrs, slog.String("http.request.id", e.RequestID))
	}
	if len(e.Details) > 0 {
		attrs = append(attrs, slog.Any("audit.details", e.Details))
	}
	return attrs
}

// Actor fills the actor fields of an event from a user (nil = system).
func Actor(u *domain.User) (id *uuid.UUID, email string) {
	if u == nil {
		return nil, ""
	}
	uid := u.ID
	return &uid, u.Email
}

// RequestInfo is the request metadata attached to events recorded while
// serving it.
type RequestInfo struct {
	ClientIP  string
	UserAgent string
	RequestID string
}

type ctxKey struct{}

// WithRequest attaches request metadata to ctx.
func WithRequest(ctx context.Context, info RequestInfo) context.Context {
	return context.WithValue(ctx, ctxKey{}, info)
}

// RequestFrom returns the request metadata attached by WithRequest.
func RequestFrom(ctx context.Context) RequestInfo { return requestFrom(ctx) }

func requestFrom(ctx context.Context) RequestInfo {
	info, _ := ctx.Value(ctxKey{}).(RequestInfo)
	return info
}

// Discard drops every event; for tests and tools that need no audit trail.
type Discard struct{}

func (Discard) Record(context.Context, domain.AuditEvent) {}
