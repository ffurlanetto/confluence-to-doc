package storage

import (
	"context"
	"errors"
	"io"
	"os"
	"strings"
	"testing"
)

func TestLocalRoundTrip(t *testing.T) {
	ctx := context.Background()
	s, err := NewLocal(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	n, err := s.Put(ctx, "abc.pdf", strings.NewReader("hello"))
	if err != nil || n != 5 {
		t.Fatalf("put: %d, %v", n, err)
	}
	f, err := s.Open(ctx, "abc.pdf")
	if err != nil {
		t.Fatal(err)
	}
	data, _ := io.ReadAll(f)
	f.Close()
	if string(data) != "hello" {
		t.Fatalf("got %q", data)
	}
	if err := s.Delete(ctx, "abc.pdf"); err != nil {
		t.Fatal(err)
	}
	if err := s.Delete(ctx, "abc.pdf"); err != nil {
		t.Fatalf("deleting a missing blob must succeed: %v", err)
	}
	if _, err := s.Open(ctx, "abc.pdf"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("want ErrNotExist, got %v", err)
	}
}

func TestLocalRejectsTraversal(t *testing.T) {
	s, _ := NewLocal(t.TempDir())
	for _, key := range []string{"../etc/passwd", "a/b", "", "..", ".hidden"} {
		if _, err := s.Put(context.Background(), key, strings.NewReader("x")); !errors.Is(err, ErrInvalidKey) {
			t.Errorf("key %q: want ErrInvalidKey, got %v", key, err)
		}
	}
}
