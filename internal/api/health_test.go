package api_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/sandpost-mail/sandpost/internal/api"
)

type pinger func(context.Context) error

func (p pinger) Ping(ctx context.Context) error { return p(ctx) }

func get(h http.Handler, path string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
	return rec
}

func TestHealthz(t *testing.T) {
	h := api.NewHandler(pinger(func(context.Context) error { return errors.New("down") }))
	if rec := get(h, "/healthz"); rec.Code != http.StatusOK {
		t.Fatalf("healthz must not depend on the database, got %d", rec.Code)
	}
}

func TestReadyz(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want int
	}{
		{"ready", nil, http.StatusOK},
		{"database down", errors.New("boom"), http.StatusServiceUnavailable},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := api.NewHandler(pinger(func(context.Context) error { return tt.err }))
			rec := get(h, "/readyz")
			if rec.Code != tt.want {
				t.Fatalf("got %d want %d", rec.Code, tt.want)
			}
			if tt.err != nil && rec.Body.String() == tt.err.Error() {
				t.Error("must not leak the underlying error")
			}
		})
	}
}

func TestUnknownRoute(t *testing.T) {
	h := api.NewHandler(pinger(func(context.Context) error { return nil }))
	if rec := get(h, "/nope"); rec.Code != http.StatusNotFound {
		t.Fatalf("got %d", rec.Code)
	}
}
