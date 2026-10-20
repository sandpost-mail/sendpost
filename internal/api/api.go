package api

import (
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"mime"
	"net/http"
	"strconv"

	"github.com/sandpost-mail/sandpost/internal/audit"
	"github.com/sandpost-mail/sandpost/internal/auth"
	"github.com/sandpost-mail/sandpost/internal/authz"
	"github.com/sandpost-mail/sandpost/internal/credentials"
	"github.com/sandpost-mail/sandpost/internal/events"
	"github.com/sandpost-mail/sandpost/internal/groups"
	"github.com/sandpost-mail/sandpost/internal/messages"
	"github.com/sandpost-mail/sandpost/internal/projects"
	"github.com/sandpost-mail/sandpost/internal/tokens"
	"github.com/sandpost-mail/sandpost/internal/users"
)

const (
	// V1Prefix is the base path of the native API.
	V1Prefix        = "/_/api/v1"
	maxBodyBytes    = 64 << 10
	contentTypeJSON = "application/json"
)

// ServerInfo is non secret server information the UI needs (served to authenticated callers).
type ServerInfo struct {
	SMTPPort    int    `json:"smtp_port"`
	SMTPTLSMode string `json:"smtp_tls_mode"`
	Version     string `json:"version"`
}

// Deps are the collaborators of the handlers. Only DB is needed for the health
// endpoints, the native API is served when Authz is set.
type Deps struct {
	DB          Pinger
	Authz       *authz.Authorizer
	Auth        *auth.Service
	Tokens      *tokens.Service
	Users       *users.Service
	Groups      *groups.Service
	Audit       *audit.Reader
	Signer      *authz.Signer
	Projects    *projects.Service
	Credentials *credentials.Service
	Messages    *messages.Repository
	Bus         *events.Bus
	Info        ServerInfo
	// CookieSecure marks the session cookie Secure (and uses the __Host-
	// prefix). Set it whenever the UI is served over HTTPS, including behind
	// a TLS terminating proxy.
	CookieSecure bool
	// UI serves the embedded single page app for every non API path.
	UI  http.Handler
	Log *slog.Logger
}

type server struct {
	Deps
	logins *loginLimits
	// loginSlots bounds concurrent password checks: each argon2 verify takes
	// 64 MiB, and the failure limit is only exact when checks do not overlap.
	loginSlots chan struct{}
}

// NewHandler returns the HTTP handler: health endpoints, the native API under
// /_/api/v1 and the UI for everything else.
//
// Every native API route except login and the signed preview URLs
// requires authentication, see routes.go. Mutating requests must be JSON,
// which keeps simple cross site form posts from reaching the handlers, and
// cookie authenticated ones also need the CSRF header.
func NewHandler(d Deps) http.Handler {
	if d.Log == nil {
		d.Log = slog.New(slog.DiscardHandler)
	}
	mux := http.NewServeMux()
	registerHealth(mux, d.DB)
	if d.Authz != nil {
		s := &server{Deps: d, logins: newLoginLimits(), loginSlots: make(chan struct{}, maxConcurrentLogins)}
		s.registerV1(mux)
	}
	if d.UI != nil {
		mux.Handle("/", d.UI)
	}
	return mux
}

func pathID(w http.ResponseWriter, r *http.Request, name string) (int64, bool) {
	id, err := strconv.ParseInt(r.PathValue(name), 10, 64)
	if err != nil || id <= 0 {
		writeError(w, http.StatusBadRequest, "invalid "+name)
		return 0, false
	}
	return id, true
}

// requireJSONWrites rejects non GET/HEAD requests whose content type is not
// JSON. Browsers can not send that cross site without a CORS preflight.
func requireJSONWrites(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			mt, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
			if err != nil || mt != contentTypeJSON {
				writeError(w, http.StatusUnsupportedMediaType, "Content-Type must be application/json")
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

func decodeBody(w http.ResponseWriter, r *http.Request, dst any) bool {
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBodyBytes))
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil && !errors.Is(err, io.EOF) {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return false
	}
	return true
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", contentTypeJSON)
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}

// writeErrorCode adds a stable machine readable code the UI can branch on.
func writeErrorCode(w http.ResponseWriter, status int, code, msg string) {
	writeJSON(w, status, map[string]string{"error": msg, "code": code})
}

// Stable error codes.
const (
	codePasswordChange = "password_change_required"
	codeCSRF           = "csrf_invalid"
	codeUnauthorized   = "unauthenticated"
)
