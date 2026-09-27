// Package crypto encrypts user secrets (Confluence PATs) at rest with
// AES-256-GCM. The ciphertext layout is: version(1) | nonce(12) | sealed.
package crypto

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"errors"
	"fmt"
)

const version byte = 1

var ErrInvalidCiphertext = errors.New("crypto: invalid ciphertext")

// Sealer encrypts and decrypts small secrets. The associated data binds a
// ciphertext to its owner so a row copied to another user cannot be decrypted.
type Sealer struct {
	aead cipher.AEAD
}

func NewSealer(key []byte) (*Sealer, error) {
	if len(key) != 32 {
		return nil, fmt.Errorf("crypto: key must be 32 bytes, got %d", len(key))
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	return &Sealer{aead: aead}, nil
}

func (s *Sealer) Seal(plaintext, associatedData []byte) ([]byte, error) {
	nonce := make([]byte, s.aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, err
	}
	out := make([]byte, 0, 1+len(nonce)+len(plaintext)+s.aead.Overhead())
	out = append(out, version)
	out = append(out, nonce...)
	return s.aead.Seal(out, nonce, plaintext, associatedData), nil
}

func (s *Sealer) Open(ciphertext, associatedData []byte) ([]byte, error) {
	ns := s.aead.NonceSize()
	if len(ciphertext) < 1+ns+s.aead.Overhead() || ciphertext[0] != version {
		return nil, ErrInvalidCiphertext
	}
	nonce, sealed := ciphertext[1:1+ns], ciphertext[1+ns:]
	pt, err := s.aead.Open(nil, nonce, sealed, associatedData)
	if err != nil {
		return nil, ErrInvalidCiphertext
	}
	return pt, nil
}
