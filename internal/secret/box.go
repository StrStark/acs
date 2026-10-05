// Package secret encrypts small secrets (access key secrets, webhook signing
// keys) at rest with a server master key.
package secret

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"strings"
)

type Box struct {
	aead cipher.AEAD
	key  []byte
}

// LoadOrCreate returns a Box using the base64 key in env if set, otherwise the
// key file at path, generating it on first use.
func LoadOrCreate(path, env string) (*Box, error) {
	var key []byte
	if env != "" {
		k, err := base64.StdEncoding.DecodeString(strings.TrimSpace(env))
		if err != nil {
			return nil, fmt.Errorf("master key: %w", err)
		}
		key = k
	} else {
		b, err := os.ReadFile(path)
		switch {
		case err == nil:
			k, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(b)))
			if err != nil {
				return nil, fmt.Errorf("master key file %s: %w", path, err)
			}
			key = k
		case errors.Is(err, os.ErrNotExist):
			key = make([]byte, 32)
			rand.Read(key)
			if err := os.WriteFile(path, []byte(base64.StdEncoding.EncodeToString(key)+"\n"), 0o600); err != nil {
				return nil, fmt.Errorf("write master key: %w", err)
			}
		default:
			return nil, err
		}
	}
	return New(key)
}

func New(key []byte) (*Box, error) {
	if len(key) != 32 {
		return nil, errors.New("master key must be 32 bytes")
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	return &Box{aead: aead, key: key}, nil
}

// DeriveKey returns a stable 32-byte key for a purpose (e.g. signing share
// unlock cookies), so the master key itself is never used for two things.
func (b *Box) DeriveKey(purpose string) []byte {
	m := hmac.New(sha256.New, b.key)
	m.Write([]byte(purpose))
	return m.Sum(nil)
}

func (b *Box) Seal(plaintext []byte) []byte {
	nonce := make([]byte, b.aead.NonceSize())
	rand.Read(nonce)
	return b.aead.Seal(nonce, nonce, plaintext, nil)
}

func (b *Box) Open(sealed []byte) ([]byte, error) {
	n := b.aead.NonceSize()
	if len(sealed) < n {
		return nil, errors.New("ciphertext too short")
	}
	return b.aead.Open(nil, sealed[:n], sealed[n:], nil)
}
