package export_test

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/ffurlanetto/confluence-to-doc/backend/internal/account"
	"github.com/ffurlanetto/confluence-to-doc/backend/internal/confluence"
	"github.com/ffurlanetto/confluence-to-doc/backend/internal/confluence/fake"
	"github.com/ffurlanetto/confluence-to-doc/backend/internal/crypto"
	"github.com/ffurlanetto/confluence-to-doc/backend/internal/domain"
	"github.com/ffurlanetto/confluence-to-doc/backend/internal/export"
	"github.com/ffurlanetto/confluence-to-doc/backend/internal/exporter"
	"github.com/ffurlanetto/confluence-to-doc/backend/internal/storage"
	"github.com/ffurlanetto/confluence-to-doc/backend/internal/store"
	"github.com/ffurlanetto/confluence-to-doc/backend/internal/testutil"
)

// htmlConverter "converts" by copying the HTML, which lets tests assert on
// the content without requiring LibreOffice.
type htmlConverter struct{ fail error }

func (c htmlConverter) Convert(_ context.Context, html []byte, _ domain.Format, dst io.Writer) error {
	if c.fail != nil {
		return c.fail
	}
	_, err := dst.Write(html)
	return err
}

type env struct {
	store   *store.Store
	blobs   *storage.Local
	svc     *export.Service
	pool    *export.Pool
	user    *domain.User
	fake    *fake.Server
	account *account.Service
}

func newEnv(t *testing.T, conv htmlConverter) *env {
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
	blobs, _ := storage.NewLocal(t.TempDir())
	user, _ := s.UpsertUser(ctx, "iss", uuid.NewString(), "", "")

	pool := export.NewPool(s, acc, conv, blobs, export.WorkerConfig{
		Concurrency: 1, PollInterval: 20 * time.Millisecond, JobTimeout: time.Minute, Lease: 3 * time.Second,
		Retention: 48 * time.Hour, MaxPages: 10, MaxImageBytes: 1 << 20, ConfluenceWorkers: 2,
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

	if n := export.NewJanitor(e.store, e.blobs, time.Hour).RunOnce(ctx); n != 1 {
		t.Fatalf("expired %d exports, want 1", n)
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
