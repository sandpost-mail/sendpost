package audit_test

import (
	"context"
	"errors"
	"strconv"
	"testing"

	"github.com/sandpost-mail/sandpost/internal/audit"
	"github.com/sandpost-mail/sandpost/internal/authz"
	"github.com/sandpost-mail/sandpost/internal/store"
	"github.com/sandpost-mail/sandpost/internal/store/sqlcgen"
	"github.com/sandpost-mail/sandpost/internal/testutil"
)

func TestRecordAndRead(t *testing.T) {
	pool := testutil.Pool(t)
	ctx := context.Background()
	uid := testutil.User(t, pool, "actor@example.test", true)
	p1 := testutil.Project(t, pool, "p1")
	p2 := testutil.Project(t, pool, "p2")

	record := func(ctx context.Context, e audit.Entry) {
		t.Helper()
		if err := store.InTx(ctx, pool, func(q *sqlcgen.Queries) error { return audit.Record(ctx, q, e) }); err != nil {
			t.Fatal(err)
		}
	}
	actorCtx := audit.WithActor(ctx, uid)
	record(actorCtx, audit.Entry{Action: "a.one", ProjectID: p1, Target: "t1", Detail: map[string]any{"k": "v"}})
	record(actorCtx, audit.Entry{Action: "a.two", ProjectID: p2})
	record(ctx, audit.Entry{Action: "a.three"}) // a system action: no actor, no project

	r := audit.NewReader(pool)
	all, err := r.ListAll(ctx, "", 0)
	if err != nil || len(all.Items) != 3 {
		t.Fatalf("all: %+v %v", all, err)
	}
	if e := all.Items[0]; e.Action != "a.three" || e.ActorUserID != nil || e.ProjectID != nil || string(e.Detail) != "{}" {
		t.Errorf("newest first, system entry: %+v", e)
	}
	if e := all.Items[2]; e.ActorEmail != "actor@example.test" || e.ProjectID == nil || *e.ProjectID != p1 || e.Target != "t1" {
		t.Errorf("oldest: %+v", e)
	}

	mine, err := r.ListProject(ctx, authz.Trusted(p1), "", 0)
	if err != nil || len(mine.Items) != 1 || mine.Items[0].Action != "a.one" {
		t.Errorf("project log: %+v %v", mine, err)
	}

	first, err := r.ListAll(ctx, "", 1)
	if err != nil || len(first.Items) != 1 || first.NextCursor == "" {
		t.Fatalf("first page: %+v %v", first, err)
	}
	next, err := r.ListAll(ctx, first.NextCursor, 5)
	if err != nil || len(next.Items) != 2 || next.NextCursor != "" || next.Items[0].ID >= first.Items[0].ID {
		t.Errorf("second page: %+v %v", next, err)
	}
	if big, _ := r.ListAll(ctx, "", 100000); len(big.Items) != 3 {
		t.Errorf("an oversized limit must be clamped, not rejected: %+v", big)
	}
	for _, bad := range []string{"abc", "-1", "0", strconv.Itoa(1) + "x"} {
		if _, err := r.ListAll(ctx, bad, 1); !errors.Is(err, audit.ErrInvalidCursor) {
			t.Errorf("cursor %q: %v", bad, err)
		}
	}
}

func TestRecordRollsBackWithTheChange(t *testing.T) {
	pool := testutil.Pool(t)
	ctx := context.Background()
	boom := errors.New("boom")
	err := store.InTx(ctx, pool, func(q *sqlcgen.Queries) error {
		if err := audit.Record(ctx, q, audit.Entry{Action: "never"}); err != nil {
			return err
		}
		return boom
	})
	if !errors.Is(err, boom) {
		t.Fatalf("err = %v", err)
	}
	if page, _ := audit.NewReader(pool).ListAll(ctx, "", 0); len(page.Items) != 0 {
		t.Errorf("an audit row of a rolled back change survived: %+v", page.Items)
	}
}

func TestActor(t *testing.T) {
	if _, ok := audit.Actor(context.Background()); ok {
		t.Error("no actor expected")
	}
	if _, ok := audit.Actor(audit.WithActor(context.Background(), 0)); ok {
		t.Error("actor 0 means none")
	}
	if id, ok := audit.Actor(audit.WithActor(context.Background(), 7)); !ok || id != 7 {
		t.Errorf("actor: %d %v", id, ok)
	}
}

func TestReaderDatabaseFailure(t *testing.T) {
	pool := testutil.Pool(t)
	r := audit.NewReader(pool)
	pool.Close()
	if _, err := r.ListAll(context.Background(), "", 1); err == nil {
		t.Error("expected an error on a closed pool")
	}
}
