package export_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/johannesboyne/gofakes3"
	"github.com/johannesboyne/gofakes3/backend/s3mem"

	"github.com/ffurlanetto/confluence-to-doc/backend/internal/account"
	"github.com/ffurlanetto/confluence-to-doc/backend/internal/audit"
	"github.com/ffurlanetto/confluence-to-doc/backend/internal/confluence"
	"github.com/ffurlanetto/confluence-to-doc/backend/internal/confluence/fake"
	"github.com/ffurlanetto/confluence-to-doc/backend/internal/crypto"
	"github.com/ffurlanetto/confluence-to-doc/backend/internal/docx"
	"github.com/ffurlanetto/confluence-to-doc/backend/internal/domain"
	"github.com/ffurlanetto/confluence-to-doc/backend/internal/export"
	"github.com/ffurlanetto/confluence-to-doc/backend/internal/exporter"
	"github.com/ffurlanetto/confluence-to-doc/backend/internal/storage"
	"github.com/ffurlanetto/confluence-to-doc/backend/internal/store"
	"github.com/ffurlanetto/confluence-to-doc/backend/internal/testutil"
)

// htmlConverter "converts" by copying the HTML, followed by the marking it
// was asked to stamp, which lets tests assert on both without LibreOffice.
type htmlConverter struct{ fail error }

func (c htmlConverter) Convert(_ context.Context, html []byte, _ domain.Format, m docx.Marking, dst io.Writer) error {
	if c.fail != nil {
		return c.fail
	}
	_, err := fmt.Fprintf(dst, "%s\nFOOTER: %s\nWATERMARK: %s\n", html, m.Footer, m.Watermark)
	return err
}

type env struct {
	store   *store.Store
	blobs   storage.BlobStore
	svc     *export.Service
	pool    *export.Pool
	user    *domain.User
	fake    *fake.Server
	account *account.Service
}

