package authz

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"time"
)

const (
	signKeyContext = "sandpost/signed-url/v1"
	// DefaultGrantTTL is how long a signed preview URL stays valid.
	DefaultGrantTTL = 5 * time.Minute
	maxGrantTTL     = time.Hour
)

// ErrBadSignature is returned for a missing, malformed, tampered, mismatched
// or expired signature. Callers answer 404 so nothing is revealed.
var ErrBadSignature = errors.New("invalid or expired signature")

// Signer mints and verifies short lived URLs for content shown in the
// sandboxed preview iframe. The iframe has an opaque origin and carries no
// cookies, so the URL itself must prove the earlier read check. A signature
// binds one project, one message and one resource, so it can not be replayed
// on other ids.
type Signer struct {
	key []byte
	now func() time.Time
}

// NewSigner derives a dedicated key from the instance SECRET_KEY.
func NewSigner(secretKey string) *Signer {
	return newSigner(secretKey, time.Now)
}

func newSigner(secretKey string, now func() time.Time) *Signer {
	mac := hmac.New(sha256.New, []byte(secretKey))
	mac.Write([]byte(signKeyContext))
	return &Signer{key: mac.Sum(nil), now: now}
}

// Sign returns the expiry (unix seconds) and signature for a resource, for
// example "html" or "attachment/2". The caller must already hold
// CapReadMessages on the project.
func (s *Signer) Sign(scope Scope, messageID int64, resource string, ttl time.Duration) (int64, string) {
	ttl = min(max(ttl, time.Second), maxGrantTTL)
	exp := s.now().Add(ttl).Unix()
	return exp, s.mac(scope.projectID, messageID, resource, exp)
}

// Verify checks a signature and returns the read Scope it grants. The scope
// is bound to projectID, which still has to be used together with messageID in
// the project scoped queries.
func (s *Signer) Verify(projectID, messageID int64, resource string, exp int64, sig string) (Scope, error) {
	if sig == "" || exp <= s.now().Unix() {
		return Scope{}, ErrBadSignature
	}
	want := s.mac(projectID, messageID, resource, exp)
	if !hmac.Equal([]byte(want), []byte(sig)) {
		return Scope{}, ErrBadSignature
	}
	return Scope{projectID: projectID, role: RoleViewer}, nil
}

func (s *Signer) mac(projectID, messageID int64, resource string, exp int64) string {
	m := hmac.New(sha256.New, s.key)
	_, _ = fmt.Fprintf(m, "%d|%d|%s|%d", projectID, messageID, resource, exp)
	return base64.RawURLEncoding.EncodeToString(m.Sum(nil))
}
