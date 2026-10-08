package auth_test

import (
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/ffurlanetto/confluence-to-doc/backend/internal/auth"
	"github.com/ffurlanetto/confluence-to-doc/backend/internal/auth/oidcmock"
)

func revalidateEvery(d time.Duration) option {
	return func(_ *oidcmock.Provider, c *auth.Config) { c.RevalidateInterval = d }
}

func TestRevalidationEndsARevokedSession(t *testing.T) {
	h := newHarness(t, revalidateEvery(time.Millisecond))
	h.login(t)
	time.Sleep(5 * time.Millisecond)
	if code, _ := h.get(t, "/api/me"); code != http.StatusOK {
		t.Fatalf("/api/me = %d before revocation", code)
	}
	if h.idp.Refreshes != 1 {
		t.Fatalf("refreshes = %d, want the due re-check to have happened", h.idp.Refreshes)
	}

	// The account is disabled at the provider: the next re-check ends it.
	h.idp.Revoked = true
	time.Sleep(5 * time.Millisecond)
	if code, _ := h.get(t, "/api/me"); code != http.StatusUnauthorized {
		t.Fatalf("/api/me = %d after revocation, want 401", code)
	}
	if h.repo.sessionCount() != 0 {
		t.Error("the revoked session must be deleted")
	}
	if got := strings.Join(h.events.actions(), " "); !strings.HasSuffix(got, "auth.session_revoked:success") {
		t.Errorf("revocation not audited: %s", got)
	}
}

func TestRevalidationRefreshesTheAdminRole(t *testing.T) {
	h := newHarness(t, revalidateEvery(time.Millisecond),
		withGroups(map[string]any{"groups": []any{"c2d-admins"}}, "groups", "c2d-admins"))
	if !h.login(t).IsAdmin {
		t.Fatal("expected an admin")
	}
	h.idp.User.Claims = map[string]any{"groups": []any{}}
	time.Sleep(5 * time.Millisecond)
	h.get(t, "/api/me")
	for _, u := range h.repo.users {
		if u.IsAdmin {
			t.Fatal("the admin role must follow the provider at re-check, not wait for the next sign-in")
		}
	}
}

func TestNoRevalidationWhenDisabled(t *testing.T) {
	h := newHarness(t, revalidateEvery(0))
	h.login(t)
	h.idp.Revoked = true
	if code, _ := h.get(t, "/api/me"); code != http.StatusOK || h.idp.Refreshes != 0 {
		t.Fatalf("code=%d refreshes=%d", code, h.idp.Refreshes)
	}
}

func (h *harness) backchannel(t *testing.T, token string) int {
	t.Helper()
	resp, err := http.PostForm(h.app.URL+"/auth/backchannel-logout", url.Values{"logout_token": {token}})
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	return resp.StatusCode
}

func TestBackchannelLogout(t *testing.T) {
	h := newHarness(t, func(p *oidcmock.Provider, _ *auth.Config) { p.SessionID = "idp-session-1" })
	h.login(t)

	// Tokens the endpoint must refuse.
	stranger := oidcmock.New("app", "s3cret", oidcmock.User{Subject: "u-1"})
	stranger.Issuer = h.idp.Issuer
	forged, _ := stranger.LogoutToken(nil)
	refusals := map[string]map[string]any{
		"not a logout token": {"events": nil},
		"carries a nonce":    {"nonce": "n"},
		"stale":              {"iat": time.Now().Add(-time.Hour).Unix()},
		"another audience":   {"aud": "someone-else"},
		"no sid nor sub":     {"sid": nil, "sub": nil},
	}
	for name, claims := range refusals {
		token, _ := h.idp.LogoutToken(claims)
		if code := h.backchannel(t, token); code != http.StatusBadRequest {
			t.Errorf("%s: %d, want 400", name, code)
		}
	}
	for name, token := range map[string]string{"forged signature": forged, "garbage": "not-a-jwt"} {
		if code := h.backchannel(t, token); code != http.StatusBadRequest {
			t.Errorf("%s: %d, want 400", name, code)
		}
	}
	if code, _ := h.get(t, "/api/me"); code != http.StatusOK {
		t.Fatal("refused tokens must not end the session")
	}

	token, _ := h.idp.LogoutToken(nil)
	if code := h.backchannel(t, token); code != http.StatusOK {
		t.Fatalf("valid logout token: %d", code)
	}
	if code, _ := h.get(t, "/api/me"); code != http.StatusUnauthorized {
		t.Fatalf("/api/me = %d after back-channel logout, want 401", code)
	}
	if got := strings.Join(h.events.actions(), " "); !strings.HasSuffix(got, "auth.backchannel_logout:success") {
		t.Errorf("logout not audited: %s", got)
	}
}

func TestBackchannelLogoutBySubject(t *testing.T) {
	h := newHarness(t)
	h.login(t)
	token, _ := h.idp.LogoutToken(map[string]any{"sid": nil})
	if code := h.backchannel(t, token); code != http.StatusOK {
		t.Fatalf("logout by subject: %d", code)
	}
	if h.repo.sessionCount() != 0 {
		t.Error("every session of the subject must end")
	}
}
