package api

import (
	"testing"
	"time"
)

// SetStreamRecheck shortens the SSE access re-check interval for one test.
func SetStreamRecheck(t *testing.T, d time.Duration) {
	t.Helper()
	old := recheckInterval
	recheckInterval = d
	t.Cleanup(func() { recheckInterval = old })
}

// MaxStreamsPerPrincipal is the per principal cap of SSE streams and wait polls.
const MaxStreamsPerPrincipal = maxStreamsPerPrincipal
