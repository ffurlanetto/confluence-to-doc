// Package crypto encrypts user secrets (Confluence PATs) at rest with
// AES-256-GCM, under a key ring so the key can be rotated without losing the
// secrets it protects.
//
// Ciphertext layouts:
//
//	v2: 0x02 | len(keyID) | keyID | nonce(12) | sealed   — written today
//	v1: 0x01 | nonce(12) | sealed                         — before key rings
//
// The key id is authenticated with the associated data, so it cannot be
// swapped; it is a hint for which key to try first, and a v1 ciphertext or an
// unknown id is tried against every key of the ring.
package crypto

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"errors"
	"fmt"
	"regexp"
)

const (
	versionLegacy byte = 1
	versionKeyed  byte = 2
)

var ErrInvalidCiphertext = errors.New("crypto: invalid ciphertext")

// DefaultKeyID names the key given as a single ENCRYPTION_KEY. Naming the old
// key "default" in a key ring keeps the ciphertexts it wrote on the fast path.
const DefaultKeyID = "default"

var keyIDPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{1,32}$`)

// Key is one key of the ring.
type Key struct {
	ID     string
	Secret []byte
}

// Sealer encrypts and decrypts small secrets. The associated data binds a
// ciphertext to its owner so a row copied to another user cannot be decrypted.
// It is safe for concurrent use.
type Sealer struct {
	current string
	order   []string
	aeads   map[string]cipher.AEAD
}

// NewSealer returns a sealer with a single key, named DefaultKeyID.
func NewSealer(key []byte) (*Sealer, error) {
	return NewKeyRing([]Key{{ID: DefaultKeyID, Secret: key}})
}

// NewKeyRing returns a sealer that encrypts with the first key and decrypts
// with any of them.
func NewKeyRing(keys []Key) (*Sealer, error) {
	if len(keys) == 0 {
		return nil, errors.New("crypto: the key ring is empty")
	}
	s := &Sealer{current: keys[0].ID, aeads: make(map[string]cipher.AEAD, len(keys))}
	secrets := map[string]bool{}
	for _, k := range keys {
		if !keyIDPattern.MatchString(k.ID) {
			return nil, fmt.Errorf("crypto: invalid key id %q (1-32 letters, digits, '-' or '_')", k.ID)
		}
		if _, dup := s.aeads[k.ID]; dup {
			return nil, fmt.Errorf("crypto: key id %q is used twice", k.ID)
		}
		if len(k.Secret) != 32 {
			return nil, fmt.Errorf("crypto: key %q must be 32 bytes, got %d", k.ID, len(k.Secret))
		}
		if secrets[string(k.Secret)] {
			return nil, fmt.Errorf("crypto: key %q repeats another key's secret", k.ID)
		}
		secrets[string(k.Secret)] = true
		block, err := aes.NewCipher(k.Secret)
		if err != nil {
			return nil, err
		}
		aead, err := cipher.NewGCM(block)
		if err != nil {
			return nil, err
		}
		s.aeads[k.ID] = aead
		s.order = append(s.order, k.ID)
	}
	return s, nil
}

// CurrentKeyID is the id of the key new secrets are encrypted with.
func (s *Sealer) CurrentKeyID() string { return s.current }

func (s *Sealer) Seal(plaintext, associatedData []byte) ([]byte, error) {
	aead := s.aeads[s.current]
	nonce := make([]byte, aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, err
	}
	out := make([]byte, 0, 2+len(s.current)+len(nonce)+len(plaintext)+aead.Overhead())
	out = append(out, versionKeyed, lengthByte(s.current))
	out = append(out, s.current...)
	out = append(out, nonce...)
	return aead.Seal(out, nonce, plaintext, keyedAD(s.current, associatedData)), nil
}

func (s *Sealer) Open(ciphertext, associatedData []byte) ([]byte, error) {
	id, nonce, sealed, err := s.split(ciphertext)
	if err != nil {
		return nil, err
	}
	ad := associatedData
	if ciphertext[0] == versionKeyed {
		ad = keyedAD(id, associatedData)
	}
	// The named key first, then the others: a ring renamed or rebuilt by hand
	// still opens what it can.
	if aead, ok := s.aeads[id]; ok {
		if pt, err := aead.Open(nil, nonce, sealed, ad); err == nil {
			return pt, nil
		}
	}
	for _, other := range s.order {
		if other == id {
			continue
		}
		if pt, err := s.aeads[other].Open(nil, nonce, sealed, ad); err == nil {
			return pt, nil
		}
	}
	return nil, ErrInvalidCiphertext
}

// NeedsRotation reports whether ciphertext was not written with the current
// key, and should be re-encrypted.
func (s *Sealer) NeedsRotation(ciphertext []byte) bool {
	id, _, _, err := s.split(ciphertext)
	return err != nil || id != s.current
}

// split parses either layout; the key id is empty for a v1 ciphertext.
func (s *Sealer) split(ciphertext []byte) (id string, nonce, sealed []byte, err error) {
	const nonceSize, overhead = 12, 16
	if len(ciphertext) == 0 {
		return "", nil, nil, ErrInvalidCiphertext
	}
	rest := ciphertext[1:]
	switch ciphertext[0] {
	case versionLegacy:
	case versionKeyed:
		if len(rest) < 1 || len(rest) < 1+int(rest[0]) {
			return "", nil, nil, ErrInvalidCiphertext
		}
		id, rest = string(rest[1:1+int(rest[0])]), rest[1+int(rest[0]):]
		if id == "" {
			return "", nil, nil, ErrInvalidCiphertext
		}
	default:
		return "", nil, nil, ErrInvalidCiphertext
	}
	if len(rest) < nonceSize+overhead {
		return "", nil, nil, ErrInvalidCiphertext
	}
	return id, rest[:nonceSize], rest[nonceSize:], nil
}

// keyedAD authenticates the key id along with the caller's associated data.
func keyedAD(id string, associatedData []byte) []byte {
	ad := make([]byte, 0, 1+len(id)+len(associatedData))
	ad = append(ad, lengthByte(id))
	ad = append(ad, id...)
	return append(ad, associatedData...)
}

// lengthByte encodes the length of a key id. It always fits: ids are
// validated to at most 32 bytes, and an id read back from a ciphertext comes
// from a one-byte length.
func lengthByte(id string) byte {
	return byte(min(len(id), 255)) //nolint:gosec // G115: bounded to 255 just above
}
