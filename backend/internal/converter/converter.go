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
	"strings"

	"github.com/ffurlanetto/confluence-to-doc/backend/internal/docx"
	"github.com/ffurlanetto/confluence-to-doc/backend/internal/domain"
)

// Converter converts a self-contained HTML document into the target format
// and writes the result to dst. template is the company Word template to
// apply, nil for the built-in styling.
type Converter interface {
	Convert(ctx context.Context, html []byte, format domain.Format, template *docx.Template, marking docx.Marking, dst io.Writer) error
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
	// Language is the documents' default language (BCP 47, e.g. fr-FR), which
	// a tagged PDF declares for screen readers. Empty keeps the template's,
	// or LibreOffice's en-US.
	Language string
}

var filters = map[domain.Format]string{
	// A tagged PDF carries the document structure (headings, lists, tables,
	// reading order) that screen readers and reflowing viewers need, and the
	// PDF/UA identification says it was produced for that purpose.
	domain.FormatPDF: `pdf:writer_pdf_Export:{"UseTaggedPDF":{"type":"boolean","value":"true"},` +
		`"PDFUACompliance":{"type":"boolean","value":"true"}}`,
	domain.FormatDOCX: "docx:MS Word 2007 XML",
}

// Convert produces the document; marking is stamped on every page of both
// formats (see docx.Mark). With a company template, the PDF is produced from
// the templated DOCX, so both formats carry the same header, fonts and page
// setup.
func (l LibreOffice) Convert(ctx context.Context, html []byte, format domain.Format, template *docx.Template, marking docx.Marking, dst io.Writer) error {
	if _, ok := filters[format]; !ok {
		return domain.ErrInvalidFormat
	}
	work, err := os.MkdirTemp(l.TempDir, "c2d-convert-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(work)

	source := filepath.Join(work, "document.html")
	if err := os.WriteFile(source, html, 0o600); err != nil {
		return err
	}

	// Always go through DOCX, whatever the target format. It is the format the
	// company template is expressed in, it is where the table of contents and
	// the document properties are assembled — and producing the PDF from it
	// rather than from the HTML is what makes the two formats one layout
	// instead of two independent renderings of the same source.
	generated, err := l.run(ctx, work, source, domain.FormatDOCX)
	if err != nil {
		return err
	}
	document, err := finish(generated, template)
	if err != nil {
		return err
	}
	if document, err = docx.Mark(document, marking); err != nil {
		return err
	}
	if document, err = docx.SetLanguage(document, l.Language); err != nil {
		return err
	}
	if format == domain.FormatDOCX {
		_, err = dst.Write(document)
		return err
	}

	docxPath := filepath.Join(work, "document-final.docx")
	if err := os.WriteFile(docxPath, document, 0o600); err != nil {
		return err
	}
	pdf, err := l.run(ctx, work, docxPath, domain.FormatPDF)
	if err != nil {
		return err
	}
	return copyOut(pdf, dst)
}

// finish turns the converter's raw DOCX into the document that is delivered.
func finish(generated []byte, template *docx.Template) ([]byte, error) {
	if template == nil {
		return docx.Polish(generated)
	}
	document, err := template.Apply(generated)
	if err != nil {
		return nil, fmt.Errorf("applying Word template %q: %w", template.Name(), err)
	}
	return document, nil
}

// run converts one file with a throw-away LibreOffice profile and returns the
// produced document.
func (l LibreOffice) run(ctx context.Context, work, source string, format domain.Format) ([]byte, error) {
	bin := l.Binary
	if bin == "" {
		bin = "soffice"
	}
	outDir := filepath.Join(work, "out-"+format.Extension())
	args := []string{
		"--headless", "--norestore", "--nolockcheck", "--nodefault", "--nologo",
		"-env:UserInstallation=" + "file://" + filepath.Join(work, "profile"),
	}
	if strings.EqualFold(filepath.Ext(source), ".html") {
		// Import as a Writer (not Writer/Web) document so page breaks,
		// headings and the PDF/DOCX export filters behave as expected.
		args = append(args, "--infilter=HTML (StarWriter)")
	}
	args = append(args, "--convert-to", filters[format], "--outdir", outDir, source)

	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.Env = isolatedEnv(os.Environ(), work)
	var output bytes.Buffer
	cmd.Stdout, cmd.Stderr = &output, &output
	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, fmt.Errorf("soffice failed: %w: %s", err, tail(output.Bytes(), 2048))
	}

	produced := filepath.Join(outDir, strings.TrimSuffix(filepath.Base(source), filepath.Ext(source))+"."+format.Extension())
	data, err := os.ReadFile(produced) //nolint:gosec // G304: path built from our own temp dir
	if err != nil {
		return nil, fmt.Errorf("soffice produced no output: %w: %s", err, tail(output.Bytes(), 2048))
	}
	if len(data) == 0 {
		return nil, errors.New("soffice produced an empty document")
	}
	return data, nil
}

// blackhole is where LibreOffice's HTTP traffic goes: the discard port on the
// loopback interface, where nothing listens.
const blackhole = "http://127.0.0.1:9"

// isolatedEnv is the environment of a LibreOffice process. LibreOffice
// fetches the stylesheets, images and other resources an imported HTML
// document points at; the renderer removes every such reference, and this is
// the second line: all of LibreOffice's HTTP(S) traffic is sent to a proxy
// that does not exist, so a reference that slipped through fails instead of
// reaching the network. The proxy settings of the server process itself (its
// way to Confluence or S3) are deliberately not inherited.
func isolatedEnv(parent []string, home string) []string {
	env := make([]string, 0, len(parent)+8)
	for _, kv := range parent {
		name, _, _ := strings.Cut(kv, "=")
		switch strings.ToLower(name) {
		case "http_proxy", "https_proxy", "all_proxy", "no_proxy", "ftp_proxy", "home":
			continue
		}
		env = append(env, kv)
	}
	for _, name := range []string{"http_proxy", "https_proxy", "all_proxy", "ftp_proxy"} {
		env = append(env, name+"="+blackhole, strings.ToUpper(name)+"="+blackhole)
	}
	return append(env, "no_proxy=", "NO_PROXY=", "HOME="+home)
}

func copyOut(data []byte, dst io.Writer) error {
	_, err := dst.Write(data)
	return err
}

func tail(b []byte, n int) []byte {
	if len(b) > n {
		return b[len(b)-n:]
	}
	return b
}
