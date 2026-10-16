// Package passwords hashes and verifies human passwords with argon2id.
// Machine secrets (SMTP credentials, API tokens) do not use this package, they
// are server generated and stored as keyed HMACs.
package passwords

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"

	"golang.org/x/crypto/argon2"
)

// argon2id parameters, in the range recommended by RFC 9106 for interactive use.
const (
	memoryKiB = 64 * 1024
	timeCost  = 3
	threads   = 2
	saltLen   = 16
	keyLen    = 32
)

// ErrMismatch is returned by Verify when the password is wrong.
var ErrMismatch = errors.New("password does not match")

var enc = base64.RawStdEncoding

// Hash returns the PHC string encoding of the argon2id hash of password.
func Hash(password string) (string, error) {
	salt := make([]byte, saltLen)
	if _, err := rand.Read(salt); err != nil {
		return "", fmt.Errorf("salt: %w", err)
	}
	key := argon2.IDKey([]byte(password), salt, timeCost, memoryKiB, threads, keyLen)
	return fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s",
		argon2.Version, memoryKiB, timeCost, threads, enc.EncodeToString(salt), enc.EncodeToString(key)), nil
}

// Verify checks password against a hash made by Hash in constant time. It
// returns ErrMismatch for a wrong password and another error for a malformed hash.
func Verify(password, encoded string) error {
	parts := strings.Split(encoded, "$")
	if len(parts) != 6 || parts[1] != "argon2id" {
		return errors.New("unsupported password hash format")
	}
	var version int
	if _, err := fmt.Sscanf(parts[2], "v=%d", &version); err != nil || version != argon2.Version {
		return errors.New("unsupported argon2 version")
	}
	var mem, t uint32
	var p uint8
	if _, err := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &mem, &t, &p); err != nil || mem == 0 || t == 0 || p == 0 {
		return errors.New("invalid argon2 parameters")
	}
	salt, err := enc.DecodeString(parts[4])
	if err != nil {
		return errors.New("invalid salt encoding")
	}
	want, err := enc.DecodeString(parts[5])
	if err != nil || len(want) == 0 {
		return errors.New("invalid hash encoding")
	}
	got := argon2.IDKey([]byte(password), salt, t, mem, p, uint32(len(want))) //nolint:gosec // len bounded by decoded hash
	if subtle.ConstantTimeCompare(got, want) != 1 {
		return ErrMismatch
	}
	return nil
}