func newEnv(t *testing.T, conv htmlConverter) *env {
	t.Helper()
	blobs, err := storage.NewLocal(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return newEnvWithBlobs(t, conv, blobs)
}

func newEnvWithBlobs(t *testing.T, conv htmlConverter, blobs storage.BlobStore) *env {
	t.Helper()
	s := testutil.NewStore(t)
	ctx := context.Background()

	f := fake.New("pat")
	f.AddPage(fake.Page{ID: "1", Title: "Root", Body: "<p>root body</p>"})
	f.AddPage(fake.Page{ID: "2", Title: "Child", ParentID: "1", Body: "<p>child body</p>"})
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	u, _ := url.Parse(srv.URL)

	sealer, _ := crypto.NewSealer(bytes.Repeat([]byte{1}, 32))
	acc := account.NewService(s, sealer, u, 5*time.Second)
	user, _ := s.UpsertUser(ctx, "iss", uuid.NewString(), "owner@example.com", "Olive Owner")

	pool := export.NewPool(s, acc, conv, blobs, export.WorkerConfig{
		Concurrency: 1, PollInterval: 20 * time.Millisecond, JobTimeout: time.Minute, Lease: 3 * time.Second,
		Retention: 48 * time.Hour, MaxPages: 10, MaxImageBytes: 1 << 20, ConfluenceWorkers: 2,
		Classification:  "Internal",
		Classifications: []domain.Classification{{Label: "Internal"}, {Label: "Confidential", Watermark: true}},
	})
	svc := export.NewService(s, blobs, export.Limits{MaxAttempts: 2, MaxActivePerUser: 5}, pool.Notify)
	return &env{store: s, blobs: blobs, svc: svc, pool: pool, user: user, fake: f, account: acc}
}

func (e *env) waitFor(t *testing.T, id uuid.UUID, want ...domain.ExportStatus) *domain.Export {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		got, err := e.svc.Get(context.Background(), id, e.user.ID)
		if err != nil {
			t.Fatal(err)
		}
		for _, w := range want {
			if got.Status == w {
				return got
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("export %s did not reach %v", id, want)
	return nil
}

func runPool(t *testing.T, p *export.Pool) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { p.Run(ctx); close(done) }()
	t.Cleanup(func() { cancel(); <-done })
}

func TestExportEndToEnd(t *testing.T) {
	e := newEnv(t, htmlConverter{})
	ctx := context.Background()
	if _, err := e.account.SetPAT(ctx, e.user.ID, "pat"); err != nil {
		t.Fatal(err)
	}
	runPool(t, e.pool)

	job, err := e.svc.Create(ctx, export.CreateRequest{
		UserID: e.user.ID, RootPageID: "1", RootTitle: "Root", Format: domain.FormatDOCX, IncludeChildren: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	done := e.waitFor(t, job.ID, domain.StatusSucceeded, domain.StatusFailed)
	if done.Status != domain.StatusSucceeded {
		t.Fatalf("export failed: %s", done.Error)
	}
	if done.PagesTotal != 2 || done.ExpiresAt == nil || done.ExpiresAt.Sub(*done.FinishedAt) != 48*time.Hour {
		t.Fatalf("unexpected export %+v", done)
	}

	_, f, err := e.svc.Open(ctx, job.ID, e.user.ID)
	if err != nil {
		t.Fatal(err)
	}
	content, _ := io.ReadAll(f)
	f.Close()
	for _, want := range []string{"root body", "child body", "1.1 Child"} {
		if !strings.Contains(string(content), want) {
			t.Errorf("document does not contain %q", want)
		}
	}

	// Another user cannot download it.
	if _, _, err := e.svc.Open(ctx, job.ID, uuid.New()); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("want ErrNotFound for another user, got %v", err)
	}

	// Deleting removes the file.
	if err := e.svc.Delete(ctx, job.ID, e.user.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := e.blobs.Open(ctx, done.FileKey); err == nil {
		t.Error("file should have been deleted")
	}
}

func TestExportFailsWithoutPAT(t *testing.T) {
	e := newEnv(t, htmlConverter{})
	runPool(t, e.pool)
	job, err := e.svc.Create(context.Background(), export.CreateRequest{UserID: e.user.ID, RootPageID: "1", Format: domain.FormatPDF})
	if err != nil {
		t.Fatal(err)
	}
	got := e.waitFor(t, job.ID, domain.StatusFailed)
	if got.Attempts != 1 {
		t.Errorf("a missing PAT is not retryable, attempts = %d", got.Attempts)
	}
	if !strings.Contains(got.Error, "PAT") {
		t.Errorf("unexpected user message %q", got.Error)
	}
}

func TestExportRetriesConversionErrors(t *testing.T) {
	e := newEnv(t, htmlConverter{fail: errors.New("soffice crashed")})
	_, _ = e.account.SetPAT(context.Background(), e.user.ID, "pat")
	job, _ := e.svc.Create(context.Background(), export.CreateRequest{UserID: e.user.ID, RootPageID: "1", Format: domain.FormatPDF})

	runPool(t, e.pool)
	var got *domain.Export
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		got, _ = e.svc.Get(context.Background(), job.ID, e.user.ID)
		if got.Error != "" {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if got.Status != domain.StatusQueued || got.Attempts != 1 || got.Error == "" {
		t.Fatalf("expected a scheduled retry, got %+v", got)
	}
	if strings.Contains(got.Error, "soffice") {
		t.Errorf("internal details leaked to user: %q", got.Error)
	}
}

func TestJanitorExpiresDocuments(t *testing.T) {
	e := newEnv(t, htmlConverter{})
	ctx := context.Background()
	job, _ := e.svc.Create(ctx, export.CreateRequest{UserID: e.user.ID, RootPageID: "1", Format: domain.FormatPDF})
	claimed, _ := e.store.ClaimNext(ctx, "w", time.Minute)
	key := job.ID.String() + ".pdf"
	_, _ = e.blobs.Put(ctx, key, strings.NewReader("pdf"))
	_ = e.store.CompleteExport(ctx, claimed.ID, "w", key, 3, 1, -time.Second)

	old := domain.AuditEvent{ID: uuid.Must(uuid.NewV7()), OccurredAt: time.Now().Add(-48 * time.Hour),
		Action: "auth.login", Outcome: domain.AuditSuccess}
	if err := e.store.InsertAuditEvent(ctx, old); err != nil {
		t.Fatal(err)
	}
	janitor := export.NewJanitor(e.store, e.blobs, time.Hour).WithAudit(audit.New(e.store, nil), e.store, 24*time.Hour)
	if n := janitor.RunOnce(ctx); n != 1 {
		t.Fatalf("expired %d exports, want 1", n)
	}
	events, _ := e.store.ListAuditEvents(ctx, domain.AuditFilter{})
	if len(events) != 1 || events[0].Action != audit.ActionExportExpire || events[0].TargetID != job.ID.String() {
		t.Errorf("want only the expiry event (old one purged), got %+v", events)
	}
	if _, err := e.blobs.Open(ctx, key); err == nil {
		t.Error("file must be deleted after retention")
	}
	if _, _, err := e.svc.Open(ctx, job.ID, e.user.ID); !errors.Is(err, domain.ErrExportExpired) {
		t.Errorf("want ErrExportExpired, got %v", err)
	}
}

func TestUserMessageHidesInternals(t *testing.T) {
	for _, err := range []error{errors.New("pq: connection refused"), confluence.ErrUnauthorized, exporter.ErrTooManyPages} {
		msg := export.UserMessage(err)
		if msg == "" || strings.Contains(msg, "pq:") {
			t.Errorf("bad message for %v: %q", err, msg)
		}
	}
}

func newFakeS3(t *testing.T) storage.BlobStore {
	t.Helper()
	backend := s3mem.New()
	if err := backend.CreateBucket("exports"); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(gofakes3.New(backend).Server())
	t.Cleanup(srv.Close)
	s, err := storage.NewS3(context.Background(), storage.S3Options{
		Bucket: "exports", Region: "us-east-1", Endpoint: srv.URL,
		AccessKeyID: "test", SecretAccessKey: "test", Prefix: "exports/", UsePathStyle: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestExportEndToEndWithS3(t *testing.T) {
	e := newEnvWithBlobs(t, htmlConverter{}, newFakeS3(t))
	ctx := context.Background()
	if _, err := e.account.SetPAT(ctx, e.user.ID, "pat"); err != nil {
		t.Fatal(err)
	}
	runPool(t, e.pool)

	job, err := e.svc.Create(ctx, export.CreateRequest{UserID: e.user.ID, RootPageID: "1", Format: domain.FormatPDF, IncludeChildren: true})
	if err != nil {
		t.Fatal(err)
	}
	if done := e.waitFor(t, job.ID, domain.StatusSucceeded, domain.StatusFailed); done.Status != domain.StatusSucceeded {
		t.Fatalf("export failed: %s", done.Error)
	}
	_, f, err := e.svc.Open(ctx, job.ID, e.user.ID)
	if err != nil {
		t.Fatal(err)
	}
	content, _ := io.ReadAll(f)
	f.Close()
	if !strings.Contains(string(content), "child body") {
		t.Fatal("document stored in S3 is incomplete")
	}

	// Deleting the export removes the S3 object.
	if err := e.svc.Delete(ctx, job.ID, e.user.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := e.blobs.Open(ctx, job.ID.String()+".pdf"); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("object should be deleted, got %v", err)
	}
}

func TestOpenReportsMissingFileAsNotFound(t *testing.T) {
	e := newEnv(t, htmlConverter{})
	ctx := context.Background()
	job, _ := e.svc.Create(ctx, export.CreateRequest{UserID: e.user.ID, RootPageID: "1", Format: domain.FormatPDF})
	claimed, _ := e.store.ClaimNext(ctx, "w", time.Minute)
	// Completed in the database, but the file never reached the storage.
	_ = e.store.CompleteExport(ctx, claimed.ID, "w", job.ID.String()+".pdf", 3, 1, time.Hour)
	if _, _, err := e.svc.Open(ctx, job.ID, e.user.ID); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("want ErrNotFound, got %v", err)
	}
}

func TestEveryDocumentIsMarked(t *testing.T) {
	e := newEnv(t, htmlConverter{})
	ctx := context.Background()
	if _, err := e.account.SetPAT(ctx, e.user.ID, "pat"); err != nil {
		t.Fatal(err)
	}
	runPool(t, e.pool)

	read := func(job *domain.Export) string {
		t.Helper()
		done := e.waitFor(t, job.ID, domain.StatusSucceeded, domain.StatusFailed)
		if done.Status != domain.StatusSucceeded {
			t.Fatalf("export failed: %s", done.Error)
		}
		_, f, err := e.svc.Open(context.Background(), job.ID, e.user.ID)
		if err != nil {
			t.Fatal(err)
		}
		defer f.Close()
		b, _ := io.ReadAll(f)
		return string(b)
	}

	confidential, _ := e.svc.Create(ctx, export.CreateRequest{UserID: e.user.ID, RootPageID: "1", Format: domain.FormatPDF, Classification: "Confidential"})
	doc := read(confidential)
	footer := regexp.MustCompile(`FOOTER: Exported by Olive Owner \(owner@example\.com\) on \d{4}-\d{2}-\d{2} \d{2}:\d{2} UTC · Confidential · Ref\. ` + confidential.ID.String())
	if !footer.MatchString(doc) || !strings.Contains(doc, "WATERMARK: CONFIDENTIAL\n") {
		t.Errorf("confidential export not marked as such:\n%s", doc[strings.Index(doc, "FOOTER"):])
	}
	for _, want := range []string{`<meta name="classification" content="Confidential">`, `content="` + confidential.ID.String() + `"`, `content="owner@example.com"`} {
		if !strings.Contains(doc, want) {
			t.Errorf("document properties miss %s", want)
		}
	}

	// Without a choice, the configured default applies — and it carries no
	// watermark.
	plain, _ := e.svc.Create(ctx, export.CreateRequest{UserID: e.user.ID, RootPageID: "1", Format: domain.FormatPDF})
	doc = read(plain)
	if !strings.Contains(doc, " · Internal · Ref. "+plain.ID.String()) || !strings.Contains(doc, "WATERMARK: \n") {
		t.Errorf("default classification not applied:\n%s", doc[strings.Index(doc, "FOOTER"):])
	}
}
