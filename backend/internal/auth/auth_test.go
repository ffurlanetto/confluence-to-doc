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

func (m *memRepo) CreateSession(_ context.Context, h []byte, sess domain.Session) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.sessions[hex.EncodeToString(h)] = sess
	return nil
}

func (m *memRepo) ClaimRevalidation(_ context.Context, h []byte, next time.Time) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	s, ok := m.sessions[hex.EncodeToString(h)]
	if !ok || s.RevalidateAt == nil || time.Now().Before(*s.RevalidateAt) {
		return false, nil
	}
	s.RevalidateAt = &next
	m.sessions[hex.EncodeToString(h)] = s
	return true, nil
}

func (m *memRepo) StoreRefreshToken(_ context.Context, h, rt []byte) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	s := m.sessions[hex.EncodeToString(h)]
	s.RefreshToken = rt
	m.sessions[hex.EncodeToString(h)] = s
	return nil
}

func (m *memRepo) DeleteSessionsBySID(_ context.Context, sid string) (int64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var n int64
	for k, s := range m.sessions {
		if s.SID == sid {
			delete(m.sessions, k)
			n++
		}
	}
	return n, nil
}

func (m *memRepo) DeleteSessionsBySubject(_ context.Context, iss, sub string) (int64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	u, ok := m.users[iss+"|"+sub]
	if !ok {
		return 0, nil
	}
	var n int64
	for k, s := range m.sessions {
		if s.UserID == u.ID {
			delete(m.sessions, k)
			n++
		}
	}
	return n, nil
}

func (m *memRepo) sessionCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.sessions)
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

func (m *memRepo) RecordLogin(_ context.Context, id uuid.UUID, isAdmin bool) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, u := range m.users {
		if u.ID == id {
			u.IsAdmin = isAdmin
		}
	}
	return nil
}

// events collects audit events.
type events struct {
	mu   sync.Mutex
	list []domain.AuditEvent
}

func (e *events) Record(_ context.Context, ev domain.AuditEvent) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.list = append(e.list, ev)
}

func (e *events) actions() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	var out []string
	for _, ev := range e.list {
		out = append(out, ev.Action+":"+string(ev.Outcome))
	}
	return out
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
	events *events
	idp    *oidcmock.Provider
}

// option tweaks the provider or the authenticator configuration.
type option func(*oidcmock.Provider, *auth.Config)

func newHarness(t *testing.T, opts ...option) *harness {
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

	cfg := auth.Config{
		IssuerURL: idpSrv.URL, ClientID: "app", ClientSecret: "s3cret", PublicURL: pub, SessionTTL: time.Hour,
		GroupsClaim: "groups",
	}
	for _, o := range opts {
		o(idp, &cfg)
	}
	ev := &events{}
	a, err := auth.New(context.Background(), cfg, repo, sealer, ev)
	if err != nil {
		t.Fatal(err)
	}
	mux.HandleFunc("GET /auth/login", a.Login)
	mux.HandleFunc("GET /auth/callback", a.Callback)
	mux.HandleFunc("POST /auth/logout", a.Logout)
	mux.HandleFunc("POST /auth/backchannel-logout", a.BackchannelLogout)
	mux.Handle("GET /api/me", a.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(auth.UserFrom(r.Context()).Email))
	})))
	mux.HandleFunc("GET /done", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("landed")) })

	jar, _ := cookiejar.New(nil)
	return &harness{app: app, client: &http.Client{Jar: jar}, repo: repo, events: ev, idp: idp}
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
	if got := strings.Join(h.events.actions(), " "); got != "auth.login:success auth.logout:success" {
		t.Fatalf("audit trail = %q", got)
	}
}

func (h *harness) login(t *testing.T) *domain.User {
	t.Helper()
	resp, err := h.client.Get(h.app.URL + "/auth/login?return_to=/done")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.Request.URL.Path != "/done" {
		t.Fatalf("login flow ended on %s (%d)", resp.Request.URL, resp.StatusCode)
	}
	for _, u := range h.repo.users {
		return u
	}
	t.Fatal("no user created")
	return nil
}

func withGroups(claims map[string]any, claim string, adminGroups ...string) option {
	return func(p *oidcmock.Provider, c *auth.Config) {
		p.User.Claims = claims
		c.GroupsClaim = claim
		c.AdminGroups = adminGroups
	}
}

func TestAdminRoleFromGroups(t *testing.T) {
	cases := []struct {
		name  string
		opt   option
		admin bool
	}{
		{"member of an admin group", withGroups(map[string]any{"groups": []any{"staff", "c2d-admins"}}, "groups", "c2d-admins"), true},
		{"not a member", withGroups(map[string]any{"groups": []any{"staff"}}, "groups", "c2d-admins"), false},
		{"single string claim", withGroups(map[string]any{"role": "c2d-admins"}, "role", "c2d-admins"), true},
		{"nested claim", withGroups(map[string]any{"realm_access": map[string]any{"roles": []any{"admin"}}}, "realm_access.roles", "admin"), true},
		{"namespaced claim with dots", withGroups(map[string]any{"https://acme.example/groups": []any{"admin"}}, "https://acme.example/groups", "admin"), true},
		{"no admin group configured", withGroups(map[string]any{"groups": []any{"c2d-admins"}}, "groups"), false},
		{"claim missing", withGroups(map[string]any{}, "groups", "c2d-admins"), false},
		{"claim only in UserInfo", func(p *oidcmock.Provider, c *auth.Config) {
			withGroups(map[string]any{"groups": []any{"c2d-admins"}}, "groups", "c2d-admins")(p, c)
			p.ClaimsInUserInfoOnly = true
		}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t, tc.opt)
			if u := h.login(t); u.IsAdmin != tc.admin {
				t.Fatalf("IsAdmin = %v, want %v", u.IsAdmin, tc.admin)
			}
		})
	}
}

func TestAdminRoleIsRevokedAtNextLogin(t *testing.T) {
	var idp *oidcmock.Provider
	h := newHarness(t, withGroups(map[string]any{"groups": []any{"c2d-admins"}}, "groups", "c2d-admins"),
		func(p *oidcmock.Provider, _ *auth.Config) { idp = p })
	if !h.login(t).IsAdmin {
		t.Fatal("expected admin")
	}
	idp.User.Claims = map[string]any{"groups": []any{}}
	if h.login(t).IsAdmin {
		t.Fatal("admin role kept after leaving the group")
	}
}

func TestBlockedAccountCannotSignIn(t *testing.T) {
	h := newHarness(t)
	u := h.login(t)
	now := time.Now()
	u.BlockedAt = &now
	h.repo.sessions = map[string]domain.Session{}

	code, body := h.get(t, "/auth/login?return_to=/done")
	if code != http.StatusForbidden || !strings.Contains(body, "blocked") {
		t.Fatalf("sign-in of a blocked account = %d %q, want 403", code, body)
	}
	if len(h.repo.sessions) != 0 {
		t.Fatal("a session was opened for a blocked account")
	}
	if got := h.events.actions(); got[len(got)-1] != "auth.login:denied" {
		t.Fatalf("audit trail = %v, want the refusal recorded", got)
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
	if got := strings.Join(h.events.actions(), " "); got != "auth.login:failure" {
		t.Fatalf("failed login not audited: %q", got)
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
