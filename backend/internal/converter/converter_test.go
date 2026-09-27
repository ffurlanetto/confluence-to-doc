package converter

import (
	"archive/zip"
	"bytes"
	"context"
	"errors"
	"os/exec"
	"testing"
	"time"

	"github.com/ffurlanetto/confluence-to-doc/backend/internal/domain"
)

const sample = `<!DOCTYPE html><html><head><meta charset="utf-8"></head><body>
<h1>1 Titre é</h1><p>Hello</p><h2 style="page-break-before: always">1.1 Enfant</h2><p>World</p></body></html>`

func requireSoffice(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("soffice"); err != nil {
		t.Skip("soffice not installed")
	}
	if testing.Short() {
		t.Skip("skipping LibreOffice conversion in -short mode")
	}
}

func TestConvertPDF(t *testing.T) {
	requireSoffice(t)
	var out bytes.Buffer
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	if err := (LibreOffice{}).Convert(ctx, []byte(sample), domain.FormatPDF, &out); err != nil {
		t.Fatal(err)
	}
	if !bytes.HasPrefix(out.Bytes(), []byte("%PDF-")) {
		t.Fatalf("output is not a PDF: %q", out.Bytes()[:min(16, out.Len())])
	}
}

func TestConvertDOCX(t *testing.T) {
	requireSoffice(t)
	var out bytes.Buffer
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	if err := (LibreOffice{}).Convert(ctx, []byte(sample), domain.FormatDOCX, &out); err != nil {
		t.Fatal(err)
	}
	zr, err := zip.NewReader(bytes.NewReader(out.Bytes()), int64(out.Len()))
	if err != nil {
		t.Fatalf("output is not a zip/docx: %v", err)
	}
	found := false
	for _, f := range zr.File {
		if f.Name == "word/document.xml" {
			found = true
		}
	}
	if !found {
		t.Fatal("word/document.xml missing")
	}
}

func TestConvertRejectsUnknownFormat(t *testing.T) {
	err := (LibreOffice{}).Convert(context.Background(), nil, domain.Format("odt"), &bytes.Buffer{})
	if !errors.Is(err, domain.ErrInvalidFormat) {
		t.Fatalf("want ErrInvalidFormat, got %v", err)
	}
}

func TestConvertReportsMissingBinary(t *testing.T) {
	err := (LibreOffice{Binary: "/nonexistent/soffice"}).Convert(context.Background(), []byte(sample), domain.FormatPDF, &bytes.Buffer{})
	if err == nil {
		t.Fatal("expected an error")
	}
}
