package authz

import (
	"errors"
	"testing"
	"time"
)

func TestSignedGrant(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	s := newSigner("0123456789abcdef0123456789abcdef", func() time.Time { return now })
	scope := Trusted(1)
	exp, sig := s.Sign(scope, 10, "html", time.Minute)

	tests := []struct {
		name string
		pid  int64
		mid  int64
		res  string
		exp  int64
		sig  string
		ok   bool
	}{
		{"valid", 1, 10, "html", exp, sig, true},
		{"replayed on a foreign project", 2, 10, "html", exp, sig, false},
		{"replayed on another message", 1, 11, "html", exp, sig, false},
		{"wrong resource", 1, 10, "attachment/0", exp, sig, false},
		{"extended expiry", 1, 10, "html", exp + 3600, sig, false},
		{"tampered signature", 1, 10, "html", exp, sig[:len(sig)-2] + "AA", false},
		{"empty signature", 1, 10, "html", exp, "", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := s.Verify(tc.pid, tc.mid, tc.res, tc.exp, tc.sig)
			if tc.ok {
				if err != nil || got.ProjectID() != 1 || got.Role() != RoleViewer {
					t.Fatalf("Verify = %+v, %v", got, err)
				}
				return
			}
			if !errors.Is(err, ErrBadSignature) {
				t.Errorf("Verify error = %v, want ErrBadSignature", err)
			}
		})
	}

	now = now.Add(2 * time.Minute)
	if _, err := s.Verify(1, 10, "html", exp, sig); !errors.Is(err, ErrBadSignature) {
		t.Errorf("expired signature accepted: %v", err)
	}
}

func TestSignerKeysAreIndependent(t *testing.T) {
	now := func() time.Time { return time.Unix(1_700_000_000, 0) }
	a := newSigner("0123456789abcdef0123456789abcdef", now)
	b := newSigner("fedcba9876543210fedcba9876543210", now)
	exp, sig := a.Sign(Trusted(1), 1, "html", time.Minute)
	if _, err := b.Verify(1, 1, "html", exp, sig); err == nil {
		t.Error("a signature must not verify under another key")
	}
}

func TestSignTTLIsBounded(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	s := newSigner("0123456789abcdef0123456789abcdef", func() time.Time { return now })
	exp, _ := s.Sign(Trusted(1), 1, "html", 1000*time.Hour)
	if exp != now.Add(maxGrantTTL).Unix() {
		t.Errorf("ttl not capped: %d", exp-now.Unix())
	}
}
