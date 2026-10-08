package doctemplate_test

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/ffurlanetto/confluence-to-doc/backend/internal/doctemplate"
	"github.com/ffurlanetto/confluence-to-doc/backend/internal/docx"
	"github.com/ffurlanetto/confluence-to-doc/backend/internal/domain"
)

type memRepo struct {
	stored *domain.DocumentTemplate
	loads  int
}

func (m *memRepo) DocumentTemplateVersion(context.Context) ([]byte, error) {
	if m.stored == nil {
		return nil, domain.ErrNotFound
	}
	return m.stored.SHA256, nil
}

func (m *memRepo) GetDocumentTemplate(_ context.Context, withContent bool) (*domain.DocumentTemplate, error) {
	if m.stored == nil {
		return nil, domain.ErrNotFound
	}
	if withContent {
		m.loads++
	}
	t := *m.stored
	return &t, nil
}

func (m *memRepo) PutDocumentTemplate(_ context.Context, name string, content []byte, by uuid.UUID) (*domain.DocumentTemplate, error) {
	sum := sha256.Sum256(content)
	m.stored = &domain.DocumentTemplate{Name: name, Content: content, SHA256: sum[:], UploadedBy: &by,
		UploadedByEmail: "admin@example.com", UploadedAt: time.Now()}
	return m.stored, nil
}

func (m *memRepo) DeleteDocumentTemplate(context.Context) error {
	if m.stored == nil {
		return domain.ErrNotFound
	}
	m.stored = nil
	return nil
}

func templateFile(t *testing.T, styleName string) []byte {
	t.Helper()
	const w = `xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main"`
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for name, content := range map[string]string{
		"[Content_Types].xml":          `<Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types"/>`,
		"word/document.xml":            `<w:document ` + w + `><w:body><w:sectPr/></w:body></w:document>`,
		"word/_rels/document.xml.rels": `<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships"/>`,
		"word/styles.xml": `<w:styles ` + w + `><w:style w:type="paragraph" w:default="1" w:styleId="` + styleName + `">` +
			`<w:name w:val="` + styleName + `"/></w:style></w:styles>`,
	} {
		f, _ := zw.Create(name)
		_, _ = f.Write([]byte(content))
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func TestSourcePrecedenceAndCache(t *testing.T) {
	ctx := context.Background()
	configured, err := docx.ParseTemplate("configured.dotx", templateFile(t, "Configured"))
	if err != nil {
		t.Fatal(err)
	}
	repo := &memRepo{}
	s := doctemplate.New(repo, configured)

	if got, _ := s.Current(ctx); got != configured {
		t.Fatal("without an upload, the configured template applies")
	}
	info, err := s.Upload(ctx, "uploaded.dotx", templateFile(t, "Uploaded"), uuid.New())
	if err != nil || info.Origin != doctemplate.OriginUploaded || info.Name != "uploaded.dotx" ||
		info.DefaultParagraphStyle != "Uploaded" || info.Configured != "configured.dotx" || info.UploadedBy != "admin@example.com" {
		t.Fatalf("Upload = %+v, %v", info, err)
	}
	first, _ := s.Current(ctx)
	second, _ := s.Current(ctx)
	if first != second || first.Name() != "uploaded.dotx" || repo.loads != 1 {
		t.Fatalf("the parsed template must be cached until it changes (loads = %d)", repo.loads)
	}
	if _, err := s.Upload(ctx, "v2.dotx", templateFile(t, "Second"), uuid.New()); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.Current(ctx); got.Name() != "v2.dotx" {
		t.Fatalf("a new upload must replace the cached one, got %s", got.Name())
	}

	if err := s.Reset(ctx); err != nil {
		t.Fatal(err)
	}
	if info, _ := s.Info(ctx); info.Origin != doctemplate.OriginConfigured || info.Name != "configured.dotx" {
		t.Fatalf("after reset = %+v", info)
	}
	if err := s.Reset(ctx); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("second reset = %v", err)
	}
}

func TestSourceWithoutAnyTemplate(t *testing.T) {
	s := doctemplate.New(&memRepo{}, nil)
	if got, err := s.Current(context.Background()); got != nil || err != nil {
		t.Fatalf("Current = %v, %v; want the built-in styling", got, err)
	}
	if info, _ := s.Info(context.Background()); info.Origin != doctemplate.OriginNone {
		t.Fatalf("Info = %+v", info)
	}
}

func TestUploadRejectsUnsafeTemplate(t *testing.T) {
	repo := &memRepo{}
	s := doctemplate.New(repo, nil)
	_, err := s.Upload(context.Background(), "x.docx", []byte("not a zip"), uuid.New())
	if !errors.Is(err, docx.ErrInvalidTemplate) || repo.stored != nil {
		t.Fatalf("Upload = %v; nothing must be stored", err)
	}
}
