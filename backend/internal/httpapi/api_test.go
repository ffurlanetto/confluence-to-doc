package httpapi_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/ffurlanetto/confluence-to-doc/backend/internal/account"
	"github.com/ffurlanetto/confluence-to-doc/backend/internal/auth"
	"github.com/ffurlanetto/confluence-to-doc/backend/internal/confluence/fake"
	"github.com/ffurlanetto/confluence-to-doc/backend/internal/crypto"
	"github.com/ffurlanetto/confluence-to-doc/backend/internal/domain"
	"github.com/ffurlanetto/confluence-to-doc/backend/internal/export"
	"github.com/ffurlanetto/confluence-to-doc/backend/internal/httpapi"
	"github.com/ffurlanetto/confluence-to-doc/backend/internal/storage"
	"github.com/ffurlanetto/confluence-to-doc/backend/internal/store"
	"github.com/ffurlanetto/confluence-to-doc/backend/internal/testutil"
)

// headerAuth authenticates requests with an "X-Test-User" header naming a
// user subject. It replaces OIDC, which is covered by the auth package tests.
type headerAuth struct{ store *store.Store }

func (headerAuth) Login(w http.ResponseWriter, _ *http.Request)    { w.WriteHeader(http.StatusFound) }
func (headerAuth) Callback(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusFound) }
func (headerAuth) Logout(w http.ResponseWriter, _ *http.Request)   { w.WriteHeader(http.StatusOK) }
func (a headerAuth) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sub := r.Header.Get("X-Test-User")
		if sub == "" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		u, err := a.store.UpsertUser(r.Context(), "test", sub, sub+"@example.com", sub)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		next.ServeHTTP(w, r.WithContext(auth.WithUser(r.Context(), u)))
	})
}

type copyConverter struct{}

func (copyConverter) Convert(_ context.Context, html []byte, _ domain.Format, dst io.Writer) error {
	_, err := dst.Write(html)
	return err
}

type api struct {
	t   *testing.T
	srv *httptest.Server
}

func newAPI(t *testing.T) *api { return newAPIWithWorker(t, true) }

func newAPIWithWorker(t *testing.T, startWorker bool) *api {
	t.Helper()
	s := testutil.NewStore(t)

	f := fake.New("good-pat")
	f.AddPage(fake.Page{ID: "10", Title: "Guide utilisateur", Body: "<p>intro</p>"})
	f.AddPage(fake.Page{ID: "11", Title: "Installation", ParentID: "10", Body: "<p>install</p>"})
	confSrv := httptest.NewServer(f)
	t.Cleanup(confSrv.Close)
	confURL, _ := url.Parse(confSrv.URL)

	sealer, _ := crypto.NewSealer(bytes.Repeat([]byte{3}, 32))
	acc := account.NewService(s, sealer, confURL, 5*time.Second)
	blobs, _ := storage.NewLocal(t.TempDir())
	pool := export.NewPool(s, acc, copyConverter{}, blobs, export.WorkerConfig{
		Concurrency: 2, PollInterval: 20 * time.Millisecond, JobTimeout: time.Minute, Lease: 5 * time.Second,
		Retention: 48 * time.Hour, MaxPages: 50, MaxImageBytes: 1 << 20, ConfluenceWorkers: 2,
	})
	if startWorker {
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan struct{})
		go func() { pool.Run(ctx); close(done) }()
		t.Cleanup(func() { cancel(); <-done })
	}

	pub, _ := url.Parse("http://app.test")
	router := httpapi.NewRouter(httpapi.Deps{
		Auth:      headerAuth{store: s},
		Accounts:  acc,
		Exports:   export.NewService(s, blobs, export.Limits{MaxAttempts: 2, MaxActivePerUser: 3}, pool.Notify),
		Ready:     s.Ping,
		PublicURL: pub,
		Retention: 48 * time.Hour,
		MaxPages:  50,
	})
	srv := httptest.NewServer(router)
	t.Cleanup(srv.Close)
	return &api{t: t, srv: srv}
}

// do sends a request as user (empty = anonymous) and decodes the JSON body into out.
func (a *api) do(method, path, user string, body any, out any) *http.Response {
	a.t.Helper()
	var rdr io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rdr = bytes.NewReader(b)
	}
	req, _ := http.NewRequest(method, a.srv.URL+path, rdr)
	if user != "" {
		req.Header.Set("X-Test-User", user)
	}
	if method != http.MethodGet {
		req.Header.Set("X-CSRF-Protection", "1")
	}
	resp, err := a.srv.Client().Do(req)
	if err != nil {
		a.t.Fatal(err)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	if out != nil && len(data) > 0 {
		if err := json.Unmarshal(data, out); err != nil && strings.HasPrefix(resp.Header.Get("Content-Type"), "application/json") {
			a.t.Fatalf("decoding %s %s: %v (%s)", method, path, err, data)
		}
	}
	resp.Body = io.NopCloser(bytes.NewReader(data))
	return resp
}

