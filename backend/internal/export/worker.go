package export

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/trace"

	"github.com/ffurlanetto/confluence-to-doc/backend/internal/confluence"
	"github.com/ffurlanetto/confluence-to-doc/backend/internal/converter"
	"github.com/ffurlanetto/confluence-to-doc/backend/internal/docx"
	"github.com/ffurlanetto/confluence-to-doc/backend/internal/domain"
	"github.com/ffurlanetto/confluence-to-doc/backend/internal/exporter"
	"github.com/ffurlanetto/confluence-to-doc/backend/internal/observability"
	"github.com/ffurlanetto/confluence-to-doc/backend/internal/storage"
	"github.com/ffurlanetto/confluence-to-doc/backend/internal/store"
)

type WorkerConfig struct {
	Concurrency       int
	PollInterval      time.Duration
	JobTimeout        time.Duration
	Lease             time.Duration
	Retention         time.Duration
	MaxPages          int
	MaxImageBytes     int64
	ConfluenceWorkers int
	// UseTemplateStyles tells the renderer to leave typography to the
	// company Word template instead of styling the document itself.
	UseTemplateStyles bool
	// ShutdownGrace is how long in-flight jobs may run after shutdown starts
	// before being released back to the queue.
	ShutdownGrace time.Duration
}

// Pool runs a bounded number of workers that consume the export queue.
// Concurrency is the load-control knob: it caps simultaneous Confluence
// crawls and LibreOffice processes per instance.
type Pool struct {
	repo      Repo
	clients   ClientProvider
	converter converter.Converter
	blobs     storage.BlobStore
	cfg       WorkerConfig
	id        string
	wake      chan struct{}
	now       func() time.Time
}

func NewPool(repo Repo, clients ClientProvider, conv converter.Converter, blobs storage.BlobStore, cfg WorkerConfig) *Pool {
	if cfg.Lease <= 0 {
		cfg.Lease = time.Minute
	}
	if cfg.ShutdownGrace <= 0 {
		cfg.ShutdownGrace = 30 * time.Second
	}
	host, _ := os.Hostname()
	return &Pool{
		repo: repo, clients: clients, converter: conv, blobs: blobs, cfg: cfg,
		id:   fmt.Sprintf("%s-%s", host, uuid.NewString()[:8]),
		wake: make(chan struct{}, 1),
		now:  time.Now,
	}
}

// Notify wakes an idle worker immediately (instead of waiting for the next
// poll). Safe to call from any goroutine; never blocks.
func (p *Pool) Notify() {
	select {
	case p.wake <- struct{}{}:
	default:
	}
}

// Run blocks until ctx is cancelled and all workers have stopped.
func (p *Pool) Run(ctx context.Context) {
	slog.InfoContext(ctx, "export workers started", "worker_id", p.id, "concurrency", p.cfg.Concurrency)
	var wg sync.WaitGroup
	for i := range p.cfg.Concurrency {
		wg.Add(1)
		go func() {
			defer wg.Done()
			p.loop(ctx, fmt.Sprintf("%s/%d", p.id, i))
		}()
	}
	wg.Wait()
	slog.Info("export workers stopped", "worker_id", p.id)
}

