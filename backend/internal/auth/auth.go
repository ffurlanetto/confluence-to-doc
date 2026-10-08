// Package auth protects the application with OAuth2 / OpenID Connect.
//
// It implements the "Backend For Frontend" pattern: the Go server is a
// confidential OAuth2 client running the Authorization Code flow with PKCE.
// Tokens never reach the browser; the SPA only holds an opaque, HttpOnly,
// SameSite session cookie whose SHA-256 is stored server side.
package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	"github.com/google/uuid"
	"golang.org/x/oauth2"

	"github.com/ffurlanetto/confluence-to-doc/backend/internal/audit"
	"github.com/ffurlanetto/confluence-to-doc/backend/internal/crypto"
	"github.com/ffurlanetto/confluence-to-doc/backend/internal/domain"
)

const flowTTL = 10 * time.Minute

type Repo interface {
	UpsertUser(ctx context.Context, issuer, subject, email, name string) (*domain.User, error)
	CreateSession(ctx context.Context, tokenHash []byte, sess domain.Session) error
	SessionUser(ctx context.Context, tokenHash []byte) (*domain.User, *domain.Session, error)
	DeleteSession(ctx context.Context, tokenHash []byte) error
	RecordLogin(ctx context.Context, userID uuid.UUID, isAdmin bool) error

	ClaimRevalidation(ctx context.Context, tokenHash []byte, next time.Time) (bool, error)
	StoreRefreshToken(ctx context.Context, tokenHash, refreshToken []byte) error
	DeleteSessionsBySID(ctx context.Context, sid string) (int64, error)
	DeleteSessionsBySubject(ctx context.Context, issuer, subject string) (int64, error)
}

// Auditor records security events (implemented by audit.Recorder).
type Auditor interface {
	Record(ctx context.Context, e domain.AuditEvent)
}

type Config struct {
	IssuerURL    string
	ClientID     string
	ClientSecret string
	Scopes       []string
	PublicURL    *url.URL
	SessionTTL   time.Duration
	SecureCookie bool
	// GroupsClaim is the claim listing the user's groups or roles, as a
	// dotted path for nested claims (e.g. "realm_access.roles").
	GroupsClaim string
	// AdminGroups grants the admin role to members of any of these groups.
	// Empty means nobody is an administrator.
	AdminGroups []string
	// RevalidateInterval is how often a session is checked with the
	// provider (through its refresh token): a disabled account or an ended
	// provider session then ends ours. 0 disables the check.
	RevalidateInterval time.Duration
}

type Authenticator struct {
	cfg           Config
	repo          Repo
	sealer        *crypto.Sealer
	audit         Auditor
	provider      *oidc.Provider
	oauth         oauth2.Config
	verifier      *oidc.IDTokenVerifier
	issuer        string
	endSessionURL string
	sessionCookie string
	now           func() time.Time
}

// New performs OIDC discovery against the issuer.
func New(ctx context.Context, cfg Config, repo Repo, sealer *crypto.Sealer, auditor Auditor) (*Authenticator, error) {
	provider, err := oidc.NewProvider(ctx, cfg.IssuerURL)
	if err != nil {
		return nil, fmt.Errorf("OIDC discovery for %s: %w", cfg.IssuerURL, err)
	}
	var extra struct {
		EndSession string `json:"end_session_endpoint"`
	}
	_ = provider.Claims(&extra)

	scopes := cfg.Scopes
	if len(scopes) == 0 {
		scopes = []string{oidc.ScopeOpenID, "profile", "email"}
	}
	a := &Authenticator{
		cfg:      cfg,
		repo:     repo,
		sealer:   sealer,
		audit:    auditor,
		provider: provider,
		oauth: oauth2.Config{
			ClientID:     cfg.ClientID,
			ClientSecret: cfg.ClientSecret,
			Endpoint:     provider.Endpoint(),
			RedirectURL:  cfg.PublicURL.String() + "/auth/callback",
			Scopes:       scopes,
		},
		verifier:      provider.Verifier(&oidc.Config{ClientID: cfg.ClientID}),
		issuer:        cfg.IssuerURL,
		endSessionURL: extra.EndSession,
		sessionCookie: "c2d_session",
		now:           time.Now,
	}
	if cfg.SecureCookie {
		// The __Host- prefix forces Secure, Path=/ and no Domain attribute.
		a.sessionCookie = "__Host-c2d_session"
	}
	return a, nil
}

