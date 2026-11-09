package activity_test

import (
	"context"
	"errors"
	"testing"

	"github.com/sandpost-mail/sandpost/internal/activity"
	"github.com/sandpost-mail/sandpost/internal/authz"
	"github.com/sandpost-mail/sandpost/internal/comments"
	"github.com/sandpost-mail/sandpost/internal/ingest"
	"github.com/sandpost-mail/sandpost/internal/testutil"
)

func TestFeedIsScopedOrderedAndPaged(t *testing.T) {
	pool := testutil.Pool(t)
	ctx := context.Background()
	p1, p2 := testutil.Project(t, pool, "one"), testutil.Project(t, pool, "two")
	raw := []byte("From: a@example.test\r\nTo: b@example.test\r\nSubject: s\r\n\r\nbody")
	ing := ingest.NewService(pool)
	m1, err := ing.Ingest(ctx, ingest.Input{ProjectID: p1, EnvelopeTo: []string{"b@example.test"}, Raw: raw})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ing.Ingest(ctx, ingest.Input{ProjectID: p2, EnvelopeTo: []string{"b@example.test"}, Raw: raw}); err != nil {
		t.Fatal(err)
	}
	if _, err := comments.New(pool).Create(ctx, authz.Trusted(p1), m1, comments.Author{}, "hello"); err != nil {
		t.Fatal(err)
	}
	r := activity.NewReader(pool)
	page, err := r.List(ctx, authz.Trusted(p1), "", 0)
	if err != nil || len(page.Items) != 2 || page.NextCursor != "" {
		t.Fatalf("page %+v %v", page, err)
	}
	if page.Items[0].Kind != activity.KindComment && page.Items[0].Kind != activity.KindMessage {
		t.Errorf("kinds %+v", page.Items)
	}
	first, err := r.List(ctx, authz.Trusted(p1), "", 1)
	if err != nil || len(first.Items) != 1 || first.NextCursor == "" {
		t.Fatalf("first %+v %v", first, err)
	}
	second, err := r.List(ctx, authz.Trusted(p1), first.NextCursor, 1)
	if err != nil || len(second.Items) != 1 || second.Items[0] == first.Items[0] {
		t.Fatalf("second %+v %v", second, err)
	}
	if _, err := r.List(ctx, authz.Trusted(p1), "%%%", 1); !errors.Is(err, activity.ErrInvalidCursor) {
		t.Errorf("bad base64: %v", err)
	}
	for _, c := range []string{"MXx8", "YXxtZXNzYWdlfDE", "MXxub3BlfDE", "MXxtZXNzYWdlfDA"} {
		if _, err := r.List(ctx, authz.Trusted(p1), c, 1); !errors.Is(err, activity.ErrInvalidCursor) {
			t.Errorf("cursor %q: %v", c, err)
		}
	}
	if _, err := r.List(ctx, authz.Trusted(p1), "", 5000); err != nil {
		t.Errorf("limit clamp: %v", err)
	}
}
