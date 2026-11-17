package ingest_test

import (
	"context"
	"testing"

	"github.com/sandpost-mail/sandpost/internal/ingest"
	"github.com/sandpost-mail/sandpost/internal/testutil"
)

func BenchmarkIngest(b *testing.B) {
	pool := testutil.Pool(b)
	pid := testutil.Project(b, pool, "bench")
	svc := ingest.NewService(pool)
	raw := ingest.RealisticMessage()
	in := ingest.Input{ProjectID: pid, EnvelopeFrom: "bounce@example.test", EnvelopeTo: []string{"alice@example.test"}, Raw: raw}
	ctx := context.Background()
	b.SetBytes(int64(len(raw)))
	b.ReportAllocs()
	for b.Loop() {
		if _, err := svc.Ingest(ctx, in); err != nil {
			b.Fatal(err)
		}
	}
}
