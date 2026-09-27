// Package account manages user preferences, notably the Confluence Personal
// Access Token, which is validated against Confluence then stored encrypted.
package account

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/ffurlanetto/confluence-to-doc/backend/internal/confluence"
	"github.com/ffurlanetto/confluence-to-doc/backend/internal/crypto"
	"github.com/ffurlanetto/confluence-to-doc/backend/internal/domain"
)

var ErrInvalidPAT = errors.New("the personal access token was rejected by Confluence")

type Repo interface {
	GetPreferences(ctx context.Context, userID uuid.UUID) (*domain.Preferences, error)
	SetPAT(ctx context.Context, userID uuid.UUID, encryptedPAT []byte) error
	SetDefaultFormat(ctx context.Context, userID uuid.UUID, f domain.Format) error
}

type Service struct {
	repo          Repo
	sealer        *crypto.Sealer
	confluenceURL *url.URL
	httpClient    *http.Client
}

func NewService(repo Repo, sealer *crypto.Sealer, confluenceURL *url.URL, timeout time.Duration) *Service {
	return &Service{
		repo:          repo,
		sealer:        sealer,
		confluenceURL: confluenceURL,
		httpClient:    &http.Client{Timeout: timeout},
	}
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
	if err := s.repo.SetPAT(ctx, userID, enc); err != nil {
		return nil, err
	}
	return cu, nil
}

func (s *Service) ClearPAT(ctx context.Context, userID uuid.UUID) error {
	return s.repo.SetPAT(ctx, userID, nil)
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
		return nil, fmt.Errorf("decrypting PAT: %w", err)
	}
	return s.newClient(string(pat)), nil
}

func (s *Service) newClient(pat string) *confluence.Client {
	return confluence.NewClient(s.confluenceURL, pat, s.httpClient)
}
