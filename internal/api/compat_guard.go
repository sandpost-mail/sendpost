package api

import (
	"errors"
	"net/http"
	"strings"

	"github.com/sandpost-mail/sandpost/internal/authz"
	"github.com/sandpost-mail/sandpost/internal/messages"
	"github.com/sandpost-mail/sandpost/internal/policy"
	"github.com/sandpost-mail/sandpost/internal/projects"
	"github.com/sandpost-mail/sandpost/internal/search"
	"github.com/sandpost-mail/sandpost/internal/tokens"
)

// compatMaxBody bounds the JSON body of the compat write endpoints (id lists).
const compatMaxBody = 1 << 20

// compatCall is what a compat handler receives after the guard let the
// request through.
type compatCall struct {
	scope authz.Scope
	// base is the path prefix of this API for the request, "/api/v1" or
	// "/p/{slug}/api/v1", for the links the handlers build.
	base string
	// root is the prefix of the /view pages, "" or "/p/{slug}".
	root string
}

type compatHandler func(w http.ResponseWriter, r *http.Request, c *compatCall)

// compatGuard authenticates a compat request and resolves its project.
//
// Credentials are an API token as Bearer, or as the password of HTTP Basic
// auth with any username (the form Mailpit clients already send). The session
// cookie is never looked at, so these routes need no CSRF protection and a
// browser can not be made to act through them. A project token names its
// project. A user token must use the /p/{slug} form, and then its role on
// that project decides. A project the caller has no role on answers exactly
// like one that does not exist.
func (s *server) compatGuard(rt compatRoute, slugForm bool) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p, ok := s.compatAuthenticate(w, r)
		if !ok {
			return
		}
		var (
			scope authz.Scope
			err   error
			c     = &compatCall{base: "/api/v1"}
		)
		switch {
		case slugForm:
			slug := r.PathValue("slug")
			c.base, c.root = "/p/"+slug+"/api/v1", "/p/"+slug
			scope, err = s.Authz.ProjectBySlug(r.Context(), p, slug, rt.cap)
		case p.Kind == authz.KindProjectToken:
			scope, err = s.Authz.Project(r.Context(), p, p.ProjectID, rt.cap)
		default:
			compatText(w, http.StatusForbidden, "a user token must use /p/{project-slug}/api/v1")
			return
		}
		if err != nil {
			s.compatFail(w, r, err)
			return
		}
		c.scope = scope
		r.Body = http.MaxBytesReader(w, r.Body, compatMaxBody)
		rt.h(w, r, c)
	})
}

// compatAuthenticate resolves the principal from the Authorization header.
func (s *server) compatAuthenticate(w http.ResponseWriter, r *http.Request) (authz.Principal, bool) {
	secret, present := compatSecret(r)
	if secret == "" {
		if !present {
			// The challenge lets clients that wait for one send their
			// credentials, and a browser ask for them.
			w.Header().Set("WWW-Authenticate", `Basic realm="Sandpost", charset="UTF-8"`)
		}
		compatText(w, http.StatusUnauthorized, "authentication required")
		return authz.Principal{}, false
	}
	p, err := s.Tokens.Authenticate(r.Context(), secret)
	if errors.Is(err, tokens.ErrInvalid) {
		compatText(w, http.StatusUnauthorized, "authentication required")
		return authz.Principal{}, false
	}
	if err != nil {
		s.compatFail(w, r, err)
		return authz.Principal{}, false
	}
	if p.MustChangePassword {
		compatText(w, http.StatusForbidden, "password change required")
		return authz.Principal{}, false
	}
	return p, true
}

// compatSecret extracts the token from a Bearer or Basic Authorization
// header. present says whether the header exists at all.
func compatSecret(r *http.Request) (secret string, present bool) {
	h := r.Header.Get("Authorization")
	if h == "" {
		return "", false
	}
	if tok, ok := strings.CutPrefix(h, "Bearer "); ok {
		return strings.TrimSpace(tok), true
	}
	if _, pass, ok := r.BasicAuth(); ok {
		return pass, true
	}
	return "", true
}

// compatText writes a plain text answer, the format of Mailpit's errors.
func compatText(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_, _ = w.Write([]byte(msg))
}

func compatNotFound(w http.ResponseWriter) { compatText(w, http.StatusNotFound, "404 page not found") }

func compatOK(w http.ResponseWriter) { compatText(w, http.StatusOK, "ok") }

// compatFail maps domain errors the way Mailpit answers. Anything missing or
// not visible is the same 404, input problems are 400 with the message, and
// unexpected errors are logged and answered with a generic 500.
func (s *server) compatFail(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, authz.ErrNotFound), errors.Is(err, messages.ErrNotFound), errors.Is(err, projects.ErrNotFound),
		errors.Is(err, messages.ErrTagNotFound):
		compatNotFound(w)
	case errors.Is(err, authz.ErrForbidden):
		compatText(w, http.StatusForbidden, "forbidden")
	case errors.Is(err, policy.ErrBlocked):
		compatText(w, http.StatusBadRequest, err.Error())
	case errors.Is(err, search.ErrInvalidDate), errors.Is(err, search.ErrQueryTooLong),
		errors.Is(err, messages.ErrEmptyFilter), errors.Is(err, search.ErrTooManyTerms),
		errors.Is(err, search.ErrSizeTooLarge), errors.Is(err, messages.ErrTagExists), errors.Is(err, messages.ErrInvalidTag):
		compatText(w, http.StatusBadRequest, err.Error())
	default:
		s.Log.Error("compat request failed", "method", r.Method, "path", r.URL.Path, "error", err)
		compatText(w, http.StatusInternalServerError, "internal error")
	}
}
