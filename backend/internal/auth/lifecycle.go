package auth

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	"github.com/google/uuid"
	"golang.org/x/oauth2"

	"github.com/ffurlanetto/confluence-to-doc/backend/internal/audit"
	"github.com/ffurlanetto/confluence-to-doc/backend/internal/domain"
)

// A session outlives the sign-in that opened it: without more, an account
// disabled in the identity provider keeps working here until SESSION_TTL.
// Two mechanisms end it sooner:
//
//   - revalidation: every RevalidateInterval, the session's refresh token is
//     exchanged with the provider; a refusal (invalid_grant) means the
//     account or the provider session is gone, and the session ends. The
//     admin role is re-evaluated from the new ID token at the same time.
//   - back-channel logout (OpenID Connect Back-Channel Logout 1.0): the
//     provider calls /auth/backchannel-logout when a user signs out there or
//     is disabled, and the matching sessions end at once.

// backchannelEvent is the event a logout token must carry.
const backchannelEvent = "http://schemas.openid.net/event/backchannel-logout"

// logoutTokenMaxAge bounds the age of a logout token without an expiry.
const logoutTokenMaxAge = 5 * time.Minute

// newSession builds the session row for a fresh sign-in.
func (a *Authenticator) newSession(hash []byte, userID uuid.UUID, expires time.Time, idClaims map[string]any, tok *oauth2.Token) (domain.Session, error) {
	sess := domain.Session{UserID: userID, ExpiresAt: expires}
	sess.SID, _ = idClaims["sid"].(string)
	if a.cfg.RevalidateInterval > 0 && tok.RefreshToken != "" {
		enc, err := a.sealer.Seal([]byte(tok.RefreshToken), hash)
		if err != nil {
			return sess, err
		}
		next := a.now().Add(a.cfg.RevalidateInterval)
		sess.RefreshToken, sess.RevalidateAt = enc, &next
	}
	return sess, nil
}

// revalidate re-checks a session with the provider. It returns false only
// when the provider refused: a provider that cannot be reached does not sign
// everybody out, the check is simply tried again later.
func (a *Authenticator) revalidate(ctx context.Context, hash []byte, user *domain.User, sess *domain.Session) bool {
	claimed, err := a.repo.ClaimRevalidation(ctx, hash, a.now().Add(a.cfg.RevalidateInterval))
	if err != nil || !claimed || len(sess.RefreshToken) == 0 {
		return true // another request is on it, or nothing to check with
	}
	refresh, err := a.sealer.Open(sess.RefreshToken, hash)
	if err != nil {
		slog.WarnContext(ctx, "session refresh token unreadable; the session runs to its expiry", "user_id", user.ID)
		return true
	}
	tok, err := a.oauth.TokenSource(ctx, &oauth2.Token{RefreshToken: string(refresh)}).Token()
	if err != nil {
		var re *oauth2.RetrieveError
		if errors.As(err, &re) && (re.ErrorCode == "invalid_grant" || (re.Response != nil && re.Response.StatusCode == http.StatusUnauthorized)) {
			if err := a.repo.DeleteSession(ctx, hash); err != nil {
				slog.ErrorContext(ctx, "ending a revoked session", "err", err)
			}
			actorID, email := audit.Actor(user)
			a.audit.Record(ctx, domain.AuditEvent{
				ActorID: actorID, ActorEmail: email, Action: audit.ActionSessionRevoked, Outcome: domain.AuditSuccess,
				TargetType: audit.TargetUser, TargetID: user.ID.String(),
				Details: map[string]any{"reason": "the identity provider refused to refresh the session"},
			})
			return false
		}
		slog.WarnContext(ctx, "session revalidation failed; trying again later", "user_id", user.ID, "err", err)
		return true
	}
	if tok.RefreshToken != "" && tok.RefreshToken != string(refresh) {
		if enc, err := a.sealer.Seal([]byte(tok.RefreshToken), hash); err == nil {
			if err := a.repo.StoreRefreshToken(ctx, hash, enc); err != nil {
				slog.WarnContext(ctx, "storing the rotated refresh token", "err", err)
			}
		}
	}
	if raw, _ := tok.Extra("id_token").(string); raw != "" {
		if idToken, err := a.verifier.Verify(ctx, raw); err == nil && idToken.Subject == user.Subject {
			var claims map[string]any
			if err := idToken.Claims(&claims); err == nil {
				user.IsAdmin = a.isAdmin(ctx, claims, tok)
				if err := a.repo.RecordLogin(ctx, user.ID, user.IsAdmin); err != nil {
					slog.WarnContext(ctx, "recording the refreshed role", "err", err)
				}
			}
		}
	}
	return true
}

// BackchannelLogout ends the sessions named by a logout token the provider
// sends server to server. It is not protected against CSRF on purpose: the
// caller is the provider, and the signed token is the proof.
func (a *Authenticator) BackchannelLogout(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	w.Header().Set("Cache-Control", "no-store")
	if err := r.ParseForm(); err != nil {
		backchannelError(w, r, "unreadable request", err)
		return
	}
	verifier := a.provider.Verifier(&oidc.Config{ClientID: a.cfg.ClientID, SkipExpiryCheck: true, Now: a.now})
	token, err := verifier.Verify(ctx, r.PostForm.Get("logout_token"))
	if err != nil {
		backchannelError(w, r, "invalid logout token", err)
		return
	}
	var claims struct {
		SID      string                     `json:"sid"`
		Events   map[string]json.RawMessage `json:"events"`
		Nonce    *string                    `json:"nonce"`
		IssuedAt int64                      `json:"iat"`
	}
	if err := token.Claims(&claims); err != nil {
		backchannelError(w, r, "invalid logout token", err)
		return
	}
	issued := time.Unix(claims.IssuedAt, 0)
	switch {
	case claims.Events[backchannelEvent] == nil:
		backchannelError(w, r, "not a logout token", nil)
		return
	case claims.Nonce != nil:
		backchannelError(w, r, "a logout token must not carry a nonce", nil)
		return
	case claims.SID == "" && token.Subject == "":
		backchannelError(w, r, "a logout token names a session or a subject", nil)
		return
	case !token.Expiry.IsZero() && a.now().After(token.Expiry),
		token.Expiry.IsZero() && a.now().Sub(issued) > logoutTokenMaxAge:
		backchannelError(w, r, "expired logout token", nil)
		return
	}

	var ended int64
	if claims.SID != "" {
		ended, err = a.repo.DeleteSessionsBySID(ctx, claims.SID)
	} else {
		ended, err = a.repo.DeleteSessionsBySubject(ctx, token.Issuer, token.Subject)
	}
	if err != nil {
		slog.ErrorContext(ctx, "back-channel logout", "err", err)
		http.Error(w, `{"error":"server_error"}`, http.StatusInternalServerError)
		return
	}
	a.audit.Record(ctx, domain.AuditEvent{
		Action: audit.ActionBackchannelLogout, Outcome: domain.AuditSuccess,
		Details: map[string]any{"subject": token.Subject, "sid": claims.SID, "sessions": ended},
	})
	w.WriteHeader(http.StatusOK)
}

func backchannelError(w http.ResponseWriter, r *http.Request, reason string, err error) {
	slog.WarnContext(r.Context(), "back-channel logout refused", "reason", reason, "err", err)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusBadRequest)
	_, _ = w.Write([]byte(`{"error":"invalid_request"}`))
}
