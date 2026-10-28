package api

import (
	"errors"
	"net/http"

	"github.com/sandpost-mail/sandpost/internal/activity"
	"github.com/sandpost-mail/sandpost/internal/audit"
	"github.com/sandpost-mail/sandpost/internal/authz"
	"github.com/sandpost-mail/sandpost/internal/comments"
	"github.com/sandpost-mail/sandpost/internal/credentials"
	"github.com/sandpost-mail/sandpost/internal/delivery"
	"github.com/sandpost-mail/sandpost/internal/groups"
	"github.com/sandpost-mail/sandpost/internal/messages"
	"github.com/sandpost-mail/sandpost/internal/policy"
	"github.com/sandpost-mail/sandpost/internal/projects"
	"github.com/sandpost-mail/sandpost/internal/tokens"
	"github.com/sandpost-mail/sandpost/internal/users"
)

// fail maps domain errors to responses. Every "not found" answer has the same
// body, whether the thing is missing or belongs to a project the caller can
// not see, so ids can not be probed. Unknown errors are logged and answered
// with a generic 500 so internals never reach the client.
func (s *server) fail(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, authz.ErrNotFound), errors.Is(err, authz.ErrBadSignature),
		errors.Is(err, projects.ErrNotFound), errors.Is(err, messages.ErrNotFound),
		errors.Is(err, credentials.ErrNotFound), errors.Is(err, credentials.ErrProjectNotFound),
		errors.Is(err, users.ErrNotFound), errors.Is(err, groups.ErrNotFound), errors.Is(err, tokens.ErrNotFound),
		errors.Is(err, delivery.ErrNotFound):
		writeError(w, http.StatusNotFound, "not found")
	case errors.Is(err, policy.ErrBlocked):
		var be *policy.BlockedError
		if errors.As(err, &be) {
			writeErrorCode(w, http.StatusBadRequest, codeDeliveryBlocked, be.Error())
			return
		}
		writeErrorCode(w, http.StatusBadRequest, codeDeliveryBlocked, "delivery blocked")
	case errors.Is(err, authz.ErrForbidden):
		writeError(w, http.StatusForbidden, "forbidden")
	case errors.Is(err, projects.ErrSlugTaken), errors.Is(err, users.ErrEmailTaken),
		errors.Is(err, groups.ErrNameTaken), errors.Is(err, users.ErrLastAdmin):
		writeError(w, http.StatusConflict, err.Error())
	case errors.Is(err, projects.ErrInvalid), errors.Is(err, credentials.ErrLabelTooLong),
		errors.Is(err, messages.ErrInvalidCursor), errors.Is(err, audit.ErrInvalidCursor),
		errors.Is(err, users.ErrInvalid), errors.Is(err, users.ErrWrongPassword),
		errors.Is(err, groups.ErrInvalid), errors.Is(err, tokens.ErrBadInput), errors.Is(err, delivery.ErrInvalid),
		errors.Is(err, comments.ErrInvalid), errors.Is(err, comments.ErrInvalidCursor), errors.Is(err, activity.ErrInvalidCursor):
		writeError(w, http.StatusBadRequest, err.Error())
	default:
		s.Log.Error("request failed", "method", r.Method, "path", r.URL.Path, "error", err)
		writeError(w, http.StatusInternalServerError, "internal error")
	}
}
