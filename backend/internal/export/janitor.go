package export

import (
	"context"
	"log/slog"
	"time"

	"github.com/ffurlanetto/confluence-to-doc/backend/internal/storage"
)

// historyRetention is how long expired exports stay listed (without file).
const historyRetention = 30 * 24 * time.Hour

// Janitor enforces the retention policy: documents are deleted once their
// download window (48h by default) is over. It is idempotent and safe to run
// on several instances at once.
type Janitor struct {
	repo     Repo
	blobs    storage.BlobStore
	interval time.Duration
}

func NewJanitor(repo Repo, blobs storage.BlobStore, interval time.Duration) *Janitor {
	return &Janitor{repo: repo, blobs: blobs, interval: interval}
}

func (j *Janitor) Run(ctx context.Context) {
	t := time.NewTicker(j.interval)
	defer t.Stop()
	for {
		j.RunOnce(ctx)
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// RunOnce performs one cleanup pass and returns the number of expired exports.
func (j *Janitor) RunOnce(ctx context.Context) int {
	expired := 0
	for ctx.Err() == nil {
		batch, err := j.repo.ListExpired(ctx, 100)
		if err != nil {
			slog.ErrorContext(ctx, "janitor: listing expired exports", "err", err)
			break
		}
		for _, e := range batch {
			if e.FileKey != "" {
				if err := j.blobs.Delete(ctx, e.FileKey); err != nil {
					slog.ErrorContext(ctx, "janitor: deleting file", "export_id", e.ID, "err", err)
					continue // keep the row so we retry next time
				}
			}
			if err := j.repo.MarkExpired(ctx, e.ID); err != nil {
				slog.ErrorContext(ctx, "janitor: marking expired", "export_id", e.ID, "err", err)
				continue
			}
			expired++
		}
		if len(batch) < 100 {
			break
		}
	}
	if n, err := j.repo.PurgeExpired(ctx, historyRetention); err == nil && n > 0 {
		slog.InfoContext(ctx, "janitor: purged old export history", "count", n)
	}
	if n, err := j.repo.DeleteExpiredSessions(ctx); err == nil && n > 0 {
		slog.InfoContext(ctx, "janitor: deleted expired sessions", "count", n)
	}
	if expired > 0 {
		slog.InfoContext(ctx, "janitor: expired exports", "count", expired)
	}
	return expired
}