type apiError struct {
	Error struct{ Code string } `json:"error"`
}

func TestHealth(t *testing.T) {
	a := newAPI(t)
	if r := a.do("GET", "/healthz", "", nil, nil); r.StatusCode != 200 {
		t.Fatalf("healthz = %d", r.StatusCode)
	}
	if r := a.do("GET", "/readyz", "", nil, nil); r.StatusCode != 200 {
		t.Fatalf("readyz = %d", r.StatusCode)
	}
	r := a.do("GET", "/healthz", "", nil, nil)
	if r.Header.Get("Content-Security-Policy") == "" || r.Header.Get("X-Content-Type-Options") != "nosniff" {
		t.Error("security headers missing")
	}
}

func TestAPIRequiresAuthentication(t *testing.T) {
	a := newAPI(t)
	if r := a.do("GET", "/api/me", "", nil, nil); r.StatusCode != http.StatusUnauthorized {
		t.Fatalf("anonymous = %d", r.StatusCode)
	}
}

func TestCSRFProtection(t *testing.T) {
	a := newAPI(t)
	req, _ := http.NewRequest("PUT", a.srv.URL+"/api/preferences/pat", strings.NewReader(`{"token":"good-pat"}`))
	req.Header.Set("X-Test-User", "alice")
	resp, err := a.srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("request without CSRF header = %d, want 403", resp.StatusCode)
	}
	req.Header.Set("X-CSRF-Protection", "1")
	req.Header.Set("Origin", "https://evil.example.com")
	req.Body = io.NopCloser(strings.NewReader(`{"token":"good-pat"}`))
	resp, _ = a.srv.Client().Do(req)
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("cross-origin request = %d, want 403", resp.StatusCode)
	}
}

func TestPreferencesAndPAT(t *testing.T) {
	a := newAPI(t)
	var prefs map[string]any
	a.do("GET", "/api/preferences", "alice", nil, &prefs)
	if prefs["hasPat"] != false || prefs["retentionHours"] != float64(48) {
		t.Fatalf("initial preferences: %v", prefs)
	}

	var e apiError
	if r := a.do("PUT", "/api/preferences/pat", "alice", map[string]string{"token": "bad"}, &e); r.StatusCode != http.StatusConflict || e.Error.Code != "pat_invalid" {
		t.Fatalf("invalid PAT: %d %+v", r.StatusCode, e)
	}
	var ok map[string]string
	if r := a.do("PUT", "/api/preferences/pat", "alice", map[string]string{"token": "good-pat"}, &ok); r.StatusCode != 200 || ok["confluenceUser"] != "John Doe" {
		t.Fatalf("valid PAT: %d %v", r.StatusCode, ok)
	}
	a.do("GET", "/api/preferences", "alice", nil, &prefs)
	if prefs["hasPat"] != true {
		t.Fatalf("hasPat should be true: %v", prefs)
	}
	if _, leaked := prefs["token"]; leaked {
		t.Fatal("token must never be returned")
	}

	a.do("PUT", "/api/preferences", "alice", map[string]string{"defaultFormat": "docx"}, &prefs)
	if prefs["defaultFormat"] != "docx" {
		t.Fatalf("default format: %v", prefs)
	}
	if r := a.do("PUT", "/api/preferences", "alice", map[string]string{"defaultFormat": "exe"}, nil); r.StatusCode != 400 {
		t.Fatalf("invalid format = %d", r.StatusCode)
	}
	if r := a.do("DELETE", "/api/preferences/pat", "alice", nil, nil); r.StatusCode != 204 {
		t.Fatalf("delete PAT = %d", r.StatusCode)
	}
}

func TestSearchRequiresPAT(t *testing.T) {
	a := newAPI(t)
	var e apiError
	if r := a.do("GET", "/api/confluence/pages?q=guide", "bob", nil, &e); r.StatusCode != http.StatusConflict || e.Error.Code != "pat_missing" {
		t.Fatalf("search without PAT: %d %+v", r.StatusCode, e)
	}
}

