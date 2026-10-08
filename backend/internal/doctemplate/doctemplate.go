// Package doctemplate decides which company Word template documents are
// produced with: the one uploaded from the administration console, else the
// one configured with WORD_TEMPLATE_PATH, else none (built-in styling).
//
// The uploaded template lives in PostgreSQL so that every instance uses the
// same one. Each export reads its checksum, a one-row lookup, and the parsed
// template is cached until the checksum changes.
package doctemplate

import (
	"bytes"
	"context"
	"errors"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/ffurlanetto/confluence-to-doc/backend/internal/docx"
	"github.com/ffurlanetto/confluence-to-doc/backend/internal/domain"
)

type Repo interface {
	DocumentTemplateVersion(ctx context.Context) ([]byte, error)
	GetDocumentTemplate(ctx context.Context, withContent bool) (*domain.DocumentTemplate, error)
	PutDocumentTemplate(ctx context.Context, name string, content []byte, uploadedBy uuid.UUID) (*domain.DocumentTemplate, error)
	DeleteDocumentTemplate(ctx context.Context) error
}

// Where the template in use comes from.
const (
	OriginUploaded   = "uploaded"
	OriginConfigured = "configured"
	OriginNone       = "none"
)

// Info describes the template in use.
type Info struct {
	Origin                string
	Name                  string
	DefaultParagraphStyle string
	Styles                int
	UploadedAt            *time.Time
	UploadedBy            string
	// Configured is the WORD_TEMPLATE_PATH template's name, which applies
	// again when the uploaded one is removed; empty when none is configured.
	Configured string
}

type Source struct {
	repo       Repo
	configured *docx.Template

	mu     sync.Mutex
	sum    []byte
	cached *docx.Template
}

// New returns a source; configured is the WORD_TEMPLATE_PATH template, nil
// when none is configured.
func New(repo Repo, configured *docx.Template) *Source {
	return &Source{repo: repo, configured: configured}
}

// Current returns the template documents are produced with now, nil for the
// built-in styling.
func (s *Source) Current(ctx context.Context) (*docx.Template, error) {
	sum, err := s.repo.DocumentTemplateVersion(ctx)
	if errors.Is(err, domain.ErrNotFound) {
		return s.configured, nil
	}
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cached != nil && bytes.Equal(s.sum, sum) {
		return s.cached, nil
	}
	stored, err := s.repo.GetDocumentTemplate(ctx, true)
	if errors.Is(err, domain.ErrNotFound) {
		return s.configured, nil // removed in the meantime
	}
	if err != nil {
		return nil, err
	}
	t, err := docx.ParseTemplate(stored.Name, stored.Content)
	if err != nil {
		// It was validated on upload; failing here means the validation got
		// stricter since. Exports report the template as unusable.
		return nil, errors.Join(docx.ErrApply, err)
	}
	s.sum, s.cached = stored.SHA256, t
	return t, nil
}

// Info describes the template in use.
func (s *Source) Info(ctx context.Context) (Info, error) {
	info := Info{Origin: OriginNone}
	if s.configured != nil {
		info.Configured = s.configured.Name()
	}
	stored, err := s.repo.GetDocumentTemplate(ctx, false)
	if err != nil && !errors.Is(err, domain.ErrNotFound) {
		return Info{}, err
	}
	t, err := s.Current(ctx)
	if err != nil {
		return Info{}, err
	}
	if t == nil {
		return info, nil
	}
	info.Name, info.DefaultParagraphStyle, info.Styles = t.Name(), t.DefaultParagraphStyle(), len(t.Styles())
	info.Origin = OriginConfigured
	if stored != nil && t != s.configured {
		info.Origin = OriginUploaded
		info.UploadedAt, info.UploadedBy = &stored.UploadedAt, stored.UploadedByEmail
	}
	return info, nil
}

// Upload validates and stores a template, which applies to the next exports
// on every instance.
func (s *Source) Upload(ctx context.Context, name string, content []byte, by uuid.UUID) (Info, error) {
	t, err := docx.ParseTemplate(name, content)
	if err != nil {
		return Info{}, err
	}
	if _, err := s.repo.PutDocumentTemplate(ctx, t.Name(), content, by); err != nil {
		return Info{}, err
	}
	return s.Info(ctx)
}

// Reset removes the uploaded template; domain.ErrNotFound when none is.
func (s *Source) Reset(ctx context.Context) error {
	return s.repo.DeleteDocumentTemplate(ctx)
}
