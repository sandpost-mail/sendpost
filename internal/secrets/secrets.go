// Package secrets encrypts small secrets at rest (relay passwords) with
// AES-256-GCM. The key is derived from the instance SECRET_KEY, and every
// ciphertext is bound to a context string (for example "transport:<project>")
// so a blob copied to another row does not decrypt.
//
// Rotation: the key is a pure function of SECRET_KEY, so changing SECRET_KEY
// makes every stored blob undecryptable. Open then fails and delivery fails
// closed. After a rotation, owners re-enter their transport passwords.
package secrets

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"errors"
	"fmt"
)

const (
	keyContext = "sandpost/transport-secret/v1"
	versionAES = 1
)

// ErrDecrypt is returned for any blob that can not be opened: wrong key,
// wrong context, corruption or an unknown format. Callers must not send
// anything that depends on the secret.
var ErrDecrypt = errors.New("secret can not be decrypted")

// Box seals and opens secrets with a key derived from SECRET_KEY.
type Box struct {
	aead cipher.AEAD
}

// New derives the encryption key from secretKey. It panics only if the
// standard library refuses a 32 byte AES key, which can not happen.
func New(secretKey string) *Box {
	mac := hmac.New(sha256.New, []byte(secretKey))
	mac.Write([]byte(keyContext))
	block, err := aes.NewCipher(mac.Sum(nil))
	if err != nil {
		panic(fmt.Sprintf("secrets: aes key: %v", err))
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		panic(fmt.Sprintf("secrets: gcm: %v", err))
	}
	return &Box{aead: aead}
}

// Seal encrypts plaintext. The result is version || nonce || ciphertext.
func (b *Box) Seal(plaintext []byte, context string) ([]byte, error) {
	nonce := make([]byte, b.aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, fmt.Errorf("secrets: nonce: %w", err)
	}
	out := make([]byte, 0, 1+len(nonce)+len(plaintext)+b.aead.Overhead())
	out = append(out, versionAES)
	out = append(out, nonce...)
	return b.aead.Seal(out, nonce, plaintext, []byte(context)), nil
}

// Open decrypts a blob made by Seal with the same context.
func (b *Box) Open(blob []byte, context string) ([]byte, error) {
	n := b.aead.NonceSize()
	if len(blob) < 1+n+b.aead.Overhead() || blob[0] != versionAES {
		return nil, ErrDecrypt
	}
	plain, err := b.aead.Open(nil, blob[1:1+n], blob[1+n:], []byte(context))
	if err != nil {
		return nil, ErrDecrypt
	}
	return plain, nil
}
