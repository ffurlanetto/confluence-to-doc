package export

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/trace"

	"github.com/ffurlanetto/confluence-to-doc/backend/internal/audit"
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
	// Classification is the default classification, used when the requester
	// chose none: written to the subject property and the page footer.
	Classification string
	// Classifications are the configured levels; the marked ones put a
	// watermark on every page.
	Classifications []domain.Classification
	// ShutdownGrace is how long in-flight jobs may run after shutdown starts
	// before being released back to the queue.
	ShutdownGrace time.Duration
	// Audit records the outcome of each export; nil records nothing.
	Audit Auditor
}

// Auditor records security events (implemented by audit.Recorder).
type Auditor interface {
	Record(ctx context.Context, e domain.AuditEvent)
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
	if cfg.Audit == nil {
		cfg.Audit = audit.Discard{}
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
	p.cfg.Audit.Record(finishCtx, exportEvent(job, audit.ActionExportComplete, domain.AuditSuccess,
		map[string]any{"pages": pages, "size": size}))
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
	if outcome == "failed" {
		p.cfg.Audit.Record(ctx, exportEvent(job, audit.ActionExportFail, domain.AuditFailure,
			map[string]any{"reason": UserMessage(cause), "attempts": job.Attempts}))
	}
	if err := p.repo.FailExport(ctx, job.ID, workerID, UserMessage(cause), retry, delay, p.cfg.Retention); err != nil &&
		!errors.Is(err, store.ErrLeaseLost) {
		log.Error("recording export failure", "err", err)
	}
}

// exportEvent is an audit event about a job, attributed to its owner: the
// worker acts on their behalf, with their token.
func exportEvent(job *domain.Export, action string, outcome domain.AuditOutcome, details map[string]any) domain.AuditEvent {
	owner := job.UserID
	details["pageId"] = job.RootPageID
	details["format"] = string(job.Format)
	return domain.AuditEvent{
		ActorID: &owner, Action: action, Outcome: outcome,
		TargetType: audit.TargetExport, TargetID: job.ID.String(), Details: details,
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
	requester, err := p.repo.GetUser(ctx, job.UserID)
	if err != nil {
		return "", 0, 0, fmt.Errorf("loading the requester: %w", err)
	}
	classification := job.Classification
	if classification == "" {
		classification = p.cfg.Classification
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
	generatedAt := p.now()
	html, err := exporter.RenderHTML(renderCtx, root, exporter.RenderOptions{
		Title:             root.Page.Title,
		SourceURL:         root.Page.WebURL,
		GeneratedAt:       generatedAt,
		UseTemplateStyles: p.cfg.UseTemplateStyles,
		Author:            p.author(ctx, client),
		Description:       description(root, generatedAt),
		Keywords:          keywords(root),
		Classification:    classification,
		Properties:        append(properties(root, pages, generatedAt), traceability(job, requester)...),
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
		err := p.converter.Convert(ctx, html, job.Format, p.marking(job, requester, classification, generatedAt), pw)
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
	case errors.Is(err, domain.ErrPATMissing), errors.Is(err, domain.ErrPATUnreadable),
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
	case errors.Is(err, domain.ErrPATUnreadable):
		return "Your Confluence personal access token (PAT) can no longer be read. Enter it again in your preferences."
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

// --- Document properties -----------------------------------------------------
//
// These end up in docProps, and therefore in the PDF as well. They are what a
// document management system indexes, so they name the source precisely enough
// to find the Confluence page the document came from.

// author is the Confluence account whose token fetched the content, which is
// the identity that actually performed the export. It is a courtesy, not a
// requirement: a document with no author is still a valid document, so a
// failure here is logged and ignored rather than failing the export.
func (p *Pool) author(ctx context.Context, client *confluence.Client) string {
	user, err := client.CurrentUser(ctx)
	if err != nil {
		slog.Debug("document author unavailable", "err", err)
		return ""
	}
	if user.DisplayName != "" {
		return user.DisplayName
	}
	return user.Username
}

func description(root *exporter.Node, at time.Time) string {
	where := root.Page.Title
	if root.Page.SpaceName != "" {
		where += " (" + root.Page.SpaceName + ")"
	}
	return "Exported from Confluence: " + where + ", " + at.Format(time.RFC3339)
}

// --- Traceability ----------------------------------------------------------------
//
// A downloaded document is forwarded, printed and filed beyond the reach of
// Confluence permissions. Every page therefore says who exported it, when and
// under which classification, with the export id that leads back to the audit
// trail; sensitive levels add a watermark.

// marking is what docx.Mark stamps on every page.
func (p *Pool) marking(job *domain.Export, requester *domain.User, classification string, at time.Time) docx.Marking {
	parts := []string{"Exported by " + displayName(requester) + " on " + at.UTC().Format("2006-01-02 15:04") + " UTC"}
	if classification != "" {
		parts = append(parts, classification)
	}
	parts = append(parts, "Ref. "+job.ID.String())
	m := docx.Marking{Footer: strings.Join(parts, " · ")}
	for _, c := range p.cfg.Classifications {
		if c.Watermark && strings.EqualFold(c.Label, classification) {
			m.Watermark = strings.ToUpper(c.Label)
		}
	}
	return m
}

// traceability are the custom properties naming the export and its requester,
// for document management systems and for whoever finds the file later.
func traceability(job *domain.Export, requester *domain.User) []exporter.Property {
	return []exporter.Property{
		{Name: "Export ID", Value: job.ID.String()},
		{Name: "Exported by", Value: requester.Email},
	}
}

func displayName(u *domain.User) string {
	switch {
	case u.Name != "" && u.Email != "":
		return u.Name + " (" + u.Email + ")"
	case u.Name != "":
		return u.Name
	case u.Email != "":
		return u.Email
	}
	return u.Subject
}

func keywords(root *exporter.Node) []string {
	words := []string{"Confluence", "export"}
	if root.Page.SpaceKey != "" {
		words = append(words, root.Page.SpaceKey)
	}
	return words
}

func properties(root *exporter.Node, pages int, at time.Time) []exporter.Property {
	props := []exporter.Property{
		{Name: "Confluence page ID", Value: root.Page.ID},
		{Name: "Confluence page title", Value: root.Page.Title},
	}
	if root.Page.SpaceKey != "" {
		props = append(props, exporter.Property{Name: "Confluence space", Value: root.Page.SpaceKey})
	}
	if root.Page.WebURL != "" {
		props = append(props, exporter.Property{Name: "Source URL", Value: root.Page.WebURL})
	}
	return append(props,
		exporter.Property{Name: "Exported pages", Value: strconv.Itoa(pages)},
		exporter.Property{Name: "Exported at", Value: at.Format(time.RFC3339)},
	)
}