func (p *Pool) loop(ctx context.Context, workerID string) {
	ticker := time.NewTicker(p.cfg.PollInterval)
	defer ticker.Stop()
	for {
		if ctx.Err() != nil {
			return
		}
		job, err := p.repo.ClaimNext(ctx, workerID, p.cfg.Lease)
		switch {
		case err == nil:
			p.process(ctx, workerID, job)
			continue // look for more work right away
		case errors.Is(err, domain.ErrNotFound):
		case ctx.Err() == nil:
			slog.ErrorContext(ctx, "claiming export", "err", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		case <-p.wake:
		}
	}
}

// process runs one job. The job context is detached from the pool context so
// shutdown gives in-flight work a grace period rather than killing it.
func (p *Pool) process(poolCtx context.Context, workerID string, job *domain.Export) {
	log := slog.With("export_id", job.ID, "worker_id", workerID, "attempt", job.Attempts)
	start := p.now()

	ctx, cancel := context.WithTimeout(context.WithoutCancel(poolCtx), p.cfg.JobTimeout)
	defer cancel()

	// A job is its own trace: it is picked up long after — and independently
	// of — the request that queued it. The export id ties the two together.
	ctx, span := observability.Tracer().Start(ctx, "export.process",
		trace.WithNewRoot(), trace.WithSpanKind(trace.SpanKindConsumer),
		trace.WithAttributes(
			observability.AttrExportID.String(job.ID.String()),
			observability.AttrFormat.String(string(job.Format)),
			observability.AttrPageID.String(job.RootPageID),
			attribute.Int("c2d.export.attempt", job.Attempts),
		))
	defer span.End()

	if job.Attempts > job.MaxAttempts {
		// Can only happen when workers repeatedly died while holding the job.
		p.finish(ctx, log, job, workerID, errors.New("worker lost the job too many times"), false)
		return
	}

	var done, total atomic.Int64
	var leaseLost atomic.Bool
	hbDone := make(chan struct{})
	go func() {
		defer close(hbDone)
		t := time.NewTicker(p.cfg.Lease / 3)
		defer t.Stop()
		shutdown := poolCtx.Done()
		var grace <-chan time.Time
		for {
			select {
			case <-ctx.Done():
				return
			case <-shutdown:
				// Shutdown: keep heartbeating during a grace period, then
				// cancel the job so it is released back to the queue.
				shutdown = nil
				grace = time.After(p.cfg.ShutdownGrace)
			case <-grace:
				log.Warn("shutdown grace period elapsed, releasing job")
				cancel()
				return
			case <-t.C:
				err := p.repo.Heartbeat(ctx, job.ID, workerID, p.cfg.Lease, int(done.Load()), int(total.Load()))
				if errors.Is(err, store.ErrLeaseLost) {
					log.Info("export deleted or reclaimed, cancelling")
					leaseLost.Store(true)
					cancel()
					return
				}
				if err != nil && ctx.Err() == nil {
					log.Warn("heartbeat failed", "err", err)
				}
			}
		}
	}()

	key, size, pages, err := p.generate(ctx, job, func(d, t int) { done.Store(int64(d)); total.Store(int64(t)) })
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
	}
	cancel()
	<-hbDone
	finishCtx, finishCancel := context.WithTimeout(context.WithoutCancel(poolCtx), 10*time.Second)
	defer finishCancel()

	switch {
	case leaseLost.Load():
		p.discard(finishCtx, key)
		return
	case err != nil && poolCtx.Err() != nil && errors.Is(err, context.Canceled):
		p.discard(finishCtx, key)
		if rerr := p.repo.ReleaseExport(finishCtx, job.ID, workerID); rerr != nil {
			log.Warn("releasing job", "err", rerr)
		}
		return
	case err != nil:
		p.finish(finishCtx, log, job, workerID, err, retryable(err))
		return
	}

	if err := p.repo.CompleteExport(finishCtx, job.ID, workerID, key, size, pages, p.cfg.Retention); err != nil {
		log.Warn("completing export", "err", err)
		p.discard(finishCtx, key)
		return
	}
	formatAttr := metric.WithAttributes(observability.AttrFormat.String(string(job.Format)))
	observability.ExportsFinished.Add(finishCtx, 1, metric.WithAttributes(
		observability.AttrFormat.String(string(job.Format)),
		observability.AttrOutcome.String("succeeded")))
	observability.ExportDuration.Record(finishCtx, p.now().Sub(start).Seconds(), formatAttr)
	observability.ExportPages.Record(finishCtx, int64(pages), formatAttr)
	span.SetAttributes(attribute.Int("c2d.export.pages", pages), attribute.Int64("c2d.export.bytes", size))
	log.Info("export succeeded", "pages", pages, "bytes", size, "duration", p.now().Sub(start).String())
}

func (p *Pool) finish(ctx context.Context, log *slog.Logger, job *domain.Export, workerID string, cause error, retry bool) {
	delay := time.Duration(1<<min(job.Attempts, 6)) * 15 * time.Second
	outcome := "failed"
	if retry && job.Attempts < job.MaxAttempts {
		outcome = "retry"
	}
	observability.ExportsFinished.Add(ctx, 1, metric.WithAttributes(
		observability.AttrFormat.String(string(job.Format)),
		observability.AttrOutcome.String(outcome)))
	log.WarnContext(ctx, "export attempt failed", "err", cause, "outcome", outcome)
	if err := p.repo.FailExport(ctx, job.ID, workerID, UserMessage(cause), retry, delay, p.cfg.Retention); err != nil &&
		!errors.Is(err, store.ErrLeaseLost) {
		log.Error("recording export failure", "err", err)
	}
}

