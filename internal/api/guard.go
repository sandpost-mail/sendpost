package api

import (
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/sandpost-mail/sandpost/internal/audit"
	"github.com/sandpost-mail/sandpost/internal/auth"
	"github.com/sandpost-mail/sandpost/internal/authz"
	"github.com/sandpost-mail/sandpost/internal/tokens"
)

const (
	csrfHeader = "X-CSRF-Token"
	// grantParam carries a signed preview grant as "<expiry>.<signature>". It
	// is one parameter without ampersands so it can sit in HTML and CSS urls.
	grantParam = "grant"
)

// guard enforces the access declared for a route, then runs its handler.
func (s *server) guard(rt route) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c := &call{}
		if rt.access == accessPublic {
			rt.h(w, r, c)
			return
		}
		if rt.access == accessSigned && r.URL.Query().Get(grantParam) != "" {
			if !s.verifyGrant(w, r, rt, c) {
				return
			}
			rt.h(w, r, c)
			return
		}
		p, ok := s.authenticate(w, r)
		if !ok {
			return
		}
		c.p, c.cap = p, rt.cap
		if p.UsesCookie() && !isSafeMethod(r.Method) && !s.Auth.CheckCSRF(p, r.Header.Get(csrfHeader)) {
			writeErrorCode(w, http.StatusForbidden, codeCSRF, "missing or invalid CSRF token")
			return
		}
		if p.MustChangePassword && !rt.allowPasswordChange {
			writeErrorCode(w, http.StatusForbidden, codePasswordChange, "password change required")
			return
		}
		if !s.enforce(w, r, rt, c) {
			return
		}
		rt.h(w, admit(r, p), c)
	})
}

// enforce applies the route's access kind and fills in c.scope.
func (s *server) enforce(w http.ResponseWriter, r *http.Request, rt route, c *call) bool {
	switch rt.access {
	case accessAny, accessPublic:
		return true
	case accessUser:
		if !c.p.IsUser() {
			s.fail(w, r, authz.ErrForbidden)
			return false
		}
		return true
	case accessGlobal:
		if err := authz.Global(c.p, rt.cap); err != nil {
			s.fail(w, r, err)
			return false
		}
		return true
	case accessProject, accessSigned:
		pid, ok := pathID(w, r, "pid")
		if !ok {
			return false
		}
		scope, err := s.Authz.Project(r.Context(), c.p, pid, rt.cap)
		if err != nil {
			s.fail(w, r, err)
			return false
		}
		c.scope = scope
		return true
	}
	s.fail(w, r, authz.ErrForbidden) // unknown access kind fails closed
	return false
}

// errUnauthenticated means the request carries no usable credentials.
var errUnauthenticated = errors.New("authentication required")

// authenticate resolves the caller from a Bearer token or the session cookie
// and answers the request when that fails.
func (s *server) authenticate(w http.ResponseWriter, r *http.Request) (authz.Principal, bool) {
	p, err := s.principal(r)
	switch {
	case errors.Is(err, errUnauthenticated):
		unauthorized(w)
		return authz.Principal{}, false
	case err != nil:
		s.fail(w, r, err)
		return authz.Principal{}, false
	}
	return p, true
}

// principal resolves the caller from a Bearer token or the session cookie. A
// Bearer header always wins and never falls back to the cookie. It writes
// nothing, so long lived streams can call it again to re-check access.
func (s *server) principal(r *http.Request) (authz.Principal, error) {
	if h := r.Header.Get("Authorization"); h != "" {
		secret, ok := strings.CutPrefix(h, "Bearer ")
		if !ok {
			return authz.Principal{}, errUnauthenticated
		}
		p, err := s.Tokens.Authenticate(r.Context(), strings.TrimSpace(secret))
		if errors.Is(err, tokens.ErrInvalid) {
			return authz.Principal{}, errUnauthenticated
		}
		return p, err
	}
	cookie, err := r.Cookie(s.cookieName())
	if err != nil {
		return authz.Principal{}, errUnauthenticated
	}
	p, err := s.Auth.Authenticate(r.Context(), cookie.Value)
	if errors.Is(err, auth.ErrNoSession) {
		return authz.Principal{}, errUnauthenticated
	}
	return p, err
}

// admit attaches the authenticated principal to the request context for the
// layers below (the audit actor). Both API surfaces call it after their
// access checks, so a write is attributed the same way on either.
func admit(r *http.Request, p authz.Principal) *http.Request {
	ctx := r.Context()
	if p.UserID != 0 {
		ctx = audit.WithActor(ctx, p.UserID)
	}
	if p.TokenID != 0 {
		ctx = audit.WithToken(ctx, p.TokenID)
	}
	if ctx == r.Context() {
		return r
	}
	return r.WithContext(ctx)
}

// verifyGrant checks a signed preview grant against the exact project,
// message and resource of the request.
func (s *server) verifyGrant(w http.ResponseWriter, r *http.Request, rt route, c *call) bool {
	pid, ok := pathID(w, r, "pid")
	if !ok {
		return false
	}
	mid, ok := pathID(w, r, "mid")
	if !ok {
		return false
	}
	expStr, sig, _ := strings.Cut(r.URL.Query().Get(grantParam), ".")
	exp, err := strconv.ParseInt(expStr, 10, 64)
	if err != nil {
		s.fail(w, r, authz.ErrBadSignature)
		return false
	}
	scope, err := s.Signer.Verify(pid, mid, rt.resource(r), exp, sig)
	if err != nil {
		s.fail(w, r, err)
		return false
	}
	c.scope = scope
	return true
}

func unauthorized(w http.ResponseWriter) {
	writeErrorCode(w, http.StatusUnauthorized, codeUnauthorized, "authentication required")
}

func isSafeMethod(m string) bool {
	return m == http.MethodGet || m == http.MethodHead || m == http.MethodOptions
}
