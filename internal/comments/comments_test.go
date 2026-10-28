package comments_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/sandpost-mail/sandpost/internal/authz"
	"github.com/sandpost-mail/sandpost/internal/comments"
	"github.com/sandpost-mail/sandpost/internal/ingest"
	"github.com/sandpost-mail/sandpost/internal/messages"
	"github.com/sandpost-mail/sandpost/internal/testutil"
)

func TestNormalize(t *testing.T) {
	tests := []struct {
		name, in, want string
		bad            bool
	}{
		{name: "trims", in: "  hi \n", want: "hi"},
		{name: "keeps markup", in: "<b>&amp;</b>", want: "<b>&amp;</b>"},
		{name: "keeps newlines and tabs", in: "a\r\n\tb", want: "a\r\n\tb"},
		{name: "empty", bad: true},
		{name: "blank", in: " \n ", bad: true},
		{name: "nul", in: "a\x00b", bad: true},
		{name: "bell", in: "a\x07", bad: true},
		{name: "invalid utf8", in: "a\xffb", bad: true},
		{name: "limit", in: strings.Repeat("é", comments.MaxBodyRunes), want: strings.Repeat("é", comments.MaxBodyRunes)},
		{name: "over limit", in: strings.Repeat("é", comments.MaxBodyRunes+1), bad: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := comments.Normalize(tt.in)
			if tt.bad {
				if !errors.Is(err, comments.ErrInvalid) {
					t.Fatalf("err = %v", err)
				}
				return
			}
			if err != nil || got != tt.want {
				t.Errorf("got %q, %v", got, err)
			}
		})
	}
}

func TestServiceScopesByProject(t *testing.T) {
	pool := testutil.Pool(t)
	ctx := context.Background()
	p1, p2 := testutil.Project(t, pool, "one"), testutil.Project(t, pool, "two")
	raw := []byte("From: a@example.test\r\nTo: b@example.test\r\nSubject: s\r\n\r\nbody")
	ing := ingest.NewService(pool)
	m1, err := ing.Ingest(ctx, ingest.Input{ProjectID: p1, EnvelopeTo: []string{"b@example.test"}, Raw: raw})
	if err != nil {
		t.Fatal(err)
	}
	svc := comments.New(pool)
	uid := testutil.User(t, pool, "u@example.test", false)
	c, err := svc.Create(ctx, authz.Trusted(p1), m1, comments.Author{UserID: uid}, "hi")
	if err != nil || c.Author != "u@example.test" {
		t.Fatalf("create: %+v %v", c, err)
	}
	// Unknown author: still stored, with a fixed label.
	if c, err = svc.Create(ctx, authz.Trusted(p1), m1, comments.Author{}, "x"); err != nil || c.Author != "unknown" {
		t.Fatalf("anonymous: %+v %v", c, err)
	}
	if _, err := svc.Create(ctx, authz.Trusted(p2), m1, comments.Author{UserID: uid}, "x"); !errors.Is(err, messages.ErrNotFound) {
		t.Errorf("create in foreign project: %v", err)
	}
	if _, err := svc.List(ctx, authz.Trusted(p2), m1, "", 0); !errors.Is(err, messages.ErrNotFound) {
		t.Errorf("list in foreign project: %v", err)
	}
	// Deleting the user keeps the comment and its author label.
	if _, err := pool.Exec(ctx, "DELETE FROM users WHERE id=$1", uid); err != nil {
		t.Fatal(err)
	}
	page, err := svc.List(ctx, authz.Trusted(p1), m1, "", 500)
	if err != nil || len(page.Items) != 2 || page.Items[0].UserID != nil || page.Items[0].Author != "u@example.test" {
		t.Errorf("after user deletion: %+v %v", page, err)
	}
	if _, err := svc.Create(ctx, authz.Trusted(p1), m1, comments.Author{}, ""); !errors.Is(err, comments.ErrInvalid) {
		t.Errorf("empty: %v", err)
	}
}

func TestListPagingAndCursor(t *testing.T) {
	pool := testutil.Pool(t)
	ctx := context.Background()
	p := testutil.Project(t, pool, "one")
	raw := []byte("From: a@example.test\r\nTo: b@example.test\r\nSubject: s\r\n\r\nbody")
	m, err := ingest.NewService(pool).Ingest(ctx, ingest.Input{ProjectID: p, EnvelopeTo: []string{"b@example.test"}, Raw: raw})
	if err != nil {
		t.Fatal(err)
	}
	svc := comments.New(pool)
	for _, b := range []string{"a", "b", "c"} {
		if _, err := svc.Create(ctx, authz.Trusted(p), m, comments.Author{}, b); err != nil {
			t.Fatal(err)
		}
	}
	page, err := svc.List(ctx, authz.Trusted(p), m, "", 2)
	if err != nil || len(page.Items) != 2 || page.NextCursor == "" {
		t.Fatalf("page 1: %+v %v", page, err)
	}
	page, err = svc.List(ctx, authz.Trusted(p), m, page.NextCursor, 2)
	if err != nil || len(page.Items) != 1 || page.Items[0].Body != "c" || page.NextCursor != "" {
		t.Fatalf("page 2: %+v %v", page, err)
	}
	for _, c := range []string{"x", "0", "-3"} {
		if _, err := svc.List(ctx, authz.Trusted(p), m, c, 0); !errors.Is(err, comments.ErrInvalidCursor) {
			t.Errorf("cursor %q: %v", c, err)
		}
	}
	if _, err := svc.List(ctx, authz.Trusted(p), m, "", 100000); err != nil {
		t.Errorf("limit clamp: %v", err)
	}
	if _, err := svc.Create(ctx, authz.Trusted(p), 999999, comments.Author{}, "x"); !errors.Is(err, messages.ErrNotFound) {
		t.Errorf("unknown message: %v", err)
	}
}
