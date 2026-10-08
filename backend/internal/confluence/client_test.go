package confluence_test

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/ffurlanetto/confluence-to-doc/backend/internal/confluence"
	"github.com/ffurlanetto/confluence-to-doc/backend/internal/confluence/fake"
)

func setup(t *testing.T) (*fake.Server, *confluence.Client) {
	t.Helper()
	f := fake.New("secret")
	f.AddPage(fake.Page{ID: "1", Title: "Root", Body: "<p>root</p>"})
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	u, _ := url.Parse(srv.URL)
	return f, confluence.NewClient(u, "secret", srv.Client())
}

func TestGetPage(t *testing.T) {
	_, c := setup(t)
	p, err := c.GetPage(context.Background(), "1", true)
	if err != nil {
		t.Fatal(err)
	}
	if p.Title != "Root" || p.BodyHTML != "<p>root</p>" || p.SpaceKey != "DEMO" {
		t.Fatalf("unexpected page %+v", p)
	}
}

func TestGetPageErrors(t *testing.T) {
	_, c := setup(t)
	if _, err := c.GetPage(context.Background(), "999", false); !errors.Is(err, confluence.ErrNotFound) {
		t.Errorf("want ErrNotFound, got %v", err)
	}
	if _, err := c.GetPage(context.Background(), "../etc", false); !errors.Is(err, confluence.ErrNotFound) {
		t.Errorf("non numeric id must be rejected, got %v", err)
	}
}

func TestInvalidToken(t *testing.T) {
	f, _ := setup(t)
	srv := httptest.NewServer(f)
	defer srv.Close()
	u, _ := url.Parse(srv.URL)
	c := confluence.NewClient(u, "wrong", srv.Client())
	if _, err := c.CurrentUser(context.Background()); !errors.Is(err, confluence.ErrUnauthorized) {
		t.Fatalf("want ErrUnauthorized, got %v", err)
	}
}

func TestListChildrenPaginates(t *testing.T) {
	f, c := setup(t)
	for i := range 60 {
		f.AddPage(fake.Page{ID: fmt.Sprint(100 + i), Title: fmt.Sprint("Child ", i), ParentID: "1"})
	}
	children, err := c.ListChildren(context.Background(), "1", true)
	if err != nil {
		t.Fatal(err)
	}
	if len(children) != 60 {
		t.Fatalf("got %d children, want 60", len(children))
	}
	if children[0].ID != "100" || children[59].ID != "159" {
		t.Errorf("order not preserved: first=%s last=%s", children[0].ID, children[59].ID)
	}
}

func TestRetriesOnServiceUnavailable(t *testing.T) {
	f, c := setup(t)
	f.FailNext.Store(2)
	if _, err := c.GetPage(context.Background(), "1", false); err != nil {
		t.Fatalf("expected retries to succeed, got %v", err)
	}
}

func TestGivesUpAfterMaxRetries(t *testing.T) {
	f, _ := setup(t)
	srv := httptest.NewServer(f)
	defer srv.Close()
	u, _ := url.Parse(srv.URL)
	c := confluence.NewClient(u, "secret", srv.Client(), confluence.WithMaxRetries(1))
	f.FailNext.Store(5)
	_, err := c.GetPage(context.Background(), "1", false)
	var se *confluence.StatusError
	if !errors.As(err, &se) || se.Code != http.StatusServiceUnavailable {
		t.Fatalf("want 503 StatusError, got %v", err)
	}
	if !confluence.Retryable(err) {
		t.Error("503 should be retryable")
	}
}

func TestDownloadRefusesForeignHost(t *testing.T) {
	_, c := setup(t)
	_, _, err := c.Download(context.Background(), "https://evil.example.com/steal", 1024)
	if !errors.Is(err, confluence.ErrForeignHost) {
		t.Fatalf("want ErrForeignHost, got %v", err)
	}
}

func TestDownloadSizeLimit(t *testing.T) {
	f, c := setup(t)
	f.AddAttachment("1/big.bin", make([]byte, 2048))
	if _, _, err := c.Download(context.Background(), "/download/attachments/1/big.bin", 1024); !errors.Is(err, confluence.ErrTooLarge) {
		t.Fatalf("want ErrTooLarge, got %v", err)
	}
	data, _, err := c.Download(context.Background(), "/download/attachments/1/big.bin", 4096)
	if err != nil || len(data) != 2048 {
		t.Fatalf("download failed: %v (%d bytes)", err, len(data))
	}
}

func TestRedirectToForeignHostIsBlocked(t *testing.T) {
	evil := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "" {
			t.Error("token leaked to foreign host")
		}
	}))
	defer evil.Close()
	redirector := httptest.NewServer(http.RedirectHandler(evil.URL, http.StatusFound))
	defer redirector.Close()
	u, _ := url.Parse(redirector.URL)
	c := confluence.NewClient(u, "secret", redirector.Client())
	if _, err := c.GetPage(context.Background(), "1", false); !errors.Is(err, confluence.ErrForeignHost) {
		t.Fatalf("want ErrForeignHost, got %v", err)
	}
}

func TestSearchPages(t *testing.T) {
	f, c := setup(t)
	f.AddPage(fake.Page{ID: "2", Title: "Architecture \"v2\"", SpaceKey: "ENG"})
	res, err := c.SearchPages(context.Background(), "archi", "", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(res) != 1 || res[0].ID != "2" {
		t.Fatalf("unexpected results %+v", res)
	}
}

func TestParsePageRef(t *testing.T) {
	cases := []struct {
		in   string
		want confluence.PageRef
		ok   bool
	}{
		{"12345", confluence.PageRef{ID: "12345"}, true},
		{"https://c.example.com/pages/viewpage.action?pageId=42", confluence.PageRef{ID: "42"}, true},
		{"https://c.example.com/wiki/spaces/ENG/pages/77/My+Page", confluence.PageRef{ID: "77"}, true},
		{"https://c.example.com/display/ENG/My+Page", confluence.PageRef{SpaceKey: "ENG", Title: "My Page"}, true},
		{"architecture", confluence.PageRef{}, false},
	}
	for _, tc := range cases {
		got, ok := confluence.ParsePageRef(tc.in)
		if ok != tc.ok || got != tc.want {
			t.Errorf("ParsePageRef(%q) = %+v,%v want %+v,%v", tc.in, got, ok, tc.want, tc.ok)
		}
	}
}

type countingLimiter struct {
	calls int
	err   error
}

func (l *countingLimiter) Wait(context.Context) error { l.calls++; return l.err }

func TestEveryRequestWaitsForTheLimiter(t *testing.T) {
	f, _ := setup(t)
	srv := httptest.NewServer(f)
	defer srv.Close()
	u, _ := url.Parse(srv.URL)
	limiter := &countingLimiter{}
	c := confluence.NewClient(u, "secret", srv.Client(), confluence.WithLimiter(limiter))
	f.FailNext.Store(2)
	if _, err := c.GetPage(context.Background(), "1", false); err != nil {
		t.Fatal(err)
	}
	if limiter.calls != 3 {
		t.Fatalf("limiter consulted %d times, want once per attempt (3)", limiter.calls)
	}

	limiter.err = context.DeadlineExceeded
	if _, err := c.GetPage(context.Background(), "1", false); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("a limiter error must stop the request, got %v", err)
	}
}
