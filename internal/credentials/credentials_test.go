package credentials_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/sandpost-mail/sandpost/internal/credentials"
	"github.com/sandpost-mail/sandpost/internal/testutil"
)

const key = "0123456789abcdef0123456789abcdef"

func TestCreateVerifyRevoke(t *testing.T) {
	ctx := context.Background()
	pool := testutil.Pool(t)
	pid := testutil.Project(t, pool, "alpha")
	other := testutil.Project(t, pool, "beta")
	svc := credentials.New(pool, key)

	issued, err := svc.Create(ctx, pid, "  ci pipeline ")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(issued.Username, "sp_") || !strings.HasPrefix(issued.Secret, "sps_") {
		t.Fatalf("unexpected format %q %q", issued.Username, issued.Secret)
	}
	if issued.Label != "ci pipeline" {
		t.Errorf("label %q", issued.Label)
	}
	if len(issued.Secret) < 40 {
		t.Errorf("secret too short: %d", len(issued.Secret))
	}

	id, err := svc.Verify(ctx, issued.Username, issued.Secret)
	if err != nil || id.ProjectID != pid || id.CredentialID != issued.ID {
		t.Fatalf("verify: %+v %v", id, err)
	}

	for name, args := range map[string][2]string{
		"wrong secret":     {issued.Username, issued.Secret + "x"},
		"empty secret":     {issued.Username, ""},
		"unknown username": {"sp_nobody", issued.Secret},
		"swapped":          {issued.Secret, issued.Username},
	} {
		if _, err := svc.Verify(ctx, args[0], args[1]); !errors.Is(err, credentials.ErrInvalid) {
			t.Errorf("%s: got %v", name, err)
		}
	}

	// A different server key must not verify the same secret.
	if _, err := credentials.New(pool, key+"other").Verify(ctx, issued.Username, issued.Secret); !errors.Is(err, credentials.ErrInvalid) {
		t.Errorf("other key accepted: %v", err)
	}

	list, err := svc.List(ctx, pid)
	if err != nil || len(list) != 1 || list[0].LastUsedAt == nil {
		t.Fatalf("list: %+v %v", list, err)
	}
	if l, _ := svc.List(ctx, other); len(l) != 0 {
		t.Errorf("credentials leaked across projects: %+v", l)
	}

	if err := svc.Revoke(ctx, other, issued.ID); !errors.Is(err, credentials.ErrNotFound) {
		t.Errorf("revoke from another project: %v", err)
	}
	if err := svc.Revoke(ctx, pid, issued.ID); err != nil {
		t.Fatal(err)
	}
	if err := svc.Revoke(ctx, pid, issued.ID); !errors.Is(err, credentials.ErrNotFound) {
		t.Errorf("second revoke: %v", err)
	}
	if _, err := svc.Verify(ctx, issued.Username, issued.Secret); !errors.Is(err, credentials.ErrInvalid) {
		t.Errorf("revoked credential accepted: %v", err)
	}
}

func TestSecretsAreNotStoredAndUnique(t *testing.T) {
	ctx := context.Background()
	pool := testutil.Pool(t)
	pid := testutil.Project(t, pool, "alpha")
	svc := credentials.New(pool, key)

	a, _ := svc.Create(ctx, pid, "")
	b, _ := svc.Create(ctx, pid, "")
	if a.Secret == b.Secret || a.Username == b.Username {
		t.Fatal("credentials must be unique")
	}
	var n int
	err := pool.QueryRow(ctx,
		`SELECT count(*) FROM smtp_credentials WHERE position($1 in encode(secret_hmac,'escape')) > 0`,
		a.Secret).Scan(&n)
	if err != nil || n != 0 {
		t.Fatalf("plaintext secret found in storage: %d %v", n, err)
	}
}

func TestCreateErrors(t *testing.T) {
	ctx := context.Background()
	pool := testutil.Pool(t)
	svc := credentials.New(pool, key)
	if _, err := svc.Create(ctx, 999, "x"); !errors.Is(err, credentials.ErrProjectNotFound) {
		t.Errorf("unknown project: %v", err)
	}
	pid := testutil.Project(t, pool, "alpha")
	if _, err := svc.Create(ctx, pid, strings.Repeat("a", 101)); !errors.Is(err, credentials.ErrLabelTooLong) {
		t.Errorf("long label: %v", err)
	}
}

func TestDatabaseErrorsAreNotInvalid(t *testing.T) {
	ctx := context.Background()
	pool := testutil.Pool(t)
	svc := credentials.New(pool, key)
	pool.Close()
	if _, err := svc.Verify(ctx, "sp_x", "y"); err == nil || errors.Is(err, credentials.ErrInvalid) {
		t.Errorf("a database failure must not look like a bad password: %v", err)
	}
	if _, err := svc.List(ctx, 1); err == nil {
		t.Error("list should fail")
	}
	if err := svc.Revoke(ctx, 1, 1); err == nil {
		t.Error("revoke should fail")
	}
	if _, err := svc.Create(ctx, 1, ""); err == nil || errors.Is(err, credentials.ErrProjectNotFound) {
		t.Errorf("create: %v", err)
	}
}
