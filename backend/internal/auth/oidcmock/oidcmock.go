// Package oidcmock is a tiny OpenID Connect provider for tests and local
// development. It auto-approves every authorization request as a fixed user.
// NEVER use it in production.
package oidcmock

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	jose "github.com/go-jose/go-jose/v4"
	"github.com/go-jose/go-jose/v4/jwt"
)

type User struct {
	Subject string
	Email   string
	Name    string
	// Claims are extra claims (e.g. "groups") added to the ID token, or only
	// served by the UserInfo endpoint when ClaimsInUserInfoOnly is set.
	Claims map[string]any
}

type Provider struct {
	// Issuer must be set to the externally visible base URL of the server.
	Issuer       string
	ClientID     string
	ClientSecret string
	User         User
	// ClaimsInUserInfoOnly mimics providers that leave groups out of the ID
	// token and only expose them through UserInfo.
	ClaimsInUserInfoOnly bool
	// SessionID is put in ID tokens as "sid", the provider's session id.
	SessionID string
	// Revoked makes refresh requests fail with invalid_grant, as for a user
	// disabled or signed out at the provider.
	Revoked bool
	// Refreshes counts refresh token exchanges.
	Refreshes int

	key           *rsa.PrivateKey
	mu            sync.Mutex
	codes         map[string]grant
	tokens        map[string]bool
	refreshTokens map[string]bool
}

type grant struct {
	nonce, challenge, redirectURI string
	expires                       time.Time
}

func New(clientID, clientSecret string, user User) *Provider {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		panic(err)
	}
	return &Provider{ClientID: clientID, ClientSecret: clientSecret, User: user, key: key,
		codes: map[string]grant{}, tokens: map[string]bool{}, refreshTokens: map[string]bool{}}
}

func (p *Provider) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch r.URL.Path {
	case "/.well-known/openid-configuration":
		writeJSON(w, http.StatusOK, map[string]any{
			"issuer":                                p.Issuer,
			"authorization_endpoint":                p.Issuer + "/authorize",
			"token_endpoint":                        p.Issuer + "/token",
			"jwks_uri":                              p.Issuer + "/jwks",
			"userinfo_endpoint":                     p.Issuer + "/userinfo",
			"end_session_endpoint":                  p.Issuer + "/logout",
			"response_types_supported":              []string{"code"},
			"subject_types_supported":               []string{"public"},
			"id_token_signing_alg_values_supported": []string{"RS256"},
			"code_challenge_methods_supported":      []string{"S256"},
		})
	case "/jwks":
		writeJSON(w, http.StatusOK, jose.JSONWebKeySet{Keys: []jose.JSONWebKey{{Key: &p.key.PublicKey, KeyID: "k1", Algorithm: "RS256", Use: "sig"}}})
	case "/authorize":
		p.authorize(w, r)
	case "/token":
		p.token(w, r)
	case "/userinfo":
		p.userinfo(w, r)
	case "/logout":
		if u := r.URL.Query().Get("post_logout_redirect_uri"); u != "" {
			http.Redirect(w, r, u, http.StatusFound)
			return
		}
		_, _ = w.Write([]byte("logged out"))
	default:
		http.NotFound(w, r)
	}
}

func (p *Provider) authorize(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	redirect, err := url.Parse(q.Get("redirect_uri"))
	if q.Get("client_id") != p.ClientID || q.Get("response_type") != "code" || err != nil || redirect.Scheme == "" {
		http.Error(w, "invalid authorization request", http.StatusBadRequest)
		return
	}
	if q.Get("code_challenge_method") != "S256" || q.Get("code_challenge") == "" {
		http.Error(w, "PKCE S256 required", http.StatusBadRequest)
		return
	}
	code := randString()
	p.mu.Lock()
	p.codes[code] = grant{nonce: q.Get("nonce"), challenge: q.Get("code_challenge"), redirectURI: redirect.String(), expires: time.Now().Add(time.Minute)}
	p.mu.Unlock()
	rq := redirect.Query()
	rq.Set("code", code)
	rq.Set("state", q.Get("state"))
	redirect.RawQuery = rq.Encode()
	http.Redirect(w, r, redirect.String(), http.StatusFound)
}

