// Package page holds the pagination helpers shared by the list endpoints:
// limit clamping and the two cursor formats in use.
//
// The cursor formats are part of the API: clients store NextCursor values and
// send them back, so the encodings here must keep decoding exactly what
// earlier releases produced.
//
//   - Keyset cursors (messages, activity) are base64url (no padding) over a
//     few fields joined by a separator, for example "<unix micros>:<id>".
//   - ID cursors (audit, comments) are a plain positive decimal row id.
package page

import (
	"encoding/base64"
	"errors"
	"strconv"
	"strings"
	"time"
)

// ErrInvalidCursor is returned for a cursor that does not decode.
var ErrInvalidCursor = errors.New("invalid cursor")

// Clamp returns def for a non-positive limit and caps it at maxLimit.
func Clamp(limit, def, maxLimit int) int {
	switch {
	case limit <= 0:
		return def
	case limit > maxLimit:
		return maxLimit
	default:
		return limit
	}
}

// EncodeKeyset joins fields with sep and encodes them as base64url without
// padding. Fields must not contain sep.
func EncodeKeyset(sep string, fields ...string) string {
	return base64.RawURLEncoding.EncodeToString([]byte(strings.Join(fields, sep)))
}

// DecodeKeyset reverses EncodeKeyset and requires exactly n fields.
func DecodeKeyset(c, sep string, n int) ([]string, error) {
	raw, err := base64.RawURLEncoding.DecodeString(c)
	if err != nil {
		return nil, ErrInvalidCursor
	}
	fields := strings.Split(string(raw), sep)
	if len(fields) != n {
		return nil, ErrInvalidCursor
	}
	return fields, nil
}

// FormatMicros renders t as decimal unix microseconds, the time field of the
// keyset cursors.
func FormatMicros(t time.Time) string { return strconv.FormatInt(t.UnixMicro(), 10) }

// ParseMicros reverses FormatMicros and returns a UTC time.
func ParseMicros(s string) (time.Time, error) {
	micro, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return time.Time{}, ErrInvalidCursor
	}
	return time.UnixMicro(micro).UTC(), nil
}

// ParseInt parses a decimal int64 cursor field.
func ParseInt(s string) (int64, error) {
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return 0, ErrInvalidCursor
	}
	return n, nil
}

// EncodeID renders an ID cursor.
func EncodeID(id int64) string { return strconv.FormatInt(id, 10) }

// DecodeID parses an ID cursor; it must be a positive decimal id.
func DecodeID(c string) (int64, error) {
	id, err := strconv.ParseInt(c, 10, 64)
	if err != nil || id <= 0 {
		return 0, ErrInvalidCursor
	}
	return id, nil
}