// flowState is kept in an encrypted, short-lived cookie between the
// redirect to the IdP and the callback (no server-side state needed).
type flowState struct {
	State    string    `json:"s"`
	Nonce    string    `json:"n"`
	Verifier string    `json:"v"`
	ReturnTo string    `json:"r"`
	Expires  time.Time `json:"e"`
}

const flowCookie = "c2d_oauth_flow"

// Login redirects the browser to the identity provider.
func (a *Authenticator) Login(w http.ResponseWriter, r *http.Request) {
	fs := flowState{
		State:    randomToken(),
		Nonce:    randomToken(),
		Verifier: oauth2.GenerateVerifier(),
		ReturnTo: SafeReturnTo(r.URL.Query().Get("return_to")),
		Expires:  a.now().Add(flowTTL),
	}
	raw, _ := json.Marshal(fs)
	sealed, err := a.sealer.Seal(raw, []byte(flowCookie))
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	// Secure is only false for plain-http local development (PUBLIC_URL=http://...).
	http.SetCookie(w, &http.Cookie{ //nolint:gosec // G124: Secure comes from configuration
		Name: flowCookie, Value: base64.RawURLEncoding.EncodeToString(sealed),
		Path: "/auth/", MaxAge: int(flowTTL.Seconds()), HttpOnly: true, Secure: a.cfg.SecureCookie,
		SameSite: http.SameSiteLaxMode, // must survive the top-level redirect back from the IdP
	})
	http.Redirect(w, r, a.oauth.AuthCodeURL(fs.State, oidc.Nonce(fs.Nonce), oauth2.S256ChallengeOption(fs.Verifier)), http.StatusFound)
}

// Callback completes the Authorization Code flow and opens a session.
func (a *Authenticator) Callback(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	fs, err := a.readFlow(r)
	clearCookie(w, flowCookie, "/auth/", a.cfg.SecureCookie)
	if err != nil {
		a.fail(w, r, "invalid or expired login attempt", err)
		return
	}
	q := r.URL.Query()
	if e := q.Get("error"); e != "" {
		a.fail(w, r, "login refused by the identity provider", errors.New(e))
		return
	}
	if subtle.ConstantTimeCompare([]byte(q.Get("state")), []byte(fs.State)) != 1 {
		a.fail(w, r, "invalid login state", errors.New("state mismatch"))
		return
	}
	tok, err := a.oauth.Exchange(ctx, q.Get("code"), oauth2.VerifierOption(fs.Verifier))
	if err != nil {
		a.fail(w, r, "login failed", err)
		return
	}
	rawID, _ := tok.Extra("id_token").(string)
	idToken, err := a.verifier.Verify(ctx, rawID)
	if err != nil {
		a.fail(w, r, "login failed", err)
		return
	}
	if subtle.ConstantTimeCompare([]byte(idToken.Nonce), []byte(fs.Nonce)) != 1 {
		a.fail(w, r, "login failed", errors.New("nonce mismatch"))
		return
	}
	var claims struct {
		Email             string `json:"email"`
		Name              string `json:"name"`
		PreferredUsername string `json:"preferred_username"`
	}
	var rawClaims map[string]any
	if err := idToken.Claims(&claims); err != nil {
		a.fail(w, r, "login failed", err)
		return
	}
	if err := idToken.Claims(&rawClaims); err != nil {
		a.fail(w, r, "login failed", err)
		return
	}
	name := claims.Name
	if name == "" {
		name = claims.PreferredUsername
	}
	user, err := a.repo.UpsertUser(ctx, idToken.Issuer, idToken.Subject, claims.Email, name)
	if err != nil {
		a.fail(w, r, "login failed", err)
		return
	}
	if user.Blocked() {
		// Blocked from the administration console: the identity provider
		// still vouches for the person, this application does not let them in.
		slog.WarnContext(ctx, "blocked account refused at sign-in", "user_id", user.ID)
		actorID, email := audit.Actor(user)
		a.audit.Record(ctx, domain.AuditEvent{
			ActorID: actorID, ActorEmail: email, Action: audit.ActionLogin, Outcome: domain.AuditDenied,
			TargetType: audit.TargetUser, TargetID: user.ID.String(), Details: map[string]any{"reason": "blocked"},
		})
		http.Error(w, "Your account has been blocked. Contact your administrator.", http.StatusForbidden)
		return
	}
	user.IsAdmin = a.isAdmin(ctx, rawClaims, tok)
	if err := a.repo.RecordLogin(ctx, user.ID, user.IsAdmin); err != nil {
		a.fail(w, r, "login failed", err)
		return
	}

	token := randomToken()
	expires := a.now().Add(a.cfg.SessionTTL)
	sess, err := a.newSession(hashToken(token), user.ID, expires, rawClaims, tok)
	if err != nil {
		a.fail(w, r, "login failed", err)
		return
	}
	if err := a.repo.CreateSession(ctx, hashToken(token), sess); err != nil {
		a.fail(w, r, "login failed", err)
		return
	}
	http.SetCookie(w, &http.Cookie{ //nolint:gosec // G124: Secure comes from configuration
		Name: a.sessionCookie, Value: token, Path: "/", Expires: expires,
		HttpOnly: true, Secure: a.cfg.SecureCookie, SameSite: http.SameSiteLaxMode,
	})
	slog.InfoContext(ctx, "user logged in", "user_id", user.ID)
	actorID, email := audit.Actor(user)
	a.audit.Record(ctx, domain.AuditEvent{
		ActorID: actorID, ActorEmail: email, Action: audit.ActionLogin, Outcome: domain.AuditSuccess,
		TargetType: audit.TargetUser, TargetID: user.ID.String(),
		Details: map[string]any{"admin": user.IsAdmin, "subject": user.Subject},
	})
	http.Redirect(w, r, fs.ReturnTo, http.StatusFound)
}

