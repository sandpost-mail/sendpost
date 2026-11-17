package api

import (
	"errors"
	"math"
	"net/http"
	"strconv"

	"github.com/sandpost-mail/sandpost/internal/auth"
	"github.com/sandpost-mail/sandpost/internal/authz"
	"github.com/sandpost-mail/sandpost/internal/comments"
	"github.com/sandpost-mail/sandpost/internal/credentials"
	"github.com/sandpost-mail/sandpost/internal/delivery"
	"github.com/sandpost-mail/sandpost/internal/extract"
	"github.com/sandpost-mail/sandpost/internal/groups"
	"github.com/sandpost-mail/sandpost/internal/ingest"
	"github.com/sandpost-mail/sandpost/internal/messages"
	pagination "github.com/sandpost-mail/sandpost/internal/page"
	"github.com/sandpost-mail/sandpost/internal/policy"
	"github.com/sandpost-mail/sandpost/internal/projects"
	"github.com/sandpost-mail/sandpost/internal/search"
	"github.com/sandpost-mail/sandpost/internal/tokens"
	"github.com/sandpost-mail/sandpost/internal/users"
)

// errorKind is the HTTP meaning of a domain error, shared by both API
// surfaces. Each surface renders it in its own body format.
type errorKind int

const (
	kindInternal errorKind = iota
	kindUnauthenticated
	kindNotFound
	kindForbidden
	kindBlocked
	kindConflict
	kindInvalid
	kindRateLimited
)

// status is the HTTP status of a kind.
func (k errorKind) status() int {
	switch k {
	case kindUnauthenticated:
		return http.StatusUnauthorized
	case kindNotFound:
		return http.StatusNotFound
	case kindForbidden:
		return http.StatusForbidden
	case kindBlocked, kindInvalid:
		return http.StatusBadRequest
	case kindConflict:
		return http.StatusConflict
	case kindRateLimited:
		return http.StatusTooManyRequests
	case kindInternal:
	}
	return http.StatusInternalServerError
}

// errorKinds is the single table from domain sentinels to HTTP meaning. Every
// "not found" (missing, or in a project the caller can not see) is one kind so
// that both surfaces answer it with an identical body and ids can not be
// probed. A sentinel missing here is answered 500, TestEveryDomainErrorIsMapped
// fails when a package adds one without an entry.
var errorKinds = []struct {
	err  error
	kind errorKind
}{
	{authz.ErrNotFound, kindNotFound},
	{authz.ErrBadSignature, kindNotFound},
	{projects.ErrNotFound, kindNotFound},
	{messages.ErrNotFound, kindNotFound},
	{messages.ErrTagNotFound, kindNotFound},
	{credentials.ErrNotFound, kindNotFound},
	{credentials.ErrProjectNotFound, kindNotFound},
	{ingest.ErrProjectNotFound, kindNotFound},
	{users.ErrNotFound, kindNotFound},
	{groups.ErrNotFound, kindNotFound},
	{tokens.ErrNotFound, kindNotFound},
	{delivery.ErrNotFound, kindNotFound},

	{tokens.ErrInvalid, kindUnauthenticated},
	{auth.ErrNoSession, kindUnauthenticated},
	{auth.ErrInvalidCredentials, kindUnauthenticated},
	{credentials.ErrInvalid, kindUnauthenticated},

	{authz.ErrForbidden, kindForbidden},

	{policy.ErrBlocked, kindBlocked},

	{projects.ErrSlugTaken, kindConflict},
	{users.ErrEmailTaken, kindConflict},
	{groups.ErrNameTaken, kindConflict},
	{users.ErrLastAdmin, kindConflict},

	{projects.ErrInvalid, kindInvalid},
	{credentials.ErrLabelTooLong, kindInvalid},
	{pagination.ErrInvalidCursor, kindInvalid}, // shared by every keyset and id cursor
	{messages.ErrInvalidRun, kindInvalid},
	{messages.ErrEmptyFilter, kindInvalid},
	{messages.ErrTagExists, kindInvalid},
	{messages.ErrInvalidTag, kindInvalid},
	{users.ErrInvalid, kindInvalid},
	{users.ErrWrongPassword, kindInvalid},
	{groups.ErrInvalid, kindInvalid},
	{tokens.ErrBadInput, kindInvalid},
	{delivery.ErrInvalid, kindInvalid},
	{policy.ErrAddress, kindInvalid},
	{policy.ErrAllowlist, kindInvalid},
	{comments.ErrInvalid, kindInvalid},
	{search.ErrInvalidDate, kindInvalid},
	{search.ErrQueryTooLong, kindInvalid},
	{search.ErrTooManyTerms, kindInvalid},
	{search.ErrSizeTooLarge, kindInvalid},
	{extract.ErrBadPattern, kindInvalid},
	{delivery.ErrRateLimited, kindRateLimited},
}

// classify returns the kind of err, kindInternal when it is not a known
// domain error.
func classify(err error) errorKind {
	for _, e := range errorKinds {
		if errors.Is(err, e.err) {
			return e.kind
		}
	}
	return kindInternal
}

// fail maps domain errors to native responses. Unknown errors are logged and
// answered with a generic 500 so internals never reach the client.
func (s *server) fail(w http.ResponseWriter, r *http.Request, err error) {
	kind := classify(err)
	switch kind {
	case kindNotFound:
		writeError(w, kind.status(), "not found")
	case kindUnauthenticated:
		unauthorized(w)
	case kindForbidden:
		writeError(w, kind.status(), "forbidden")
	case kindBlocked:
		var be *policy.BlockedError
		if errors.As(err, &be) {
			writeErrorCode(w, kind.status(), codeDeliveryBlocked, be.Error())
			return
		}
		writeErrorCode(w, kind.status(), codeDeliveryBlocked, "delivery blocked")
	case kindConflict, kindInvalid:
		writeError(w, kind.status(), err.Error())
	case kindRateLimited:
		setRetryAfter(w, err)
		writeError(w, kind.status(), err.Error())
	case kindInternal:
		s.Log.Error("request failed", "method", r.Method, "path", r.URL.Path, "error", err)
		writeError(w, kind.status(), "internal error")
	}
}

// compatFail maps domain errors the way Mailpit answers: plain text, the same
// 404 for anything missing or not visible, the message for input problems.
func (s *server) compatFail(w http.ResponseWriter, r *http.Request, err error) {
	kind := classify(err)
	switch kind {
	case kindNotFound:
		compatNotFound(w)
	case kindUnauthenticated:
		compatText(w, kind.status(), "authentication required")
	case kindForbidden:
		compatText(w, kind.status(), "forbidden")
	case kindBlocked, kindConflict, kindInvalid:
		compatText(w, kind.status(), err.Error())
	case kindRateLimited:
		setRetryAfter(w, err)
		compatText(w, kind.status(), err.Error())
	case kindInternal:
		s.Log.Error("compat request failed", "method", r.Method, "path", r.URL.Path, "error", err)
		compatText(w, kind.status(), "internal error")
	}
}

// setRetryAfter sets Retry-After (whole seconds, at least 1) when err carries
// a wait.
func setRetryAfter(w http.ResponseWriter, err error) {
	var rl *delivery.RateLimitError
	if !errors.As(err, &rl) {
		return
	}
	secs := int(math.Ceil(rl.RetryAfter.Seconds()))
	w.Header().Set("Retry-After", strconv.Itoa(max(secs, 1)))
}
