package api

import (
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"mime"
	"net/http"
	"strconv"

	"github.com/sandpost-mail/sandpost/internal/credentials"
	"github.com/sandpost-mail/sandpost/internal/events"
	"github.com/sandpost-mail/sandpost/internal/messages"
	"github.com/sandpost-mail/sandpost/internal/projects"
)

const (
	// V1Prefix is the base path of the native API.
	V1Prefix        = "/_/api/v1"
	maxBodyBytes    = 64 << 10
	contentTypeJSON = "application/json"
)

// ServerInfo is public, non secret server information the UI needs.
type ServerInfo struct {
	SMTPPort    int    `json:"smtp_port"`
	SMTPTLSMode string `json:"smtp_tls_mode"`
	Version     string `json:"version"`
}

// Deps are the collaborators of the handlers. Only DB is needed for the health
// endpoints, the native API is served when Projects is set.
type Deps struct {
	DB          Pinger
	Projects    *projects.Service
	Credentials *credentials.Service
	Messages    *messages.Repository
	Bus         *events.Bus
	Info        ServerInfo
	// UI serves the embedded single page app for every non API path.
	UI  http.Handler
	Log *slog.Logger
}

type server struct {
	Deps
}

// NewHandler returns the HTTP handler: health endpoints, the native API under
// /_/api/v1 and the UI for everything else.
//
// Until M2 lands there is no authentication on the native API. Run Sandpost on
// a trusted network. Mutating requests must be JSON, which keeps simple
// cross site form posts from reaching the handlers.
func NewHandler(d Deps) http.Handler {
	if d.Log == nil {
		d.Log = slog.New(slog.DiscardHandler)
	}
	mux := http.NewServeMux()
	registerHealth(mux, d.DB)
	if d.Projects != nil {
		s := &server{Deps: d}
		s.registerV1(mux)
	}
	if d.UI != nil {
		mux.Handle("/", d.UI)
	}
	return mux
}

func (s *server) registerV1(mux *http.ServeMux) {
	const p = V1Prefix + "/projects/{pid}"
	route := func(pattern string, h http.HandlerFunc) {
		mux.Handle(pattern, requireJSONWrites(h))
	}
	mux.HandleFunc(V1Prefix+"/", func(w http.ResponseWriter, _ *http.Request) {
		writeError(w, http.StatusNotFound, "not found")
	})
	route("GET "+V1Prefix+"/info", s.info)
	route("GET "+V1Prefix+"/projects", s.listProjects)
	route("POST "+V1Prefix+"/projects", s.createProject)
	route("GET "+p, s.withProject(s.getProject))

	route("GET "+p+"/credentials", s.withProject(s.listCredentials))
	route("POST "+p+"/credentials", s.withProject(s.createCredential))
	route("DELETE "+p+"/credentials/{cid}", s.withProject(s.revokeCredential))

	route("GET "+p+"/tag-rules", s.withProject(s.listTagRules))
	route("POST "+p+"/tag-rules", s.withProject(s.createTagRule))
	route("DELETE "+p+"/tag-rules/{rid}", s.withProject(s.deleteTagRule))

	route("GET "+p+"/messages", s.withProject(s.listMessages))
	route("GET "+p+"/messages/{mid}", s.withProject(s.getMessage))
	route("POST "+p+"/messages/{mid}/read", s.withProject(s.markRead))
	route("GET "+p+"/messages/{mid}/raw", s.withProject(s.rawMessage))
	route("GET "+p+"/messages/{mid}/html", s.withProject(s.htmlMessage))
	route("GET "+p+"/messages/{mid}/attachments/{ordinal}", s.withProject(s.attachment))
	route("GET "+p+"/events", s.withProject(s.streamEvents))
}

// projectHandler receives the validated project id.
type projectHandler func(w http.ResponseWriter, r *http.Request, projectID int64)

// withProject parses {pid} and checks the project exists, so project scoped
// endpoints answer 404 for unknown projects instead of empty data.
func (s *server) withProject(h projectHandler) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		pid, ok := pathID(w, r, "pid")
		if !ok {
			return
		}
		if _, err := s.Projects.Get(r.Context(), pid); err != nil {
			s.fail(w, r, err)
			return
		}
		h(w, r, pid)
	}
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

// fail maps domain errors to responses. Unknown errors are logged and answered
// with a generic 500 so internals never reach the client.
func (s *server) fail(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, projects.ErrNotFound), errors.Is(err, messages.ErrNotFound),
		errors.Is(err, credentials.ErrNotFound), errors.Is(err, credentials.ErrProjectNotFound):
		writeError(w, http.StatusNotFound, "not found")
	case errors.Is(err, projects.ErrSlugTaken):
		writeError(w, http.StatusConflict, err.Error())
	case errors.Is(err, projects.ErrInvalid), errors.Is(err, credentials.ErrLabelTooLong),
		errors.Is(err, messages.ErrInvalidCursor):
		writeError(w, http.StatusBadRequest, err.Error())
	default:
		s.Log.Error("request failed", "method", r.Method, "path", r.URL.Path, "error", err)
		writeError(w, http.StatusInternalServerError, "internal error")
	}
}
