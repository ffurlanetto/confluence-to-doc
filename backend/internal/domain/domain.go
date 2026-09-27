// Package domain holds the core business types shared by the storage,
// HTTP and worker layers. It has no dependency on infrastructure.
package domain

import (
	"errors"
	"time"

	"github.com/google/uuid"
)

var (
	ErrNotFound           = errors.New("not found")
	ErrTooManyActive      = errors.New("too many active exports")
	ErrPATMissing         = errors.New("confluence personal access token not configured")
	ErrExportNotReady     = errors.New("export not ready")
	ErrExportExpired      = errors.New("export expired")
	ErrInvalidFormat      = errors.New("invalid export format")
	ErrInvalidPageID      = errors.New("invalid page id")
	ErrExportNotDeletable = errors.New("export is being processed and cannot be deleted")
)

type User struct {
	ID        uuid.UUID
	Issuer    string
	Subject   string
	Email     string
	Name      string
	CreatedAt time.Time
}

type Session struct {
	UserID    uuid.UUID
	ExpiresAt time.Time
}

type Preferences struct {
	UserID        uuid.UUID
	EncryptedPAT  []byte
	PATUpdatedAt  *time.Time
	DefaultFormat Format
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
	Status          ExportStatus
	Attempts        int
	MaxAttempts     int
	Error           string
	PagesDone       int
	PagesTotal      int
	FileKey         string
	FileSize        int64
	CreatedAt       time.Time
	StartedAt       *time.Time
	FinishedAt      *time.Time
	ExpiresAt       *time.Time
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
