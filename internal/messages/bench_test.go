package messages_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/sandpost-mail/sandpost/internal/authz/authztest"
	"github.com/sandpost-mail/sandpost/internal/ingest"
	"github.com/sandpost-mail/sandpost/internal/messages"
	"github.com/sandpost-mail/sandpost/internal/search"
	"github.com/sandpost-mail/sandpost/internal/testutil"
)

// benchMailbox is the size of the seeded mailbox: large enough for the
// planner to prefer the indexes, small enough to seed quickly.
const benchMailbox = 2000

// BenchmarkCompatListPage measures the Mailpit list and search endpoint's
// query (rows plus mailbox and match counts in one statement).
func BenchmarkCompatListPage(b *testing.B) {
	pool := testutil.Pool(b)
	pid := testutil.Project(b, pool, "bench")
	svc := ingest.NewService(pool)
	ctx := context.Background()
	for i := range benchMailbox {
		raw := fmt.Sprintf("From: sender%d@example.test\r\nTo: user%d@example.test\r\nSubject: Order %d shipped\r\n\r\nHello %d\r\n",
			i%17, i%50, i, i)
		if _, err := svc.Ingest(ctx, ingest.Input{ProjectID: pid, EnvelopeTo: []string{"user@example.test"}, Raw: []byte(raw)}); err != nil {
			b.Fatal(err)
		}
	}
	repo := messages.New(pool)
	scope := authztest.Scope(pid)
	tests := []struct{ name, query string }{
		{"all", ""},
		{"from", "from:sender3@example.test"},
		{"subject", `subject:"Order 1"`},
		{"free text", "hello"},
	}
	for _, tc := range tests {
		f, err := search.ParseMailpit(tc.query, time.UTC)
		if err != nil {
			b.Fatal(err)
		}
		b.Run(tc.name, func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				if _, err := repo.ListPage(ctx, scope, f, 0, 50); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
