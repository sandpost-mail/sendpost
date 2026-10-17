package store_test

import (
	"context"
	"errors"
	"testing"

	"github.com/sandpost-mail/sandpost/internal/store"
	"github.com/sandpost-mail/sandpost/internal/store/sqlcgen"
	"github.com/sandpost-mail/sandpost/internal/testutil"
)

func TestInTx(t *testing.T) {
	pool := testutil.Pool(t)
	ctx := context.Background()
	count := func() int {
		var n int
		if err := pool.QueryRow(ctx, "SELECT count(*) FROM groups").Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}

	err := store.InTx(ctx, pool, func(q *sqlcgen.Queries) error {
		_, err := q.CreateGroup(ctx, "committed")
		return err
	})
	if err != nil || count() != 1 {
		t.Fatalf("commit: %v, groups %d", err, count())
	}

	boom := errors.New("boom")
	err = store.InTx(ctx, pool, func(q *sqlcgen.Queries) error {
		if _, err := q.CreateGroup(ctx, "rolled back"); err != nil {
			return err
		}
		return boom
	})
	if !errors.Is(err, boom) || count() != 1 {
		t.Fatalf("rollback: %v, groups %d", err, count())
	}

	// A failing statement aborts the transaction and the error surfaces.
	err = store.InTx(ctx, pool, func(q *sqlcgen.Queries) error {
		_, err := q.CreateGroup(ctx, "COMMITTED")
		return err
	})
	if !store.IsUniqueViolation(err) {
		t.Errorf("duplicate: %v", err)
	}

	pool.Close()
	if err := store.InTx(ctx, pool, func(*sqlcgen.Queries) error { return nil }); err == nil {
		t.Error("begin on a closed pool must fail")
	}
}

// TestAccessSchemaConstraints pins the integrity rules the services rely on.
func TestAccessSchemaConstraints(t *testing.T) {
	pool := testutil.Pool(t)
	ctx := context.Background()
	uid := testutil.User(t, pool, "u@example.test", false)
	pid := testutil.Project(t, pool, "p")
	gid := testutil.Group(t, pool, "g")

	tests := []struct {
		name string
		sql  string
		args []any
	}{
		{"unknown project role", "INSERT INTO project_groups (project_id, group_id, role) VALUES ($1, $2, 'root')", []any{pid, gid}},
		{"token without owner or project", "INSERT INTO api_tokens (token_hmac, prefix, scopes) VALUES ('\\x01', 'spu_x', ARRAY['read'])", nil},
		{"token with both owner and project", "INSERT INTO api_tokens (owner_user_id, project_id, token_hmac, prefix, scopes) VALUES ($1, $2, '\\x02', 'spu_x', ARRAY['read'])", []any{uid, pid}},
		{"token with an unknown scope", "INSERT INTO api_tokens (owner_user_id, token_hmac, prefix, scopes) VALUES ($1, '\\x03', 'spu_x', ARRAY['root'])", []any{uid}},
		{"token without scopes", "INSERT INTO api_tokens (owner_user_id, token_hmac, prefix, scopes) VALUES ($1, '\\x04', 'spu_x', ARRAY[]::text[])", []any{uid}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := pool.Exec(ctx, tc.sql, tc.args...); err == nil {
				t.Error("the database accepted an invalid row")
			}
		})
	}
	// Deleting a user or project cascades to what belongs to it but keeps the audit trail.
	if _, err := pool.Exec(ctx, "INSERT INTO api_tokens (owner_user_id, token_hmac, prefix, scopes) VALUES ($1, '\\x05', 'spu_x', ARRAY['read'])", uid); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, "INSERT INTO audit_log (actor_user_id, project_id, action) VALUES ($1, $2, 'x')", uid, pid); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, "DELETE FROM users WHERE id = $1", uid); err != nil {
		t.Fatal(err)
	}
	var tokens, audits int
	if err := pool.QueryRow(ctx, "SELECT (SELECT count(*) FROM api_tokens), (SELECT count(*) FROM audit_log WHERE actor_user_id IS NULL)").Scan(&tokens, &audits); err != nil {
		t.Fatal(err)
	}
	if tokens != 0 || audits != 1 {
		t.Errorf("cascade: tokens %d, orphaned audit rows %d", tokens, audits)
	}
}
