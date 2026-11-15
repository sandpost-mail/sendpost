package secrets

import (
	"crypto/hmac"
	"crypto/sha256"
	"fmt"
	"log/slog"
)

// MinSecretKeyBytes is the shortest SECRET_KEY DeriveKeys accepts. The config
// package enforces the same bound (and an entropy check) at startup.
const MinSecretKeyBytes = 32

// Purpose labels. Every purpose key is HMAC-SHA256(SECRET_KEY, label). The
// labels are part of the stored data format: changing one invalidates the
// sessions, tokens, credentials, signed URLs or relay passwords it protects.
// Each label must be unique (see TestPurposeLabelsAreDistinct).
const (
	labelSession        = "sandpost/session/v1"
	labelCSRF           = "sandpost/csrf/v1"
	labelAPIToken       = "sandpost/api-token/v1"
	labelSMTPCredential = "sandpost/smtp-credential/v1" //nolint:gosec // G101: a KDF label, not a credential
	labelSignedURL      = "sandpost/signed-url/v1"
	labelTransport      = "sandpost/transport-secret/v1"
)

// ErrWeakSecretKey is returned by DeriveKeys for a SECRET_KEY shorter than
// MinSecretKeyBytes.
var ErrWeakSecretKey = fmt.Errorf("secret key must be at least %d bytes", MinSecretKeyBytes)

// Purpose keys. Distinct types make it a compile error to hand one purpose's
// key to another purpose's consumer.
type (
	// SessionKey hashes session cookies before they are stored.
	SessionKey []byte
	// CSRFKey derives the CSRF token of a session.
	CSRFKey []byte
	// APITokenKey hashes API tokens before they are stored.
	APITokenKey []byte
	// SMTPCredentialKey hashes SMTP credential secrets before they are stored.
	SMTPCredentialKey []byte
	// SignedURLKey signs preview URLs.
	SignedURLKey []byte
	// TransportKey encrypts relay passwords at rest (see NewBox).
	TransportKey []byte
)

// Keys holds every purpose key derived from SECRET_KEY. It is built once at
// startup so only the command and this package ever see the root secret.
type Keys struct {
	Session        SessionKey
	CSRF           CSRFKey
	APIToken       APITokenKey
	SMTPCredential SMTPCredentialKey
	SignedURL      SignedURLKey
	Transport      TransportKey
}

// DeriveKeys derives every purpose key from secretKey.
func DeriveKeys(secretKey string) (Keys, error) {
	if len(secretKey) < MinSecretKeyBytes {
		return Keys{}, ErrWeakSecretKey
	}
	return Keys{
		Session:        derive(secretKey, labelSession),
		CSRF:           derive(secretKey, labelCSRF),
		APIToken:       derive(secretKey, labelAPIToken),
		SMTPCredential: derive(secretKey, labelSMTPCredential),
		SignedURL:      derive(secretKey, labelSignedURL),
		Transport:      derive(secretKey, labelTransport),
	}, nil
}

// MustDeriveKeys is DeriveKeys for fixed keys in tests; it panics on error.
func MustDeriveKeys(secretKey string) Keys {
	k, err := DeriveKeys(secretKey)
	if err != nil {
		panic(fmt.Sprintf("secrets: %v", err))
	}
	return k
}

// String never prints key material.
func (Keys) String() string { return "secrets.Keys{redacted}" }

// GoString never prints key material, including under %#v.
func (Keys) GoString() string { return "secrets.Keys{redacted}" }

// LogValue implements slog.LogValuer and never exposes key material.
func (Keys) LogValue() slog.Value { return slog.StringValue("redacted") }

func derive(secretKey, label string) []byte {
	mac := hmac.New(sha256.New, []byte(secretKey))
	mac.Write([]byte(label))
	return mac.Sum(nil)
}