func (p *Pool) discard(ctx context.Context, key string) {
	if key != "" {
		_ = p.blobs.Delete(ctx, key)
	}
}

// generate crawls Confluence, renders and converts the document, and stores
// it. It returns the storage key, the file size and the number of pages.
func (p *Pool) generate(ctx context.Context, job *domain.Export, progress func(done, total int)) (string, int64, int, error) {
	client, err := p.clients.Client(ctx, job.UserID)
	if err != nil {
		return "", 0, 0, err
	}
	crawlCtx, crawlSpan := observability.Start(ctx, "confluence.crawl",
		observability.AttrPageID.String(job.RootPageID))
	root, err := exporter.BuildTree(crawlCtx, client, job.RootPageID, exporter.TreeOptions{
		IncludeChildren: job.IncludeChildren,
		MaxPages:        p.cfg.MaxPages,
		Concurrency:     p.cfg.ConfluenceWorkers,
		OnProgress:      func(n int) { progress(0, n) },
	})
	if err != nil {
		observability.End(crawlSpan, err)
		return "", 0, 0, err
	}
	pages := root.Count()
	crawlSpan.SetAttributes(attribute.Int("c2d.export.pages", pages))
	observability.End(crawlSpan, nil)
	progress(pages, pages)

	renderCtx, renderSpan := observability.Start(ctx, "document.render")
	html, err := exporter.RenderHTML(renderCtx, root, exporter.RenderOptions{
		Title:             root.Page.Title,
		SourceURL:         root.Page.WebURL,
		GeneratedAt:       p.now(),
		UseTemplateStyles: p.cfg.UseTemplateStyles,
	}, exporter.ConfluenceAssets{Client: client, MaxImageBytes: p.cfg.MaxImageBytes})
	if err != nil {
		observability.End(renderSpan, err)
		return "", 0, 0, err
	}
	renderSpan.SetAttributes(attribute.Int("c2d.document.html_bytes", len(html)))
	observability.End(renderSpan, nil)

	key := job.ID.String() + "." + job.Format.Extension()
	ctx, convertSpan := observability.Start(ctx, "document.convert",
		observability.AttrFormat.String(string(job.Format)))
	defer convertSpan.End()
	pr, pw := io.Pipe()
	convErr := make(chan error, 1)
	go func() {
		err := p.converter.Convert(ctx, html, job.Format, pw)
		pw.CloseWithError(err)
		convErr <- err
	}()
	size, err := p.blobs.Put(ctx, key, pr)
	_ = pr.CloseWithError(err)
	if cerr := <-convErr; cerr != nil {
		p.discard(context.WithoutCancel(ctx), key)
		return "", 0, 0, fmt.Errorf("converting document: %w", cerr)
	}
	if err != nil {
		p.discard(context.WithoutCancel(ctx), key)
		return "", 0, 0, fmt.Errorf("storing document: %w", err)
	}
	return key, size, pages, nil
}

func retryable(err error) bool {
	switch {
	case errors.Is(err, domain.ErrPATMissing),
		errors.Is(err, exporter.ErrTooManyPages),
		errors.Is(err, domain.ErrInvalidFormat),
		// A template that cannot be applied fails the same way every time.
		errors.Is(err, docx.ErrApply):
		return false
	case errors.Is(err, context.DeadlineExceeded):
		return false // the next attempt would most likely time out as well
	}
	return confluence.Retryable(err)
}

// UserMessage converts an internal error into a message safe to show to the
// user (no internal details, actionable when possible).
func UserMessage(err error) string {
	switch {
	case errors.Is(err, domain.ErrPATMissing):
		return "No Confluence personal access token (PAT) is configured in your preferences."
	case errors.Is(err, confluence.ErrUnauthorized):
		return "Your Confluence personal access token (PAT) is invalid or expired. Update it in your preferences."
	case errors.Is(err, confluence.ErrForbidden):
		return "You do not have the required permissions on this Confluence page."
	case errors.Is(err, confluence.ErrNotFound):
		return "The Confluence page cannot be found (deleted or not accessible)."
	case errors.Is(err, exporter.ErrTooManyPages):
		return "The page tree contains too many pages for a single export. Export a sub-branch instead."
	case errors.Is(err, context.DeadlineExceeded):
		return "Generation exceeded the maximum allowed time."
	case errors.Is(err, docx.ErrApply):
		return "The company Word template could not be applied. Contact your administrator."
	default:
		return "A technical error occurred while generating the document."
	}
}
