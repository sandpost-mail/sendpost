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
		c.p = p
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
		if p.UserID != 0 {
			r = r.WithContext(audit.WithActor(r.Context(), p.UserID))
		}
		rt.h(w, r, c)
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

// authenticate resolves the caller from a Bearer token or the session cookie.
// A Bearer header always wins and never falls back to the cookie.
func (s *server) authenticate(w http.ResponseWriter, r *http.Request) (authz.Principal, bool) {
	var (
		p   authz.Principal
		err error
	)
	if h := r.Header.Get("Authorization"); h != "" {
		secret, ok := strings.CutPrefix(h, "Bearer ")
		if !ok {
			unauthorized(w)
			return authz.Principal{}, false
		}
		p, err = s.Tokens.Authenticate(r.Context(), strings.TrimSpace(secret))
		if errors.Is(err, tokens.ErrInvalid) {
			unauthorized(w)
			return authz.Principal{}, false
		}
	} else {
		cookie, cerr := r.Cookie(s.cookieName())
		if cerr != nil {
			unauthorized(w)
			return authz.Principal{}, false
		}
		p, err = s.Auth.Authenticate(r.Context(), cookie.Value)
		if errors.Is(err, auth.ErrNoSession) {
			unauthorized(w)
			return authz.Principal{}, false
		}
	}
	if err != nil {
		s.fail(w, r, err)
		return authz.Principal{}, false
	}
	return p, true
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
