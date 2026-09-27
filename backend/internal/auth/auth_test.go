package auth_test

import (
	"bytes"
	"context"
	"encoding/hex"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/ffurlanetto/confluence-to-doc/backend/internal/auth"
	"github.com/ffurlanetto/confluence-to-doc/backend/internal/auth/oidcmock"
	"github.com/ffurlanetto/confluence-to-doc/backend/internal/crypto"
	"github.com/ffurlanetto/confluence-to-doc/backend/internal/domain"
)

type memRepo struct {
	mu       sync.Mutex
	users    map[string]*domain.User
	sessions map[string]domain.Session
}

func newMemRepo() *memRepo {
	return &memRepo{users: map[string]*domain.User{}, sessions: map[string]domain.Session{}}
}

func (m *memRepo) UpsertUser(_ context.Context, iss, sub, email, name string) (*domain.User, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	u, ok := m.users[iss+"|"+sub]
	if !ok {
		u = &domain.User{ID: uuid.New(), Issuer: iss, Subject: sub}
		m.users[iss+"|"+sub] = u
	}
	u.Email, u.Name = email, name
	return u, nil
}

func (m *memRepo) CreateSession(_ context.Context, h []byte, uid uuid.UUID, exp time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.sessions[hex.EncodeToString(h)] = domain.Session{UserID: uid, ExpiresAt: exp}
	return nil
}

func (m *memRepo) SessionUser(_ context.Context, h []byte) (*domain.User, *domain.Session, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	s, ok := m.sessions[hex.EncodeToString(h)]
	if !ok || time.Now().After(s.ExpiresAt) {
		return nil, nil, domain.ErrNotFound
	}
	for _, u := range m.users {
		if u.ID == s.UserID {
			return u, &s, nil
		}
	}
	return nil, nil, domain.ErrNotFound
}

func (m *memRepo) DeleteSession(_ context.Context, h []byte) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.sessions, hex.EncodeToString(h))
	return nil
}

type harness struct {
	app    *httptest.Server
	client *http.Client
	repo   *memRepo
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	idp := oidcmock.New("app", "s3cret", oidcmock.User{Subject: "u-1", Email: "alice@example.com", Name: "Alice"})
	idpSrv := httptest.NewServer(idp)
	t.Cleanup(idpSrv.Close)
	idp.Issuer = idpSrv.URL

	repo := newMemRepo()
	sealer, _ := crypto.NewSealer(bytes.Repeat([]byte{9}, 32))

	mux := http.NewServeMux()
	app := httptest.NewServer(mux)
	t.Cleanup(app.Close)
	pub, _ := url.Parse(app.URL)

	a, err := auth.New(context.Background(), auth.Config{
		IssuerURL: idpSrv.URL, ClientID: "app", ClientSecret: "s3cret", PublicURL: pub, SessionTTL: time.Hour,
	}, repo, sealer)
	if err != nil {
		t.Fatal(err)
	}
	mux.HandleFunc("GET /auth/login", a.Login)
	mux.HandleFunc("GET /auth/callback", a.Callback)
	mux.HandleFunc("POST /auth/logout", a.Logout)
	mux.Handle("GET /api/me", a.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(auth.UserFrom(r.Context()).Email))
	})))
	mux.HandleFunc("GET /done", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("landed")) })

	jar, _ := cookiejar.New(nil)
	return &harness{app: app, client: &http.Client{Jar: jar}, repo: repo}
}

func (h *harness) get(t *testing.T, path string) (int, string) {
	t.Helper()
	resp, err := h.client.Get(h.app.URL + path)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(body)
}

func TestLoginFlow(t *testing.T) {
	h := newHarness(t)
	if code, _ := h.get(t, "/api/me"); code != http.StatusUnauthorized {
		t.Fatalf("anonymous /api/me = %d, want 401", code)
	}

	resp, err := h.client.Get(h.app.URL + "/auth/login?return_to=/done")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK || resp.Request.URL.Path != "/done" {
		t.Fatalf("login flow ended on %s (%d)", resp.Request.URL, resp.StatusCode)
	}

	resp, err = h.client.Get(h.app.URL + "/api/me")
	if err != nil {
		t.Fatal(err)
	}
	buf := new(bytes.Buffer)
	_, _ = buf.ReadFrom(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK || buf.String() != "alice@example.com" {
		t.Fatalf("/api/me = %d %q", resp.StatusCode, buf.String())
	}
	if len(h.repo.sessions) != 1 {
		t.Fatalf("sessions = %d", len(h.repo.sessions))
	}

	resp, err = h.client.Post(h.app.URL+"/auth/logout", "application/json", nil)
	if err != nil {
		t.Fatal(err)
	}
	buf.Reset()
	_, _ = buf.ReadFrom(resp.Body)
	resp.Body.Close()
	if !strings.Contains(buf.String(), "logoutUrl") {
		t.Errorf("logout response should carry the IdP logout URL: %s", buf.String())
	}
	if len(h.repo.sessions) != 0 {
		t.Fatal("session not deleted on logout")
	}
	if code, _ := h.get(t, "/api/me"); code != http.StatusUnauthorized {
		t.Fatalf("after logout /api/me = %d, want 401", code)
	}
}

func TestCallbackRejectsForgedState(t *testing.T) {
	h := newHarness(t)
	// Start a login but stop before following the IdP redirect.
	noFollow := &http.Client{Jar: h.client.Jar, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := noFollow.Get(h.app.URL + "/auth/login")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	resp, err = noFollow.Get(h.app.URL + "/auth/callback?code=x&state=forged")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("forged state accepted: %d", resp.StatusCode)
	}
}

func TestCallbackWithoutFlowCookie(t *testing.T) {
	h := newHarness(t)
	if code, _ := h.get(t, "/auth/callback?code=x&state=y"); code != http.StatusUnauthorized {
		t.Fatalf("callback without flow cookie = %d, want 401", code)
	}
}

func TestSafeReturnTo(t *testing.T) {
	cases := map[string]string{
		"":                     "/",
		"/exports?x=1":         "/exports?x=1",
		"//evil.com":           "/",
		"/\\evil.com":          "/",
		"https://evil.com/x":   "/",
		"javascript:alert(1)":  "/",
		"/ok\r\nSet-Cookie: x": "/",
	}
	for in, want := range cases {
		if got := auth.SafeReturnTo(in); got != want {
			t.Errorf("SafeReturnTo(%q) = %q, want %q", in, got, want)
		}
	}
}
