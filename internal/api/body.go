package api

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"time"
)

// bodyReadTimeout bounds how long a client may take to send a request body.
// It is set per request, around the body read only: a server wide
// ReadTimeout would stay armed after the body and end SSE streams and the
// wait long poll, which never read a body.
const bodyReadTimeout = 30 * time.Second

// readBodyWithDeadline reads at most limit bytes of the body under a read
// deadline, then clears the deadline. Writers that can not set deadlines (test
// recorders) read without one.
func readBodyWithDeadline(w http.ResponseWriter, r *http.Request, limit int64) ([]byte, error) {
	rc := http.NewResponseController(w)
	if err := rc.SetReadDeadline(time.Now().Add(bodyReadTimeout)); err == nil {
		defer func() { _ = rc.SetReadDeadline(time.Time{}) }()
	}
	data, err := io.ReadAll(http.MaxBytesReader(w, r.Body, limit))
	if err != nil {
		return nil, fmt.Errorf("read body: %w", err)
	}
	return data, nil
}

func isJSONContentType(r *http.Request) bool {
	mt, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	return err == nil && mt == contentTypeJSON
}

// decodeBody decodes exactly one JSON value into dst. Unknown fields, trailing
// data and syntax errors are 400, a body over maxBodyBytes is 413. An empty
// body leaves dst unchanged.
func decodeBody(w http.ResponseWriter, r *http.Request, dst any) bool {
	data, err := readBodyWithDeadline(w, r, maxBodyBytes)
	var mbe *http.MaxBytesError
	switch {
	case errors.As(err, &mbe):
		writeError(w, http.StatusRequestEntityTooLarge, "request body too large")
		return false
	case err != nil:
		writeError(w, http.StatusBadRequest, "can not read request body")
		return false
	}
	if err := decodeStrict(data, dst); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return false
	}
	return true
}

// errTrailingData is a JSON body with more than one value.
var errTrailingData = errors.New("trailing data after JSON value")

func decodeStrict(data []byte, dst any) error {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		if errors.Is(err, io.EOF) {
			return nil
		}
		return fmt.Errorf("decode body: %w", err)
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return errTrailingData
	}
	return nil
}
