package api

import (
	"encoding/hex"
	"net/http"
	"strconv"
	"sync"

	"github.com/sandpost-mail/sandpost/internal/authz"
)

// maxStreamsPerPrincipal caps the concurrent SSE streams plus wait long polls
// of one token or session. Each holds a connection, a goroutine and a bus
// subscription for minutes.
const maxStreamsPerPrincipal = 10

// streamLimits counts the open long lived requests per principal.
type streamLimits struct {
	mu   sync.Mutex
	max  int
	open map[string]int
}

func newStreamLimits(limit int) *streamLimits {
	return &streamLimits{max: limit, open: map[string]int{}}
}

// acquire takes a slot for key, false when the key is at its cap. The caller
// calls release once when acquire returned true.
func (l *streamLimits) acquire(key string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.open[key] >= l.max {
		return false
	}
	l.open[key]++
	return true
}

func (l *streamLimits) release(key string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.open[key] <= 1 {
		delete(l.open, key)
		return
	}
	l.open[key]--
}

// streamKey identifies the credential of a principal: the token, else the
// session, else the user.
func streamKey(p authz.Principal) string {
	switch {
	case p.TokenID != 0:
		return "token:" + strconv.FormatInt(p.TokenID, 10)
	case len(p.SessionID) > 0:
		return "session:" + hex.EncodeToString(p.SessionID)
	default:
		return "user:" + strconv.FormatInt(p.UserID, 10)
	}
}

// acquireStream takes a long lived request slot for the caller, or answers
// 429. Signed grants carry no principal and never reach the stream routes.
func (s *server) acquireStream(w http.ResponseWriter, c *call) (release func(), ok bool) {
	key := streamKey(c.p)
	if !s.streams.acquire(key) {
		w.Header().Set("Retry-After", "5")
		writeError(w, http.StatusTooManyRequests, "too many concurrent streams")
		return nil, false
	}
	return func() { s.streams.release(key) }, true
}
