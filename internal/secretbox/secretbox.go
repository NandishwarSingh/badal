// Package secretbox seals credentials that must be stored but not read back
// from a database dump.
//
// An IMAP app password is a live credential to the entire mailbox. Even in a
// single-user system it should not sit in plaintext next to everything else
// that gets backed up nightly and replicated to a laptop.
package secretbox

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"fmt"
	"io"
)

type Box struct{ aead cipher.AEAD }

// New derives the key from a passphrase. SHA-256 is adequate here because the
// input is a 32-byte random secret from the environment, not a human password
// that would need a slow KDF.
func New(secret string) (*Box, error) {
	if len(secret) < 16 {
		return nil, fmt.Errorf("BADAL_SECRET_KEY must be at least 16 characters")
	}
	sum := sha256.Sum256([]byte(secret))
	block, err := aes.NewCipher(sum[:])
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	return &Box{aead: aead}, nil
}

// Seal returns nonce||ciphertext. GCM authenticates, so a tampered row fails
// to open rather than decrypting to garbage.
func (b *Box) Seal(plain string) ([]byte, error) {
	nonce := make([]byte, b.aead.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, err
	}
	return b.aead.Seal(nonce, nonce, []byte(plain), nil), nil
}

func (b *Box) Open(sealed []byte) (string, error) {
	n := b.aead.NonceSize()
	if len(sealed) < n {
		return "", fmt.Errorf("ciphertext too short")
	}
	out, err := b.aead.Open(nil, sealed[:n], sealed[n:], nil)
	if err != nil {
		return "", fmt.Errorf("decrypt failed (wrong BADAL_SECRET_KEY?): %w", err)
	}
	return string(out), nil
}
