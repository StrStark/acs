package secret

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"os"

	"golang.org/x/crypto/argon2"
)

// ErrWrongPassphrase is returned when a keyring cannot be unlocked.
var ErrWrongPassphrase = errors.New("wrong recovery passphrase")

// Keyring is the master key locked with a recovery passphrase. It is copied
// to every storage backend so a new machine can restore encrypted secrets.
type Keyring struct {
	Version int    `json:"version"`
	KDF     string `json:"kdf"`
	Time    uint32 `json:"time"`
	Memory  uint32 `json:"memory"`
	Threads uint8  `json:"threads"`
	Salt    []byte `json:"salt"`
	// Sealed is nonce || AES-GCM(master key).
	Sealed []byte `json:"sealed"`
}

func kek(passphrase string, k *Keyring) []byte {
	return argon2.IDKey([]byte(passphrase), k.Salt, k.Time, k.Memory, k.Threads, 32)
}

// Lock seals the box's master key with passphrase.
func (b *Box) Lock(passphrase string) (*Keyring, error) {
	k := &Keyring{Version: 1, KDF: "argon2id", Time: 3, Memory: 64 * 1024, Threads: 2, Salt: make([]byte, 16)}
	rand.Read(k.Salt)
	aead, err := gcm(kek(passphrase, k))
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, aead.NonceSize())
	rand.Read(nonce)
	k.Sealed = aead.Seal(nonce, nonce, b.key, nil)
	return k, nil
}

// Unlock recovers the master key from a keyring.
func (k *Keyring) Unlock(passphrase string) ([]byte, error) {
	if k.KDF != "argon2id" || len(k.Salt) == 0 {
		return nil, errors.New("unsupported keyring format")
	}
	aead, err := gcm(kek(passphrase, k))
	if err != nil {
		return nil, err
	}
	n := aead.NonceSize()
	if len(k.Sealed) < n {
		return nil, errors.New("corrupt keyring")
	}
	key, err := aead.Open(nil, k.Sealed[:n], k.Sealed[n:], nil)
	if err != nil {
		return nil, ErrWrongPassphrase
	}
	return key, nil
}

func (k *Keyring) Marshal() []byte {
	b, _ := json.MarshalIndent(k, "", "  ")
	return b
}

func ParseKeyring(data []byte) (*Keyring, error) {
	var k Keyring
	if err := json.Unmarshal(data, &k); err != nil {
		return nil, err
	}
	return &k, nil
}

// Matches reports whether key is this box's master key.
func (b *Box) Matches(key []byte) bool { return string(b.key) == string(key) }

func gcm(key []byte) (cipher.AEAD, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}

// WriteKeyFile stores a master key in the format LoadOrCreate reads.
func WriteKeyFile(path string, key []byte) error {
	return os.WriteFile(path, []byte(base64.StdEncoding.EncodeToString(key)+"\n"), 0o600)
}
