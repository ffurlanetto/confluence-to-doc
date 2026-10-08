// Package domain holds the core business types shared by the storage,
// HTTP and worker layers. It has no dependency on infrastructure.
package domain

import (
	"errors"
	"time"

	"github.com/google/uuid"
)

var (
	ErrNotFound              = errors.New("not found")
	ErrTooManyActive         = errors.New("too many active exports")
	ErrPATMissing            = errors.New("confluence personal access token not configured")
	ErrExportNotReady        = errors.New("export not ready")
	ErrExportExpired         = errors.New("export expired")
	ErrInvalidFormat         = errors.New("invalid export format")
	ErrInvalidPageID         = errors.New("invalid page id")
	ErrExportNotDeletable    = errors.New("export is being processed and cannot be deleted")
	ErrInvalidClassification = errors.New("unknown document classification")
	// ErrPATUnreadable means no key of the ring can decrypt the stored token:
	// the key that encrypted it was removed. The user must enter it again.
	ErrPATUnreadable = errors.New("confluence personal access token cannot be decrypted")
)

type User struct {
	ID      uuid.UUID
	Issuer  string
	Subject string
	Email   string
	Name    string
	// IsAdmin is derived from the identity provider's group claim at each
	// login (OIDC_ADMIN_GROUPS); it is never set through the API.
	IsAdmin   bool
	CreatedAt time.Time
	// LastActiveAt is the last sign-in, or the creation for an account that
	// never signed in again. Only filled where it is needed.
	LastActiveAt time.Time
}

type Session struct {
	UserID    uuid.UUID
	ExpiresAt time.Time
	// SID is the identity provider's session id, which a back-channel
	// logout names; empty when the provider gives none.
	SID string
	// RefreshToken is the encrypted OAuth refresh token used to re-check the
	// account with the provider at RevalidateAt (nil: no re-check possible).
	RefreshToken []byte
	RevalidateAt *time.Time
}

type Preferences struct {
	UserID       uuid.UUID
	EncryptedPAT []byte
	PATUpdatedAt *time.Time
	// PATExpiresAt is when the token stops working, if Confluence said.
	PATExpiresAt  *time.Time
	DefaultFormat Format
	// NotifyEmail sends notifications by email (when the server can);
	// NotifyExports includes finished exports, not only account notices.
	NotifyEmail   bool
	NotifyExports bool
	// EncryptedTeamsWebhook is the user's Teams workflow URL, encrypted.
	EncryptedTeamsWebhook []byte
}

// HasPAT reports whether the user configured a Confluence token.
func (p Preferences) HasPAT() bool { return len(p.EncryptedPAT) > 0 }

type Format string

const (
	FormatPDF  Format = "pdf"
	FormatDOCX Format = "docx"
)

func ParseFormat(s string) (Format, error) {
	switch Format(s) {
	case FormatPDF, FormatDOCX:
		return Format(s), nil
	}
	return "", ErrInvalidFormat
}

// Extension returns the file extension (without dot) of the format.
func (f Format) Extension() string { return string(f) }

// ContentType returns the MIME type of the format.
func (f Format) ContentType() string {
	if f == FormatDOCX {
		return "application/vnd.openxmlformats-officedocument.wordprocessingml.document"
	}
	return "application/pdf"
}

// Classification is a document sensitivity level offered when exporting.
type Classification struct {
	Label string
	// Watermark sets the label diagonally across every page.
	Watermark bool
}

type ExportStatus string

const (
	StatusQueued    ExportStatus = "queued"
	StatusRunning   ExportStatus = "running"
	StatusSucceeded ExportStatus = "succeeded"
	StatusFailed    ExportStatus = "failed"
	StatusExpired   ExportStatus = "expired"
)

// Active reports whether the export still consumes queue capacity.
func (s ExportStatus) Active() bool { return s == StatusQueued || s == StatusRunning }

type Export struct {
	ID              uuid.UUID
	UserID          uuid.UUID
	RootPageID      string
	RootTitle       string
	Format          Format
	IncludeChildren bool
	// Classification is the label chosen when the export was requested,
	// empty when none was.
	Classification string
	Status         ExportStatus
	Attempts       int
	MaxAttempts    int
	Error          string
	PagesDone      int
	PagesTotal     int
	FileKey        string
	FileSize       int64
	CreatedAt      time.Time
	StartedAt      *time.Time
	FinishedAt     *time.Time
	ExpiresAt      *time.Time
}

// Downloadable reports whether the file can be served at instant now.
func (e *Export) Downloadable(now time.Time) error {
	switch {
	case e.Status == StatusExpired || (e.ExpiresAt != nil && !now.Before(*e.ExpiresAt)):
		return ErrExportExpired
	case e.Status != StatusSucceeded || e.FileKey == "":
		return ErrExportNotReady
	}
	return nil
}

// QueueStats is a snapshot of the queue used for metrics and backpressure.
type QueueStats struct {
	Queued  int
	Running int
}

// AuditOutcome tells whether an audited action happened.
type AuditOutcome string

const (
	AuditSuccess AuditOutcome = "success"
	AuditFailure AuditOutcome = "failure"
	// AuditDenied is an action refused for lack of permission.
	AuditDenied AuditOutcome = "denied"
)

// AuditEvent is one entry of the security audit trail: who did what, to
// what, from where, and whether it worked. Events are append-only.
type AuditEvent struct {
	ID         uuid.UUID
	OccurredAt time.Time
	// ActorID is nil for actions performed by the system (e.g. retention).
	ActorID    *uuid.UUID
	ActorEmail string
	Action     string
	Outcome    AuditOutcome
	TargetType string
	TargetID   string
	ClientIP   string
	UserAgent  string
	RequestID  string
	// Details holds action-specific context. Never put secrets or document
	// contents in it.
	Details map[string]any
}

// AuditFilter selects audit events, newest first. Zero values match all.
type AuditFilter struct {
	ActorID *uuid.UUID
	// Actor matches a substring of the actor's email, case-insensitively.
	Actor  string
	Action string
	From   *time.Time
	To     *time.Time
	// Before is the pagination cursor: only events older than this id.
	Before *uuid.UUID
	Limit  int
}

// NotificationKind identifies what a notification is about.
type NotificationKind string

const (
	NotifyExportSucceeded NotificationKind = "export.succeeded"
	NotifyExportFailed    NotificationKind = "export.failed"
	NotifyPATExpiring     NotificationKind = "pat.expiring"
	NotifyAccountInactive NotificationKind = "account.inactive"
)

// Optional reports whether the user may turn the notification off: export
// outcomes are, notices about their account are not.
func (k NotificationKind) Optional() bool {
	return k == NotifyExportSucceeded || k == NotifyExportFailed
}

// Notification is a message to a user, shown in the application and
// delivered on the channels they chose.
type Notification struct {
	ID        uuid.UUID
	UserID    uuid.UUID
	Kind      NotificationKind
	Title     string
	Body      string
	Link      string
	CreatedAt time.Time
	ReadAt    *time.Time
}

// Channel is an external way of delivering a notification.
type Channel string

const (
	ChannelEmail Channel = "email"
	ChannelTeams Channel = "teams"
)

// Delivery is a notification to send on one channel, with what sending needs.
type Delivery struct {
	Notification Notification
	Channel      Channel
	Attempts     int
	Email        string
	// EncryptedTeamsWebhook is read at sending time: a URL removed since the
	// notification was created is not used.
	EncryptedTeamsWebhook []byte
}
