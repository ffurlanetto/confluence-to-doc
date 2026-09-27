package crypto

import (
	"bytes"
	"errors"
	"testing"
)

func newTestSealer(t *testing.T) *Sealer {
	t.Helper()
	s, err := NewSealer(bytes.Repeat([]byte{7}, 32))
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestSealOpenRoundTrip(t *testing.T) {
	s := newTestSealer(t)
	ct, err := s.Seal([]byte("my-pat"), []byte("user-1"))
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(ct, []byte("my-pat")) {
		t.Fatal("ciphertext leaks plaintext")
	}
	pt, err := s.Open(ct, []byte("user-1"))
	if err != nil {
		t.Fatal(err)
	}
	if string(pt) != "my-pat" {
		t.Fatalf("got %q", pt)
	}
}

func TestOpenRejectsWrongAssociatedData(t *testing.T) {
	s := newTestSealer(t)
	ct, _ := s.Seal([]byte("my-pat"), []byte("user-1"))
	if _, err := s.Open(ct, []byte("user-2")); !errors.Is(err, ErrInvalidCiphertext) {
		t.Fatalf("expected ErrInvalidCiphertext, got %v", err)
	}
}

func TestOpenRejectsTamperedCiphertext(t *testing.T) {
	s := newTestSealer(t)
	ct, _ := s.Seal([]byte("my-pat"), nil)
	ct[len(ct)-1] ^= 0xff
	if _, err := s.Open(ct, nil); !errors.Is(err, ErrInvalidCiphertext) {
		t.Fatalf("expected ErrInvalidCiphertext, got %v", err)
	}
	if _, err := s.Open([]byte{1, 2}, nil); !errors.Is(err, ErrInvalidCiphertext) {
		t.Fatalf("expected ErrInvalidCiphertext for short input, got %v", err)
	}
}

func TestNewSealerRejectsBadKey(t *testing.T) {
	if _, err := NewSealer([]byte("short")); err == nil {
		t.Fatal("expected error")
	}
}
