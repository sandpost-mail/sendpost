package api

import (
	"errors"
	"log/slog"
	"net/http"
	"runtime/debug"
	"time"
)

// statusWriter records the status and size of a response. It keeps the
// Flusher (SSE) and, through Unwrap, the deadline controls of the writer it
// wraps.
type statusWriter struct {
	http.ResponseWriter
	status int
	bytes  int64
}

func (w *statusWriter) WriteHeader(code int) {
	if w.status == 0 {
		w.status = code
	}
	w.ResponseWriter.WriteHeader(code)
}

func (w *statusWriter) Write(b []byte) (int, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	n, err := w.ResponseWriter.Write(b)
	w.bytes += int64(n)
	return n, err
}

func (w *statusWriter) Flush() {
	if f, ok := w.ResponseWriter.(http.Flusher); ok {
		if w.status == 0 {
			w.status = http.StatusOK
		}
		f.Flush()
	}
}

func (w *statusWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

// accessLog logs one line per request: method, route pattern (never the raw
// path or query, which can carry grants or search text), status, duration and
// response size. It also recovers handler panics and logs them with their
// stack through slog instead of the standard library logger.
func accessLog(log *slog.Logger, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		sw := &statusWriter{ResponseWriter: w}
		defer func() {
			if v := recover(); v != nil {
				if err, ok := v.(error); ok && errors.Is(err, http.ErrAbortHandler) {
					panic(v)
				}
				log.Error("handler panic", "method", r.Method, "pattern", routePattern(r),
					"panic", v, "stack", string(debug.Stack()))
				if sw.status == 0 {
					writeError(sw, http.StatusInternalServerError, "internal error")
				}
			}
			level := slog.LevelInfo
			if r.Pattern == "GET /healthz" || r.Pattern == "GET /readyz" {
				level = slog.LevelDebug
			}
			status := sw.status
			if status == 0 {
				status = http.StatusOK
			}
			log.Log(r.Context(), level, "http request", "method", r.Method, "pattern", routePattern(r),
				"status", status, "duration_ms", time.Since(start).Milliseconds(), "bytes", sw.bytes)
		}()
		next.ServeHTTP(sw, r)
	})
}

// routePattern is the ServeMux pattern that matched, set on r by the mux.
func routePattern(r *http.Request) string {
	if r.Pattern == "" {
		return "unmatched"
	}
	return r.Pattern
}
