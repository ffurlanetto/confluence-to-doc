// Package converter turns the assembled HTML document into PDF or DOCX.
package converter

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/ffurlanetto/confluence-to-doc/backend/internal/domain"
)

// Converter converts a self-contained HTML document into the target format
// and writes the result to dst.
type Converter interface {
	Convert(ctx context.Context, html []byte, format domain.Format, dst io.Writer) error
}

// LibreOffice converts documents with a headless `soffice` process.
//
// Each conversion runs with its own throw-away user profile so that several
// conversions can safely run in parallel; overall parallelism is bounded by
// the worker pool.
type LibreOffice struct {
	Binary string
	// TempDir is where working directories are created ("" = os.TempDir()).
	TempDir string
}

var filters = map[domain.Format]string{
	domain.FormatPDF:  "pdf:writer_pdf_Export",
	domain.FormatDOCX: "docx:MS Word 2007 XML",
}

func (l LibreOffice) Convert(ctx context.Context, html []byte, format domain.Format, dst io.Writer) error {
	filter, ok := filters[format]
	if !ok {
		return domain.ErrInvalidFormat
	}
	work, err := os.MkdirTemp(l.TempDir, "c2d-convert-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(work)

	in := filepath.Join(work, "document.html")
	if err := os.WriteFile(in, html, 0o600); err != nil {
		return err
	}
	outDir := filepath.Join(work, "out")
	profile := "file://" + filepath.Join(work, "profile")

	bin := l.Binary
	if bin == "" {
		bin = "soffice"
	}
	cmd := exec.CommandContext(ctx, bin,
		"--headless", "--norestore", "--nolockcheck", "--nodefault", "--nologo",
		"-env:UserInstallation="+profile,
		// Import as a Writer (not Writer/Web) document so page breaks,
		// headings and the PDF/DOCX export filters behave as expected.
		"--infilter=HTML (StarWriter)",
		"--convert-to", filter,
		"--outdir", outDir,
		in,
	)
	cmd.Env = append(os.Environ(), "HOME="+work)
	var output bytes.Buffer
	cmd.Stdout, cmd.Stderr = &output, &output
	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return fmt.Errorf("soffice failed: %w: %s", err, tail(output.Bytes(), 2048))
	}

	f, err := os.Open(filepath.Join(outDir, "document."+format.Extension())) //nolint:gosec // G304: path built from our temp dir and a validated format
	if err != nil {
		return fmt.Errorf("soffice produced no output: %w: %s", err, tail(output.Bytes(), 2048))
	}
	defer f.Close()
	if st, err := f.Stat(); err == nil && st.Size() == 0 {
		return errors.New("soffice produced an empty document")
	}
	_, err = io.Copy(dst, f)
	return err
}

func tail(b []byte, n int) []byte {
	if len(b) > n {
		return b[len(b)-n:]
	}
	return b
}
