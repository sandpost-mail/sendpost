package secrets_test

import (
	"bytes"
	"errors"
	"strings"
	"testing"

	"github.com/sandpost-mail/sandpost/internal/secrets"
)

const key = "0123456789abcdef0123456789abcdef"

func TestSealOpenRoundTrip(t *testing.T) {
	box := secrets.New(key)
	for _, plain := range []string{"", "hunter2", strings.Repeat("x", 4096), "pässwörd"} {
		blob, err := box.Seal([]byte(plain), "transport:1")
		if err != nil {
			t.Fatal(err)
		}
		if plain != "" && bytes.Contains(blob, []byte(plain)) {
			t.Errorf("ciphertext contains the plaintext %q", plain)
		}
		got, err := box.Open(blob, "transport:1")
		if err != nil || string(got) != plain {
			t.Errorf("Open = %q, %v; want %q", got, err, plain)
		}
	}
}

func TestSealIsRandomised(t *testing.T) {
	box := secrets.New(key)
	a, _ := box.Seal([]byte("same"), "ctx")
	b, _ := box.Seal([]byte("same"), "ctx")
	if bytes.Equal(a, b) {
		t.Error("two seals of the same value must differ (random nonce)")
	}
}

func TestOpenFailsClosed(t *testing.T) {
	box := secrets.New(key)
	blob, _ := box.Seal([]byte("secret"), "transport:1")
	flipped := append([]byte(nil), blob...)
	flipped[len(flipped)-1] ^= 1
	badVersion := append([]byte(nil), blob...)
	badVersion[0] = 9

	tests := []struct {
		name string
		box  *secrets.Box
		blob []byte
		aad  string
	}{
		{"wrong key", secrets.New("ffffffffffffffffffffffffffffffff"), blob, "transport:1"},
		{"wrong context", box, blob, "transport:2"},
		{"tampered", box, flipped, "transport:1"},
		{"unknown version", box, badVersion, "transport:1"},
		{"empty", box, nil, "transport:1"},
		{"truncated", box, blob[:5], "transport:1"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := tc.box.Open(tc.blob, tc.aad)
			if !errors.Is(err, secrets.ErrDecrypt) || got != nil {
				t.Errorf("Open = %q, %v; want ErrDecrypt", got, err)
			}
		})
	}
}