// isAdmin reports whether the user belongs to one of the admin groups. The
// groups claim is read from the ID token, or from the UserInfo endpoint when
// the provider only exposes it there (a common Keycloak setup).
func (a *Authenticator) isAdmin(ctx context.Context, idClaims map[string]any, tok *oauth2.Token) bool {
	if len(a.cfg.AdminGroups) == 0 {
		return false
	}
	groups, found := claimValues(idClaims, a.cfg.GroupsClaim)
	if !found {
		info, err := a.provider.UserInfo(ctx, oauth2.StaticTokenSource(tok))
		var infoClaims map[string]any
		if err == nil {
			err = info.Claims(&infoClaims)
		}
		if err != nil {
			slog.WarnContext(ctx, "groups claim not in the ID token and UserInfo unavailable", "claim", a.cfg.GroupsClaim, "err", err)
			return false
		}
		groups, found = claimValues(infoClaims, a.cfg.GroupsClaim)
		if !found {
			slog.WarnContext(ctx, "groups claim not found in the ID token nor UserInfo", "claim", a.cfg.GroupsClaim)
			return false
		}
	}
	for _, g := range groups {
		for _, admin := range a.cfg.AdminGroups {
			if g == admin {
				return true
			}
		}
	}
	return false
}

// claimValues reads a claim holding a string or a list of strings. path is
// first looked up as a whole (namespaced claims such as
// "https://example.com/groups" contain dots), then as a dotted path into
// nested objects.
func claimValues(claims map[string]any, path string) ([]string, bool) {
	v, ok := claims[path]
	if !ok {
		var cur any = claims
		for _, part := range strings.Split(path, ".") {
			m, isMap := cur.(map[string]any)
			if !isMap {
				return nil, false
			}
			if cur, ok = m[part]; !ok {
				return nil, false
			}
		}
		v = cur
	}
	switch t := v.(type) {
	case string:
		return []string{t}, true
	case []any:
		out := make([]string, 0, len(t))
		for _, item := range t {
			if s, isString := item.(string); isString {
				out = append(out, s)
			}
		}
		return out, true
	}
	return nil, false
}

