// Package account manages user preferences, notably the Confluence Personal
// Access Token, which is validated against Confluence then stored encrypted.
package account

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/google/uuid"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"

	"github.com/ffurlanetto/confluence-to-doc/backend/internal/confluence"
	"github.com/ffurlanetto/confluence-to-doc/backend/internal/crypto"
	"github.com/ffurlanetto/confluence-to-doc/backend/internal/domain"
	"github.com/ffurlanetto/confluence-to-doc/backend/internal/store"
)

var ErrInvalidPAT = errors.New("the personal access token was rejected by Confluence")

type Repo interface {
	GetPreferences(ctx context.Context, userID uuid.UUID) (*domain.Preferences, error)
	SetPAT(ctx context.Context, userID uuid.UUID, encryptedPAT []byte, expiresAt *time.Time) error
	SetDefaultFormat(ctx context.Context, userID uuid.UUID, f domain.Format) error
	PATsNotUnderKey(ctx context.Context, keyID string, after uuid.UUID, limit int) ([]store.EncryptedPAT, error)
	ReplacePAT(ctx context.Context, userID uuid.UUID, old, replacement []byte) (bool, error)
}

type Service struct {
	repo          Repo
	sealer        *crypto.Sealer
	confluenceURL *url.URL
	httpClient    *http.Client
	limiter       confluence.Limiter
}

func NewService(repo Repo, sealer *crypto.Sealer, confluenceURL *url.URL, timeout time.Duration) *Service {
	return &Service{
		repo:          repo,
		sealer:        sealer,
		confluenceURL: confluenceURL,
		// otelhttp traces every Confluence call, which is where most of an
		// export's time goes.
		httpClient: &http.Client{Timeout: timeout, Transport: otelhttp.NewTransport(http.DefaultTransport)},
	}
}

// WithLimiter paces every Confluence request made with this service's
// clients: the deployment-wide budget that protects Confluence.
func (s *Service) WithLimiter(l confluence.Limiter) *Service {
	s.limiter = l
	return s
}

// ConfluenceURL is the Confluence instance all users connect to. It is fixed
// by configuration (never user-supplied) to prevent SSRF and token leaks.
func (s *Service) ConfluenceURL() *url.URL { u := *s.confluenceURL; return &u }

func (s *Service) Preferences(ctx context.Context, userID uuid.UUID) (*domain.Preferences, error) {
	return s.repo.GetPreferences(ctx, userID)
}

// SetPAT validates the token against Confluence, then stores it encrypted.
// It returns the Confluence user the token belongs to.
func (s *Service) SetPAT(ctx context.Context, userID uuid.UUID, pat string) (*confluence.User, error) {
	pat = strings.TrimSpace(pat)
	if pat == "" || len(pat) > 1024 {
		return nil, ErrInvalidPAT
	}
	cu, err := s.newClient(pat).CurrentUser(ctx)
	if errors.Is(err, confluence.ErrUnauthorized) || errors.Is(err, confluence.ErrForbidden) {
		return nil, ErrInvalidPAT
	}
	if err != nil {
		return nil, fmt.Errorf("validating token against Confluence: %w", err)
	}
	enc, err := s.sealer.Seal([]byte(pat), userID[:])
	if err != nil {
		return nil, err
	}
	// Best effort: an older Confluence, or a token it cannot describe, simply
	// leaves the expiry unknown.
	expires, err := s.newClient(pat).TokenExpiry(ctx)
	if err != nil {
		slog.DebugContext(ctx, "token expiry unavailable", "err", err)
	}
	if err := s.repo.SetPAT(ctx, userID, enc, expires); err != nil {
		return nil, err
	}
	return cu, nil
}

func (s *Service) ClearPAT(ctx context.Context, userID uuid.UUID) error {
	return s.repo.SetPAT(ctx, userID, nil, nil)
}

func (s *Service) SetDefaultFormat(ctx context.Context, userID uuid.UUID, f domain.Format) error {
	return s.repo.SetDefaultFormat(ctx, userID, f)
}

// Client returns a Confluence client authenticated as the user.
func (s *Service) Client(ctx context.Context, userID uuid.UUID) (*confluence.Client, error) {
	p, err := s.repo.GetPreferences(ctx, userID)
	if err != nil {
		return nil, err
	}
	if !p.HasPAT() {
		return nil, domain.ErrPATMissing
	}
	pat, err := s.sealer.Open(p.EncryptedPAT, userID[:])
	if err != nil {
		return nil, fmt.Errorf("%w: %w", domain.ErrPATUnreadable, err)
	}
	return s.newClient(string(pat)), nil
}

// rotationBatch is how many tokens are read at a time by RotateKeys.
const rotationBatch = 200

// RotateKeys re-encrypts the stored tokens that were not encrypted with the
// current key, so an old key can eventually be removed from the ring. It is
// idempotent and safe on several instances: a row is only replaced if it still
// holds what was read. It returns the number of tokens re-encrypted and the
// number left that no key of the ring could open.
func (s *Service) RotateKeys(ctx context.Context) (rotated, unreadable int, err error) {
	for after := uuid.Nil; ; {
		pending, err := s.repo.PATsNotUnderKey(ctx, s.sealer.CurrentKeyID(), after, rotationBatch)
		if err != nil || len(pending) == 0 {
			return rotated, unreadable, err
		}
		after = pending[len(pending)-1].UserID
		n, bad, err := s.rotate(ctx, pending)
		rotated, unreadable = rotated+n, unreadable+bad
		if err != nil {
			return rotated, unreadable, err
		}
	}
}

func (s *Service) rotate(ctx context.Context, pending []store.EncryptedPAT) (rotated, unreadable int, err error) {
	for _, p := range pending {
		pat, err := s.sealer.Open(p.Encrypted, p.UserID[:])
		if err != nil {
			// The key that wrote it left the ring: the user must enter the token
			// again. Leave the row as it is; Client reports it as unreadable.
			unreadable++
			continue
		}
		enc, err := s.sealer.Seal(pat, p.UserID[:])
		if err != nil {
			return rotated, unreadable, err
		}
		ok, err := s.repo.ReplacePAT(ctx, p.UserID, p.Encrypted, enc)
		if err != nil {
			return rotated, unreadable, err
		}
		if ok {
			rotated++
		}
	}
	return rotated, unreadable, nil
}

func (s *Service) newClient(pat string) *confluence.Client {
	if s.limiter != nil {
		return confluence.NewClient(s.confluenceURL, pat, s.httpClient, confluence.WithLimiter(s.limiter))
	}
	return confluence.NewClient(s.confluenceURL, pat, s.httpClient)
}
