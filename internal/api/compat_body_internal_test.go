package api

import (
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
)

// The guard reads the body once; compatJSON decodes that buffer and must
// keep the strict rules of the former second read.
func TestCompatJSONDecodesTheGuardBuffer(t *testing.T) {
	tests := []struct {
		name      string
		body      string
		wantEmpty bool
		wantErr   bool
		wantIDs   []string
	}{
		{"nil body is empty", "", true, false, nil},
		{"blank body is empty", " \n\t", true, false, nil},
		{"ids", `{"IDs":["a","b"]}`, false, false, []string{"a", "b"}},
		{"keys ignore case", `{"ids":["a"]}`, false, false, []string{"a"}},
		{"unknown field refused", `{"Ids":["a"],"x":1}`, false, true, nil},
		{"mistyped key refused", `{"IDz":["a"]}`, false, true, nil},
		{"trailing data refused", `{"IDs":[]} {}`, false, true, nil},
		{"syntax error refused", `{"IDs":`, false, true, nil},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var body struct{ IDs []string }
			empty, err := compatJSON(&compatCall{body: []byte(tc.body)}, &body)
			if empty != tc.wantEmpty || (err != nil) != tc.wantErr {
				t.Fatalf("empty %v err %v; want empty %v err %v", empty, err, tc.wantEmpty, tc.wantErr)
			}
			if !tc.wantErr && !slices.Equal(body.IDs, tc.wantIDs) {
				t.Errorf("IDs = %q, want %q", body.IDs, tc.wantIDs)
			}
		})
	}
}

func TestCompatBodyRequiresAValue(t *testing.T) {
	for _, b := range []string{"", "   ", `{"nope":1}`} {
		rec := httptest.NewRecorder()
		var dst struct{ IDs []string }
		if compatBody(rec, &compatCall{body: []byte(b)}, &dst) || rec.Code != http.StatusBadRequest {
			t.Errorf("body %q: ok or status %d, want 400", b, rec.Code)
		}
	}
	rec := httptest.NewRecorder()
	var dst struct{ IDs []string }
	if !compatBody(rec, &compatCall{body: []byte(`{"IDs":["1"]}`)}, &dst) || len(dst.IDs) != 1 {
		t.Errorf("valid body refused: %d %v", rec.Code, dst.IDs)
	}
}

func TestCompatReadBodyReturnsTheBuffer(t *testing.T) {
	tests := []struct {
		name, method, ct, body string
		wantOK                 bool
		wantStatus             int
	}{
		{"get skips the body", http.MethodGet, "", "ignored", true, http.StatusOK},
		{"json kept", http.MethodPut, "application/json", `{"IDs":[]}`, true, http.StatusOK},
		{"bodyless write needs no type", http.MethodDelete, "", "", true, http.StatusOK},
		{"non json refused", http.MethodPut, "text/plain", `{"IDs":[]}`, false, http.StatusUnsupportedMediaType},
		{"too large", http.MethodPut, "application/json", strings.Repeat(" ", compatMaxBody+1), false, http.StatusRequestEntityTooLarge},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequestWithContext(t.Context(), tc.method, "/api/v1/messages", strings.NewReader(tc.body))
			if tc.ct != "" {
				r.Header.Set("Content-Type", tc.ct)
			}
			rec := httptest.NewRecorder()
			data, ok := compatReadBody(rec, r)
			if ok != tc.wantOK || rec.Code != tc.wantStatus {
				t.Fatalf("ok %v status %d; want %v %d", ok, rec.Code, tc.wantOK, tc.wantStatus)
			}
			if ok && tc.method != http.MethodGet && string(data) != tc.body {
				t.Errorf("buffer = %q, want %q", data, tc.body)
			}
		})
	}
}