// Logout destroys the session and returns the IdP logout URL (if any) so the
// SPA can also end the single sign-on session.
func (a *Authenticator) Logout(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(a.sessionCookie); err == nil && c.Value != "" && len(c.Value) <= 256 {
		if user, _, err := a.repo.SessionUser(r.Context(), hashToken(c.Value)); err == nil {
			actorID, email := audit.Actor(user)
			a.audit.Record(r.Context(), domain.AuditEvent{
				ActorID: actorID, ActorEmail: email, Action: audit.ActionLogout, Outcome: domain.AuditSuccess,
				TargetType: audit.TargetUser, TargetID: user.ID.String(),
			})
		}
		if err := a.repo.DeleteSession(r.Context(), hashToken(c.Value)); err != nil {
			slog.ErrorContext(r.Context(), "deleting session", "err", err)
		}
	}
	clearCookie(w, a.sessionCookie, "/", a.cfg.SecureCookie)
	resp := map[string]string{}
	if a.endSessionURL != "" {
		u, err := url.Parse(a.endSessionURL)
		if err == nil {
			q := u.Query()
			q.Set("client_id", a.cfg.ClientID)
			q.Set("post_logout_redirect_uri", a.cfg.PublicURL.String()+"/")
			u.RawQuery = q.Encode()
			resp["logoutUrl"] = u.String()
		}
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(resp)
}

type ctxKey struct{}

// Middleware rejects requests without a valid session with 401 and stores the
// authenticated user in the request context.
func (a *Authenticator) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := r.Cookie(a.sessionCookie)
		if err != nil || c.Value == "" || len(c.Value) > 256 {
			unauthorized(w)
			return
		}
		hash := hashToken(c.Value)
		user, sess, err := a.repo.SessionUser(r.Context(), hash)
		if errors.Is(err, domain.ErrNotFound) {
			unauthorized(w)
			return
		}
		if err != nil {
			slog.ErrorContext(r.Context(), "loading session", "err", err)
			http.Error(w, `{"error":"internal error"}`, http.StatusInternalServerError)
			return
		}
		if sess.RevalidateAt != nil && !a.now().Before(*sess.RevalidateAt) && !a.revalidate(r.Context(), hash, user, sess) {
			clearCookie(w, a.sessionCookie, "/", a.cfg.SecureCookie)
			unauthorized(w)
			return
		}
		next.ServeHTTP(w, r.WithContext(WithUser(r.Context(), user)))
	})
}

// WithUser returns a context carrying the authenticated user.
func WithUser(ctx context.Context, u *domain.User) context.Context {
	return context.WithValue(ctx, ctxKey{}, u)
}

// UserFrom returns the authenticated user; it panics if the route is not
// protected by Middleware (a programming error).
func UserFrom(ctx context.Context) *domain.User {
	u, ok := ctx.Value(ctxKey{}).(*domain.User)
	if !ok {
		panic("auth: no user in context; route not protected by auth.Middleware")
	}
	return u
}

func (a *Authenticator) readFlow(r *http.Request) (*flowState, error) {
	c, err := r.Cookie(flowCookie)
	if err != nil {
		return nil, err
	}
	sealed, err := base64.RawURLEncoding.DecodeString(c.Value)
	if err != nil {
		return nil, err
	}
	raw, err := a.sealer.Open(sealed, []byte(flowCookie))
	if err != nil {
		return nil, err
	}
	var fs flowState
	if err := json.Unmarshal(raw, &fs); err != nil {
		return nil, err
	}
	if a.now().After(fs.Expires) {
		return nil, errors.New("login flow expired")
	}
	return &fs, nil
}

func (a *Authenticator) fail(w http.ResponseWriter, r *http.Request, msg string, err error) {
	slog.WarnContext(r.Context(), "authentication failed", "reason", msg, "err", err)
	a.audit.Record(r.Context(), domain.AuditEvent{
		Action: audit.ActionLogin, Outcome: domain.AuditFailure,
		Details: map[string]any{"reason": msg},
	})
	http.Error(w, msg, http.StatusUnauthorized)
}

// SafeReturnTo only allows local absolute paths, preventing open redirects.
func SafeReturnTo(p string) string {
	if p == "" || !strings.HasPrefix(p, "/") || strings.HasPrefix(p, "//") || strings.HasPrefix(p, "/\\") || strings.ContainsAny(p, "\r\n") {
		return "/"
	}
	u, err := url.Parse(p)
	if err != nil || u.Host != "" || u.Scheme != "" {
		return "/"
	}
	return p
}

func unauthorized(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusUnauthorized)
	_, _ = w.Write([]byte(`{"error":"authentication required"}`))
}

func clearCookie(w http.ResponseWriter, name, path string, secure bool) {
	http.SetCookie(w, &http.Cookie{ //nolint:gosec // G124: Secure comes from configuration
		Name: name, Value: "", Path: path, MaxAge: -1, HttpOnly: true, Secure: secure, SameSite: http.SameSiteLaxMode,
	})
}

func randomToken() string {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		panic(err) // crypto/rand never fails on supported platforms
	}
	return base64.RawURLEncoding.EncodeToString(b)
}

func hashToken(t string) []byte {
	s := sha256.Sum256([]byte(t))
	return s[:]
}
