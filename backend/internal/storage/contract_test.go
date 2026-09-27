package storage_test

import (
	"bytes"
	"context"
	"errors"
	"io"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/johannesboyne/gofakes3"
	"github.com/johannesboyne/gofakes3/backend/s3mem"

	"github.com/ffurlanetto/confluence-to-doc/backend/internal/storage"
)

// newFakeS3 starts an in-memory S3 server with an "exports" bucket.
func newFakeS3(t *testing.T) (*storage.S3, *s3mem.Backend) {
	t.Helper()
	backend := s3mem.New()
	if err := backend.CreateBucket("exports"); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(gofakes3.New(backend).Server())
	t.Cleanup(srv.Close)
	s, err := storage.NewS3(context.Background(), storage.S3Options{
		Bucket: "exports", Region: "us-east-1", Endpoint: srv.URL,
		AccessKeyID: "test", SecretAccessKey: "test", Prefix: "exports/", UsePathStyle: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	return s, backend
}

func backends(t *testing.T) map[string]storage.BlobStore {
	t.Helper()
	local, err := storage.NewLocal(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	s3, _ := newFakeS3(t)
	return map[string]storage.BlobStore{"local": local, "s3": s3}
}

// TestBlobStoreContract runs the same behavioural checks on every backend so
// they stay interchangeable behind the feature flag.
func TestBlobStoreContract(t *testing.T) {
	for name, store := range backends(t) {
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			payload := bytes.Repeat([]byte("0123456789"), 300_000) // 3 MB

			n, err := store.Put(ctx, "doc.pdf", bytes.NewReader(payload))
			if err != nil || n != int64(len(payload)) {
				t.Fatalf("put: n=%d err=%v", n, err)
			}

			f, err := store.Open(ctx, "doc.pdf")
			if err != nil {
				t.Fatal(err)
			}
			got, err := io.ReadAll(f)
			if err != nil || !bytes.Equal(got, payload) {
				t.Fatalf("read back %d bytes (err %v), want %d", len(got), err, len(payload))
			}

			// Seek semantics used by http.ServeContent.
			if size, err := f.Seek(0, io.SeekEnd); err != nil || size != int64(len(payload)) {
				t.Fatalf("seek end: %d %v", size, err)
			}
			if _, err := f.Seek(1_000_000, io.SeekStart); err != nil {
				t.Fatal(err)
			}
			chunk := make([]byte, 10)
			if _, err := io.ReadFull(f, chunk); err != nil || !bytes.Equal(chunk, payload[1_000_000:1_000_010]) {
				t.Fatalf("read after seek: %q %v", chunk, err)
			}
			if err := f.Close(); err != nil {
				t.Fatal(err)
			}

			// Overwrite replaces the content.
			if _, err := store.Put(ctx, "doc.pdf", strings.NewReader("v2")); err != nil {
				t.Fatal(err)
			}
			f, _ = store.Open(ctx, "doc.pdf")
			got, _ = io.ReadAll(f)
			f.Close()
			if string(got) != "v2" {
				t.Fatalf("after overwrite got %q", got)
			}

			if err := store.Delete(ctx, "doc.pdf"); err != nil {
				t.Fatal(err)
			}
			if err := store.Delete(ctx, "doc.pdf"); err != nil {
				t.Fatalf("deleting a missing blob must succeed: %v", err)
			}
			if _, err := store.Open(ctx, "doc.pdf"); !errors.Is(err, fs.ErrNotExist) {
				t.Fatalf("open after delete: want fs.ErrNotExist, got %v", err)
			}

			for _, key := range []string{"../escape", "a/b", ""} {
				if _, err := store.Put(ctx, key, strings.NewReader("x")); !errors.Is(err, storage.ErrInvalidKey) {
					t.Errorf("key %q: want ErrInvalidKey, got %v", key, err)
				}
			}
		})
	}
}

func TestBlobStoreServesRangeRequests(t *testing.T) {
	for name, store := range backends(t) {
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			if _, err := store.Put(ctx, "r.docx", strings.NewReader("abcdefghijklmnopqrstuvwxyz")); err != nil {
				t.Fatal(err)
			}
			f, err := store.Open(ctx, "r.docx")
			if err != nil {
				t.Fatal(err)
			}
			defer f.Close()
			req := httptest.NewRequest(http.MethodGet, "/download", nil)
			req.Header.Set("Range", "bytes=5-9")
			rec := httptest.NewRecorder()
			http.ServeContent(rec, req, "r.docx", time.Now(), f)
			if rec.Code != http.StatusPartialContent || rec.Body.String() != "fghij" {
				t.Fatalf("range response: %d %q", rec.Code, rec.Body.String())
			}
		})
	}
}

func TestS3UsesPrefix(t *testing.T) {
	s, backend := newFakeS3(t)
	if _, err := s.Put(context.Background(), "a.pdf", strings.NewReader("x")); err != nil {
		t.Fatal(err)
	}
	if _, err := backend.HeadObject("exports", "exports/a.pdf"); err != nil {
		t.Fatalf("object not stored under the prefix: %v", err)
	}
}

func TestNewS3FailsFastOnMissingBucket(t *testing.T) {
	srv := httptest.NewServer(gofakes3.New(s3mem.New()).Server())
	defer srv.Close()
	_, err := storage.NewS3(context.Background(), storage.S3Options{
		Bucket: "missing", Region: "us-east-1", Endpoint: srv.URL,
		AccessKeyID: "test", SecretAccessKey: "test", UsePathStyle: true,
	})
	if err == nil || !strings.Contains(err.Error(), "missing") {
		t.Fatalf("want bucket error, got %v", err)
	}
}
