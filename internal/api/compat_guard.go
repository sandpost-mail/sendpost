package api

import (
	"bytes"
	"errors"
	"net/http"
	"strings"

	"github.com/sandpost-mail/sandpost/internal/authz"
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
	// body is the request body of a write, read once by the guard (see
	// compatReadBody). Handlers decode it with compatJSON or compatBody and
	// never read r.Body.
	body []byte
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
		if c.body, ok = compatReadBody(w, r); !ok {
			return
		}
		rt.h(w, admit(r, p), c)
	})
}

// compatReadBody reads the body of a write request up front, under the
// request body deadline and size limit, and refuses a non-empty body that is
// not declared as JSON. Browsers can not send application/json cross site
// without a CORS preflight, so cached Basic credentials can not be used by a
// form post on another site. Bodyless writes (Mailpit's "delete all") need
// no content type. The handler then decodes the returned bytes.
func compatReadBody(w http.ResponseWriter, r *http.Request) ([]byte, bool) {
	if r.Method == http.MethodGet || r.Method == http.MethodHead {
		return nil, true
	}
	data, err := readBodyWithDeadline(w, r, compatMaxBody)
	var mbe *http.MaxBytesError
	switch {
	case errors.As(err, &mbe):
		compatText(w, http.StatusRequestEntityTooLarge, "request body too large")
		return nil, false
	case err != nil:
		compatText(w, http.StatusBadRequest, "can not read request body")
		return nil, false
	}
	if len(bytes.TrimSpace(data)) > 0 && !isJSONContentType(r) {
		compatText(w, http.StatusUnsupportedMediaType, "Content-Type must be application/json")
		return nil, false
	}
	return data, true
}

// compatAuthenticate resolves the principal from the Authorization header.
func (s *server) compatAuthenticate(w http.ResponseWriter, r *http.Request) (authz.Principal, bool) {
	secret, present := compatSecret(r)
	if secret == "" {
		if !present {
			// The challenge lets clients that wait for one send their
			// credentials (HTTP libraries that only answer a challenge, such
			// as .NET's NetworkCredential or curl --anyauth), and a browser
			// ask for them for the /view pages. Cached browser credentials
			// can not drive writes: those need a JSON body, see
			// compatReadBody.
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
	_, _ = w.Write([]byte(msg)) //nolint:gosec // G705: text/plain with nosniff, never rendered as HTML
}

func compatNotFound(w http.ResponseWriter) { compatText(w, http.StatusNotFound, "404 page not found") }

func compatOK(w http.ResponseWriter) { compatText(w, http.StatusOK, "ok") }
