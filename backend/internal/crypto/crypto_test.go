package crypto

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
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

func key(b byte) []byte { return bytes.Repeat([]byte{b}, 32) }

// legacySeal is the v1 layout written before key rings existed.
func legacySeal(t *testing.T, secret, plaintext, ad []byte) []byte {
	t.Helper()
	block, _ := aes.NewCipher(secret)
	aead, _ := cipher.NewGCM(block)
	nonce := make([]byte, aead.NonceSize())
	out := append([]byte{1}, nonce...)
	return aead.Seal(out, nonce, plaintext, ad)
}

func TestKeyRingRotation(t *testing.T) {
	old, _ := NewSealer(key(1)) // a single ENCRYPTION_KEY, id "default"
	before, _ := old.Seal([]byte("pat-1"), []byte("u1"))
	legacy := legacySeal(t, key(1), []byte("pat-0"), []byte("u0"))

	ring, err := NewKeyRing([]Key{{ID: "2026-10", Secret: key(2)}, {ID: DefaultKeyID, Secret: key(1)}})
	if err != nil {
		t.Fatal(err)
	}
	for name, tc := range map[string]struct {
		ct, ad []byte
		want   string
	}{
		"written by the single key": {before, []byte("u1"), "pat-1"},
		"written before key rings":  {legacy, []byte("u0"), "pat-0"},
	} {
		pt, err := ring.Open(tc.ct, tc.ad)
		if err != nil || string(pt) != tc.want {
			t.Errorf("%s: got %q, %v", name, pt, err)
		}
		if !ring.NeedsRotation(tc.ct) {
			t.Errorf("%s: must be re-encrypted with the current key", name)
		}
	}

	fresh, _ := ring.Seal([]byte("pat-2"), []byte("u2"))
	if ring.NeedsRotation(fresh) || ring.CurrentKeyID() != "2026-10" {
		t.Error("a ciphertext from the current key needs no rotation")
	}
	if !bytes.Contains(fresh, []byte("2026-10")) {
		t.Error("the key id is recorded in the ciphertext")
	}
	if _, err := old.Open(fresh, []byte("u2")); !errors.Is(err, ErrInvalidCiphertext) {
		t.Error("the old key alone cannot open what the new key wrote")
	}

	// Renaming a key keeps its ciphertexts readable (the id is only a hint).
	renamed, _ := NewKeyRing([]Key{{ID: "renamed", Secret: key(1)}})
	if pt, err := renamed.Open(before, []byte("u1")); err != nil || string(pt) != "pat-1" {
		t.Errorf("renamed key: %q %v", pt, err)
	}
}

func TestKeyIDIsAuthenticated(t *testing.T) {
	ring, _ := NewKeyRing([]Key{{ID: "aaaa", Secret: key(1)}, {ID: "bbbb", Secret: key(2)}})
	ct, _ := ring.Seal([]byte("pat"), []byte("u"))
	copy(ct[2:6], "bbbb") // claim the other key
	if _, err := ring.Open(ct, []byte("u")); !errors.Is(err, ErrInvalidCiphertext) {
		t.Fatalf("tampered key id accepted: %v", err)
	}
}

func TestNewKeyRingValidation(t *testing.T) {
	cases := map[string][]Key{
		"empty":         nil,
		"bad id":        {{ID: "no spaces", Secret: key(1)}},
		"duplicate id":  {{ID: "a", Secret: key(1)}, {ID: "a", Secret: key(2)}},
		"short secret":  {{ID: "a", Secret: []byte("short")}},
		"reused secret": {{ID: "a", Secret: key(1)}, {ID: "b", Secret: key(1)}},
	}
	for name, keys := range cases {
		if _, err := NewKeyRing(keys); err == nil {
			t.Errorf("%s: want an error", name)
		}
	}
}

func TestOpenRejectsMalformedKeyedCiphertext(t *testing.T) {
	s := newTestSealer(t)
	for _, ct := range [][]byte{{2}, {2, 5, 'a'}, {2, 0}, {9, 1, 2, 3}, {}} {
		if _, err := s.Open(ct, nil); !errors.Is(err, ErrInvalidCiphertext) {
			t.Errorf("%v: want ErrInvalidCiphertext, got %v", ct, err)
		}
	}
}
