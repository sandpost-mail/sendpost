package passwords

import (
	"errors"
	"strings"
	"testing"
)

func TestHashAndVerify(t *testing.T) {
	h, err := Hash("correct horse battery staple")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(h, "$argon2id$v=19$m=65536,t=3,p=2$") {
		t.Errorf("format: %s", h)
	}
	if strings.Contains(h, "correct horse") {
		t.Error("hash contains the password")
	}
	if err := Verify("correct horse battery staple", h); err != nil {
		t.Errorf("verify: %v", err)
	}
	if err := Verify("wrong", h); !errors.Is(err, ErrMismatch) {
		t.Errorf("want mismatch, got %v", err)
	}
	h2, _ := Hash("correct horse battery staple")
	if h == h2 {
		t.Error("salts must differ")
	}
}

func TestVerifyRejectsMalformedHashes(t *testing.T) {
	for _, bad := range []string{
		"", "plain", "$bcrypt$x$y$z$w", "$argon2id$v=18$m=1,t=1,p=1$c2FsdA$aGFzaA",
		"$argon2id$v=19$m=0,t=1,p=1$c2FsdA$aGFzaA", "$argon2id$v=19$m=8,t=1,p=1$!!$aGFzaA",
		"$argon2id$v=19$m=8,t=1,p=1$c2FsdA$", "$argon2id$v=19$nonsense$c2FsdA$aGFzaA",
	} {
		err := Verify("x", bad)
		if err == nil || errors.Is(err, ErrMismatch) {
			t.Errorf("%q: want a format error, got %v", bad, err)
		}
	}
}
