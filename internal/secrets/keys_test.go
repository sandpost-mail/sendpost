package secrets_test

import (
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"testing"

	"github.com/sandpost-mail/sandpost/internal/secrets"
)

// goldenSecretKey and the values below were recorded with the per-package
// derivations that existed before DeriveKeys (auth, tokens, credentials,
// authz.NewSigner, secrets.New). They must never change: every stored session,
// API token, SMTP credential, signed URL and encrypted relay password depends
// on these bytes.
const goldenSecretKey = "0123456789abcdef0123456789abcdef"

func TestDeriveKeysGolden(t *testing.T) {
	k, err := secrets.DeriveKeys(goldenSecretKey)
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		purpose string
		got     []byte
		want    string
	}{
		{"session", k.Session, "2c279485ef1d15501dde5003ff72360249865a4e51b74dc3338db86a227a047b"},
		{"csrf", k.CSRF, "8820860787293137894ed8e9bf644e83e59a323346d0fd44c7e4639d87cd6335"},
		{"api token", k.APIToken, "9762d7c7a338b4bc9e69e952e7d3a96fcec571a00582b00a83d0bbc37e37c676"},
		{"smtp credential", k.SMTPCredential, "005e059d2966dc30c8205f706c44001eb9ea588fd173e317fdc8e41879ed8f4a"},
		{"signed url", k.SignedURL, "ff2b79c43231cf5da57d9b9048e8c06aa4865a072165965d2ed3690bf2bbe97b"},
		{"transport", k.Transport, "e406620f741286d2c84f0f8b623fc4d42ec50298daad46d4c3b78e6891dbccd4"},
	}
	for _, tc := range tests {
		t.Run(tc.purpose, func(t *testing.T) {
			if got := hex.EncodeToString(tc.got); got != tc.want {
				t.Errorf("%s key = %s, want %s", tc.purpose, got, tc.want)
			}
		})
	}
}

// TestBoxOpensGoldenBlob opens a blob sealed by the former secrets.New box,
// so relay passwords stored before the refactor still decrypt.
func TestBoxOpensGoldenBlob(t *testing.T) {
	blob, err := hex.DecodeString("01eba129bbbd01b411d37691e27acbc557a91c342272d14995ceed3cd76f46cad3dce4a3")
	if err != nil {
		t.Fatal(err)
	}
	box := secrets.NewBox(secrets.MustDeriveKeys(goldenSecretKey).Transport)
	got, err := box.Open(blob, "transport:1")
	if err != nil || string(got) != "hunter2" {
		t.Fatalf("Open = %q, %v; want hunter2", got, err)
	}
}

func TestPurposeKeysAreDistinct(t *testing.T) {
	k := secrets.MustDeriveKeys(goldenSecretKey)
	seen := map[string]string{}
	for name, v := range map[string][]byte{
		"session": k.Session, "csrf": k.CSRF, "api token": k.APIToken,
		"smtp credential": k.SMTPCredential, "signed url": k.SignedURL, "transport": k.Transport,
	} {
		h := hex.EncodeToString(v)
		if other, dup := seen[h]; dup {
			t.Errorf("%s and %s share a key: purpose labels must be unique", name, other)
		}
		seen[h] = name
	}
}

func TestDeriveKeysRejectsShortSecret(t *testing.T) {
	for _, s := range []string{"", "short", strings.Repeat("x", secrets.MinSecretKeyBytes-1)} {
		if _, err := secrets.DeriveKeys(s); !errors.Is(err, secrets.ErrWeakSecretKey) {
			t.Errorf("DeriveKeys(%d bytes) err = %v, want ErrWeakSecretKey", len(s), err)
		}
	}
}

func TestMustDeriveKeysPanicsOnShortSecret(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Error("MustDeriveKeys must panic on a short secret")
		}
	}()
	secrets.MustDeriveKeys("short")
}

func TestKeysNeverPrintMaterial(t *testing.T) {
	k := secrets.MustDeriveKeys(goldenSecretKey)
	session := hex.EncodeToString(k.Session)
	var logged strings.Builder
	slog.New(slog.NewTextHandler(&logged, nil)).Info("keys", "keys", k)
	for _, out := range []string{fmt.Sprint(k), fmt.Sprintf("%v %+v %#v %s", k, k, k, k), logged.String()} {
		if strings.Contains(out, "redacted") && !strings.Contains(out, session) && !strings.Contains(out, string(k.Session)) {
			continue
		}
		t.Errorf("formatted keys leak material or are not redacted: %q", out)
	}
}
