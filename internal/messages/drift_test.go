package messages_test

import (
	"context"
	"slices"
	"testing"
	"time"

	"github.com/sandpost-mail/sandpost/internal/authz/authztest"
	"github.com/sandpost-mail/sandpost/internal/messages"
	"github.com/sandpost-mail/sandpost/internal/search"
)

// TestListAndMatchFiltersAgree runs the same native queries through
// ListMessages and MatchMessages, whose filter SQL is duplicated (sqlc can not
// share a WHERE block), so the two copies can not drift apart unnoticed.
func TestListAndMatchFiltersAgree(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	if _, err := e.pool.Exec(ctx, `INSERT INTO tag_rules (project_id, kind, header_name, pattern, tag)
		VALUES ($1, 'header', 'X-Build', '', '{value}')`, e.pid); err != nil {
		t.Fatal(err)
	}
	d := func(day int) time.Time { return time.Date(2026, 10, day, 12, 0, 0, 0, time.UTC) }
	e.add(t, e.pid, d(10), "Alice <alice@example.test>", "bob@example.test", "Welcome aboard", "Your invoice number is 4242")
	e.add(t, e.pid, d(11), "billing@shop.test", "carol@example.test", "Order shipped", "Tracking parcel arrives", "X-Build: Nightly-7")
	e.add(t, e.pid, d(12), "alice@example.test", "dave@corp.test", "100% off_sale", "Unrelated text")
	e.add(t, e.pid, d(13), "ops@example.test", "t@example.test", "Tagged mail", "x", "X-Build: nightly-8")

	queries := []string{
		"", "from:alice", "to:corp", "subject:ship", "subject:100%", "body:invoice", "parcel", "aboa",
		`"parcel arrives"`, "tag:nightly-7", "tag:NIGHTLY-8", "tag:nope", "date:2026-10-11",
		"after:2026-10-12", "before:2026-10-11", "from:alice after:2026-10-11", "from:nobody",
	}
	scope := authztest.Scope(e.pid)
	for _, q := range queries {
		t.Run(q, func(t *testing.T) {
			parsed, err := search.Parse(q)
			if err != nil {
				t.Fatal(err)
			}
			page, err := e.repo.List(ctx, scope, parsed, "", messages.MaxLimit)
			if err != nil {
				t.Fatal(err)
			}
			matched, err := e.repo.Match(ctx, scope, messages.MatchQuery{Query: parsed, Limit: messages.MaxLimit})
			if err != nil {
				t.Fatal(err)
			}
			listed := ids(page.Items)
			slices.Sort(listed)
			if got := ids(matched); !equalIDs(listed, got) {
				t.Errorf("List %v, Match %v", listed, got)
			}
		})
	}
}

// Tags are case-insensitive since migration 00006: the first spelling is
// kept, so filters must not depend on the spelling of the query.
func TestTagFiltersIgnoreCase(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	if _, err := e.pool.Exec(ctx, `INSERT INTO tag_rules (project_id, kind, header_name, pattern, tag)
		VALUES ($1, 'header', 'X-Build', '', '{value}')`, e.pid); err != nil {
		t.Fatal(err)
	}
	first := e.add(t, e.pid, time.Now(), "a@x.test", "u@x.test", "s", "b", "X-Build: Run-42")
	second := e.add(t, e.pid, time.Now(), "a@x.test", "u@x.test", "s", "b", "X-Build: run-42")
	scope := authztest.Scope(e.pid)
	for _, spelling := range []string{"run-42", "RUN-42", "Run-42"} {
		got, err := e.repo.Match(ctx, scope, messages.MatchQuery{Run: spelling})
		if err != nil || !equalIDs(ids(got), []int64{first, second}) {
			t.Errorf("run %q: %v %v", spelling, ids(got), err)
		}
		if subjects := list(t, e, e.pid, "tag:"+spelling); len(subjects) != 2 {
			t.Errorf("tag:%s lists %v", spelling, subjects)
		}
	}
}

func TestMatchNewestFirst(t *testing.T) {
	e := newEnv(t)
	now := time.Now()
	var all []int64
	for range 5 {
		all = append(all, e.add(t, e.pid, now, "a@x.test", "u@x.test", "s", "b"))
	}
	got, err := e.repo.Match(context.Background(), authztest.Scope(e.pid), messages.MatchQuery{NewestFirst: true, Limit: 2})
	if err != nil || !equalIDs(ids(got), []int64{all[4], all[3]}) {
		t.Fatalf("newest first: %v %v (all %v)", ids(got), err, all)
	}
}
