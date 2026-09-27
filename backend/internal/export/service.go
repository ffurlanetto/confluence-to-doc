// Package export implements the export use cases: enqueueing (API side),
// asynchronous processing (worker side) and retention (janitor).
package export

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"time"

	"github.com/google/uuid"

	"github.com/ffurlanetto/confluence-to-doc/backend/internal/confluence"
	"github.com/ffurlanetto/confluence-to-doc/backend/internal/domain"
	"github.com/ffurlanetto/confluence-to-doc/backend/internal/metrics"
	"github.com/ffurlanetto/confluence-to-doc/backend/internal/storage"
	"github.com/ffurlanetto/confluence-to-doc/backend/internal/store"
)

// Repo is the persistence contract of the export package.
type Repo interface {
	CreateExport(ctx context.Context, e *domain.Export, maxActive int) error
	ListExports(ctx context.Context, userID uuid.UUID, limit int) ([]*domain.Export, error)
	GetExport(ctx context.Context, id, userID uuid.UUID) (*domain.Export, error)
	DeleteExport(ctx context.Context, id, userID uuid.UUID) (*domain.Export, error)

	ClaimNext(ctx context.Context, workerID string, lease time.Duration) (*domain.Export, error)
	Heartbeat(ctx context.Context, id uuid.UUID, workerID string, lease time.Duration, pagesDone, pagesTotal int) error
	CompleteExport(ctx context.Context, id uuid.UUID, workerID, fileKey string, size int64, pages int, retention time.Duration) error
	FailExport(ctx context.Context, id uuid.UUID, workerID, message string, retry bool, retryDelay, retention time.Duration) error
	ReleaseExport(ctx context.Context, id uuid.UUID, workerID string) error

	ListExpired(ctx context.Context, limit int) ([]store.ExpiredExport, error)
	MarkExpired(ctx context.Context, id uuid.UUID) error
	PurgeExpired(ctx context.Context, keep time.Duration) (int64, error)
	DeleteExpiredSessions(ctx context.Context) (int64, error)
}

// ClientProvider returns a Confluence client authenticated as the user.
type ClientProvider interface {
	Client(ctx context.Context, userID uuid.UUID) (*confluence.Client, error)
}

type Limits struct {
	MaxAttempts      int
	MaxActivePerUser int
}

// Service holds the API-side use cases.
type Service struct {
	repo   Repo
	blobs  storage.BlobStore
	limits Limits
	notify func() // wakes up local workers; may be nil
	now    func() time.Time
}

func NewService(repo Repo, blobs storage.BlobStore, limits Limits, notify func()) *Service {
	return &Service{repo: repo, blobs: blobs, limits: limits, notify: notify, now: time.Now}
}

type CreateRequest struct {
	UserID          uuid.UUID
	RootPageID      string
	RootTitle       string
	Format          domain.Format
	IncludeChildren bool
}

func (s *Service) Create(ctx context.Context, req CreateRequest) (*domain.Export, error) {
	e := &domain.Export{
		ID:              uuid.Must(uuid.NewV7()),
		UserID:          req.UserID,
		RootPageID:      req.RootPageID,
		RootTitle:       req.RootTitle,
		Format:          req.Format,
		IncludeChildren: req.IncludeChildren,
		MaxAttempts:     s.limits.MaxAttempts,
	}
	if err := s.repo.CreateExport(ctx, e, s.limits.MaxActivePerUser); err != nil {
		return nil, err
	}
	metrics.ExportsCreated.WithLabelValues(string(e.Format)).Inc()
	slog.InfoContext(ctx, "export enqueued", "export_id", e.ID, "page_id", e.RootPageID, "format", e.Format)
	if s.notify != nil {
		s.notify()
	}
	return e, nil
}

func (s *Service) List(ctx context.Context, userID uuid.UUID) ([]*domain.Export, error) {
	return s.repo.ListExports(ctx, userID, 100)
}

func (s *Service) Get(ctx context.Context, id, userID uuid.UUID) (*domain.Export, error) {
	return s.repo.GetExport(ctx, id, userID)
}

func (s *Service) Delete(ctx context.Context, id, userID uuid.UUID) error {
	e, err := s.repo.DeleteExport(ctx, id, userID)
	if err != nil {
		return err
	}
	if e.FileKey != "" {
		if err := s.blobs.Delete(ctx, e.FileKey); err != nil {
			// The row is gone; the janitor cannot see the file any more, so log loudly.
			slog.ErrorContext(ctx, "deleting export file", "export_id", id, "err", err)
		}
	}
	return nil
}

// Open returns the generated document if it is still within its retention window.
func (s *Service) Open(ctx context.Context, id, userID uuid.UUID) (*domain.Export, io.ReadSeekCloser, error) {
	e, err := s.repo.GetExport(ctx, id, userID)
	if err != nil {
		return nil, nil, err
	}
	if err := e.Downloadable(s.now()); err != nil {
		return nil, nil, err
	}
	f, err := s.blobs.Open(ctx, e.FileKey)
	if err != nil {
		if errors.Is(err, storage.ErrInvalidKey) {
			return nil, nil, domain.ErrNotFound
		}
		return nil, nil, err
	}
	return e, f, nil
}