func (p *Provider) token(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		oauthError(w, "invalid_request")
		return
	}
	id, secret, ok := r.BasicAuth()
	if !ok {
		id, secret = r.PostForm.Get("client_id"), r.PostForm.Get("client_secret")
	}
	if id != p.ClientID || subtle.ConstantTimeCompare([]byte(secret), []byte(p.ClientSecret)) != 1 {
		oauthError(w, "invalid_client")
		return
	}
	if r.PostForm.Get("grant_type") == "refresh_token" {
		p.refresh(w, r.PostForm.Get("refresh_token"))
		return
	}
	code := r.PostForm.Get("code")
	p.mu.Lock()
	g, found := p.codes[code]
	delete(p.codes, code) // codes are single-use
	p.mu.Unlock()
	sum := sha256.Sum256([]byte(r.PostForm.Get("code_verifier")))
	if !found || time.Now().After(g.expires) || r.PostForm.Get("redirect_uri") != g.redirectURI ||
		base64.RawURLEncoding.EncodeToString(sum[:]) != g.challenge {
		oauthError(w, "invalid_grant")
		return
	}

	signer, err := jose.NewSigner(jose.SigningKey{Algorithm: jose.RS256, Key: jose.JSONWebKey{Key: p.key, KeyID: "k1"}},
		(&jose.SignerOptions{}).WithType("JWT"))
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	p.issue(w, signer, g.nonce)
}

// issue answers a token request with an ID token, an access token and a
// refresh token.
func (p *Provider) issue(w http.ResponseWriter, signer jose.Signer, nonce string) {
	now := time.Now()
	claims := map[string]any{
		"iss": p.Issuer, "sub": p.User.Subject, "aud": p.ClientID,
		"iat": now.Unix(), "exp": now.Add(5 * time.Minute).Unix(),
		"email": p.User.Email, "name": p.User.Name,
	}
	if nonce != "" {
		claims["nonce"] = nonce
	}
	if p.SessionID != "" {
		claims["sid"] = p.SessionID
	}
	if !p.ClaimsInUserInfoOnly {
		for k, v := range p.User.Claims {
			claims[k] = v
		}
	}
	idToken, err := jwt.Signed(signer).Claims(claims).Serialize()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	access, refresh := randString(), randString()
	p.mu.Lock()
	p.tokens[access] = true
	p.refreshTokens[refresh] = true
	p.mu.Unlock()
	writeJSON(w, http.StatusOK, map[string]any{
		"access_token": access, "token_type": "Bearer", "expires_in": 300, "id_token": idToken,
		"refresh_token": refresh,
	})
}

// refresh exchanges a refresh token, rotating it as most providers do.
func (p *Provider) refresh(w http.ResponseWriter, token string) {
	p.mu.Lock()
	valid := p.refreshTokens[token] && !p.Revoked
	delete(p.refreshTokens, token)
	p.Refreshes++
	p.mu.Unlock()
	if !valid {
		oauthError(w, "invalid_grant")
		return
	}
	signer, err := p.signer()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	p.issue(w, signer, "")
}

func (p *Provider) signer() (jose.Signer, error) {
	return jose.NewSigner(jose.SigningKey{Algorithm: jose.RS256, Key: jose.JSONWebKey{Key: p.key, KeyID: "k1"}},
		(&jose.SignerOptions{}).WithType("JWT"))
}

// LogoutToken signs a back-channel logout token with the provider's key;
// extra claims override the defaults (events, iat, jti, aud, iss, sub, sid).
func (p *Provider) LogoutToken(extra map[string]any) (string, error) {
	signer, err := jose.NewSigner(jose.SigningKey{Algorithm: jose.RS256, Key: jose.JSONWebKey{Key: p.key, KeyID: "k1"}},
		(&jose.SignerOptions{}).WithType("logout+jwt"))
	if err != nil {
		return "", err
	}
	claims := map[string]any{
		"iss": p.Issuer, "aud": p.ClientID, "sub": p.User.Subject, "iat": time.Now().Unix(), "jti": randString(),
		"events": map[string]any{"http://schemas.openid.net/event/backchannel-logout": map[string]any{}},
	}
	if p.SessionID != "" {
		claims["sid"] = p.SessionID
	}
	for k, v := range extra {
		if v == nil {
			delete(claims, k)
			continue
		}
		claims[k] = v
	}
	return jwt.Signed(signer).Claims(claims).Serialize()
}

func (p *Provider) userinfo(w http.ResponseWriter, r *http.Request) {
	token, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	p.mu.Lock()
	valid := ok && p.tokens[token]
	p.mu.Unlock()
	if !valid {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	claims := map[string]any{"sub": p.User.Subject, "email": p.User.Email, "name": p.User.Name}
	for k, v := range p.User.Claims {
		claims[k] = v
	}
	writeJSON(w, http.StatusOK, claims)
}

func oauthError(w http.ResponseWriter, code string) {
	writeJSON(w, http.StatusBadRequest, map[string]string{"error": code})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func randString() string {
	b := make([]byte, 24)
	_, _ = rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}
