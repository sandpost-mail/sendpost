package messages_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/sandpost-mail/sandpost/internal/authz"
	"github.com/sandpost-mail/sandpost/internal/messages"
	"github.com/sandpost-mail/sandpost/internal/search"
	"github.com/sandpost-mail/sandpost/internal/testutil"
)

func ids(items []messages.Summary) []int64 {
	out := make([]int64, 0, len(items))
	for _, it := range items {
		out = append(out, it.ID)
	}
	return out
}

func equalIDs(a, b []int64) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestMatchRunFilterAndOrder(t *testing.T) {
	e := newEnv(t)
	other := testutil.Project(t, e.pool, "beta")
	now := time.Now().UTC()
	a := e.add(t, e.pid, now, "a@x.test", "u@x.test", "welcome", "hi", "X-Run-Id: run-1")
	b := e.add(t, e.pid, now.Add(time.Second), "a@x.test", "u@x.test", "reset", "hi", "x-run-id: run-2")
	c := e.add(t, e.pid, now.Add(2*time.Second), "a@x.test", "u@x.test", "welcome", "hi", "X-Run-Id: run-1")
	foreign := e.add(t, other, now, "a@x.test", "u@x.test", "welcome", "hi", "X-Run-Id: run-1")
	scope := authz.Trusted(e.pid)
	ctx := context.Background()

	got, err := e.repo.Match(ctx, scope, messages.MatchQuery{})
	if err != nil || !equalIDs(ids(got), []int64{a, b, c}) {
		t.Fatalf("all, oldest first by id: %v %v (foreign %d must be absent)", ids(got), err, foreign)
	}
	got, err = e.repo.Match(ctx, scope, messages.MatchQuery{Run: "run-1"})
	if err != nil || !equalIDs(ids(got), []int64{a, c}) {
		t.Fatalf("run-1: %v %v", ids(got), err)
	}
	got, _ = e.repo.Match(ctx, scope, messages.MatchQuery{Run: "run-2"}) // header name is case insensitive
	if !equalIDs(ids(got), []int64{b}) {
		t.Fatalf("run-2: %v", ids(got))
	}
	got, _ = e.repo.Match(ctx, scope, messages.MatchQuery{Run: "run-1", AfterID: a})
	if !equalIDs(ids(got), []int64{c}) {
		t.Fatalf("after_id: %v", ids(got))
	}
	q, _ := search.Parse("subject:welcome")
	got, _ = e.repo.Match(ctx, scope, messages.MatchQuery{Query: q, Run: "run-1", Limit: 1})
	if !equalIDs(ids(got), []int64{a}) {
		t.Fatalf("query + limit: %v", ids(got))
	}
	got, _ = e.repo.Match(ctx, authz.Trusted(other), messages.MatchQuery{Run: "run-1"})
	if !equalIDs(ids(got), []int64{foreign}) {
		t.Fatalf("other project sees only its own: %v", ids(got))
	}
	got, _ = e.repo.Match(ctx, scope, messages.MatchQuery{Run: "run"}) // no prefix matching
	if len(got) != 0 {
		t.Fatalf("run id matches exactly: %v", ids(got))
	}
}

func TestMatchRunFilterAcceptsTag(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	if _, err := e.pool.Exec(ctx, `INSERT INTO tag_rules (project_id, kind, header_name, pattern, tag)
		VALUES ($1, 'header', 'X-Build', '', '{value}')`, e.pid); err != nil {
		t.Fatal(err)
	}
	id := e.add(t, e.pid, time.Now(), "a@x.test", "u@x.test", "s", "b", "X-Build: b77")
	e.add(t, e.pid, time.Now(), "a@x.test", "u@x.test", "s", "b", "X-Build: b78")
	got, err := e.repo.Match(ctx, authz.Trusted(e.pid), messages.MatchQuery{Run: "b77"})
	if err != nil || !equalIDs(ids(got), []int64{id}) {
		t.Fatalf("tag as run: %v %v", ids(got), err)
	}
	if len(got[0].Tags) != 1 || got[0].Tags[0] != "b77" {
		t.Fatalf("tags: %v", got[0].Tags)
	}
	// The README convention: a header rule producing run:{value}.
	if _, err := e.pool.Exec(ctx, `INSERT INTO tag_rules (project_id, kind, header_name, pattern, tag)
		VALUES ($1, 'header', 'X-Job', '', 'run:{value}')`, e.pid); err != nil {
		t.Fatal(err)
	}
	job := e.add(t, e.pid, time.Now(), "a@x.test", "u@x.test", "s", "b", "X-Job: j5")
	got, _ = e.repo.Match(ctx, authz.Trusted(e.pid), messages.MatchQuery{Run: "j5"})
	if !equalIDs(ids(got), []int64{job}) {
		t.Fatalf("run:<id> tag: %v", ids(got))
	}
}

func TestValidateRunID(t *testing.T) {
	for _, ok := range []string{"", "run-1", "ci/42 #3", strings.Repeat("x", messages.MaxRunIDLength)} {
		if err := messages.ValidateRunID(ok); err != nil {
			t.Errorf("%q: %v", ok, err)
		}
	}
	for _, bad := range []string{" x", "x ", "a\nb", "a\x00b", "\xff", strings.Repeat("x", messages.MaxRunIDLength+1)} {
		if err := messages.ValidateRunID(bad); !errors.Is(err, messages.ErrInvalidRun) {
			t.Errorf("%q: %v", bad, err)
		}
	}
	e := newEnv(t)
	if _, err := e.repo.Match(context.Background(), authz.Trusted(e.pid), messages.MatchQuery{Run: "a\nb"}); !errors.Is(err, messages.ErrInvalidRun) {
		t.Errorf("Match must validate: %v", err)
	}
}