func TestExportFlow(t *testing.T) {
	a := newAPI(t)
	a.do("PUT", "/api/preferences/pat", "carol", map[string]string{"token": "good-pat"}, nil)

	var search struct {
		Results []struct{ ID, Title string }
	}
	a.do("GET", "/api/confluence/pages?q=guide", "carol", nil, &search)
	if len(search.Results) != 1 || search.Results[0].ID != "10" {
		t.Fatalf("search: %+v", search)
	}
	a.do("GET", "/api/confluence/pages?q="+url.QueryEscape("https://c.example.com/pages/viewpage.action?pageId=11"), "carol", nil, &search)
	if len(search.Results) != 1 || search.Results[0].Title != "Installation" {
		t.Fatalf("search by URL: %+v", search)
	}
	a.do("GET", "/api/confluence/pages/10/children", "carol", nil, &search)
	if len(search.Results) != 1 || search.Results[0].ID != "11" {
		t.Fatalf("children: %+v", search)
	}

	if r := a.do("POST", "/api/exports", "carol", map[string]any{"pageId": "999", "format": "pdf"}, nil); r.StatusCode != 404 {
		t.Fatalf("export of missing page = %d", r.StatusCode)
	}
	if r := a.do("POST", "/api/exports", "carol", map[string]any{"pageId": "10", "format": "odt"}, nil); r.StatusCode != 400 {
		t.Fatalf("export with invalid format = %d", r.StatusCode)
	}

	var created struct {
		ID, Status, Title string
	}
	r := a.do("POST", "/api/exports", "carol", map[string]any{"pageId": "10", "format": "docx", "includeChildren": true}, &created)
	if r.StatusCode != http.StatusAccepted || created.Title != "Guide utilisateur" || r.Header.Get("Location") == "" {
		t.Fatalf("create: %d %+v", r.StatusCode, created)
	}

	var got struct {
		Status     string
		PagesTotal int
		ExpiresAt  *time.Time
	}
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		a.do("GET", "/api/exports/"+created.ID, "carol", nil, &got)
		if got.Status == "succeeded" || got.Status == "failed" {
			break
		}
		time.Sleep(25 * time.Millisecond)
	}
	if got.Status != "succeeded" || got.PagesTotal != 2 || got.ExpiresAt == nil {
		t.Fatalf("export did not succeed: %+v", got)
	}

	var list struct{ Exports []struct{ ID string } }
	a.do("GET", "/api/exports", "carol", nil, &list)
	if len(list.Exports) != 1 {
		t.Fatalf("list: %+v", list)
	}
	a.do("GET", "/api/exports", "mallory", nil, &list)
	if len(list.Exports) != 0 {
		t.Fatal("another user must not see carol's exports")
	}

	r = a.do("GET", "/api/exports/"+created.ID+"/download", "carol", nil, nil)
	body, _ := io.ReadAll(r.Body)
	if r.StatusCode != 200 || !strings.Contains(string(body), "install") {
		t.Fatalf("download: %d", r.StatusCode)
	}
	if cd := r.Header.Get("Content-Disposition"); !strings.Contains(cd, "attachment") || !strings.Contains(cd, ".docx") {
		t.Errorf("content disposition = %q", cd)
	}
	if r := a.do("GET", "/api/exports/"+created.ID+"/download", "mallory", nil, nil); r.StatusCode != 404 {
		t.Fatalf("download by another user = %d, want 404", r.StatusCode)
	}
	if r := a.do("GET", "/api/exports/not-a-uuid", "carol", nil, nil); r.StatusCode != 404 {
		t.Fatalf("invalid id = %d", r.StatusCode)
	}

	if r := a.do("DELETE", "/api/exports/"+created.ID, "carol", nil, nil); r.StatusCode != 204 {
		t.Fatalf("delete = %d", r.StatusCode)
	}
	if r := a.do("GET", "/api/exports/"+created.ID+"/download", "carol", nil, nil); r.StatusCode != 404 {
		t.Fatalf("download after delete = %d", r.StatusCode)
	}
}

func TestPerUserQuota(t *testing.T) {
	a := newAPIWithWorker(t, false) // nothing drains the queue
	a.do("PUT", "/api/preferences/pat", "dave", map[string]string{"token": "good-pat"}, nil)
	for i := range 3 {
		if r := a.do("POST", "/api/exports", "dave", map[string]any{"pageId": "10", "format": "pdf"}, nil); r.StatusCode != http.StatusAccepted {
			t.Fatalf("export %d = %d", i, r.StatusCode)
		}
	}
	var e apiError
	if r := a.do("POST", "/api/exports", "dave", map[string]any{"pageId": "10", "format": "pdf"}, &e); r.StatusCode != http.StatusTooManyRequests || e.Error.Code != "too_many_exports" {
		t.Fatalf("4th export = %d %+v, want 429", r.StatusCode, e)
	}
	// The quota is per user.
	a.do("PUT", "/api/preferences/pat", "erin", map[string]string{"token": "good-pat"}, nil)
	if r := a.do("POST", "/api/exports", "erin", map[string]any{"pageId": "10", "format": "pdf"}, nil); r.StatusCode != http.StatusAccepted {
		t.Fatalf("other user export = %d", r.StatusCode)
	}
}
