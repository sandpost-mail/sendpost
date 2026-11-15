package page_test

import (
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/sandpost-mail/sandpost/internal/page"
)

func TestClamp(t *testing.T) {
	tests := []struct {
		name                  string
		limit, def, max, want int
	}{
		{"zero uses default", 0, 50, 200, 50},
		{"negative uses default", -3, 100, 200, 100},
		{"in range kept", 17, 50, 200, 17},
		{"max kept", 200, 50, 200, 200},
		{"above max capped", 201, 50, 200, 200},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := page.Clamp(tc.limit, tc.def, tc.max); got != tc.want {
				t.Errorf("Clamp(%d, %d, %d) = %d, want %d", tc.limit, tc.def, tc.max, got, tc.want)
			}
		})
	}
}

// The encodings below are what earlier releases sent as NextCursor (computed
// independently: base64url without padding of the raw text). Clients store
// them, so they must keep round-tripping byte for byte.
func TestKeysetWireFormat(t *testing.T) {
	at := time.UnixMicro(1700000000123456).UTC()
	tests := []struct {
		name   string
		sep    string
		fields []string
		wire   string
	}{
		{"messages", ":", []string{page.FormatMicros(at), page.EncodeID(42)}, "MTcwMDAwMDAwMDEyMzQ1Njo0Mg"},
		{"activity", "|", []string{page.FormatMicros(at), "comment", page.EncodeID(7)}, "MTcwMDAwMDAwMDEyMzQ1Nnxjb21tZW50fDc"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := page.EncodeKeyset(tc.sep, tc.fields...); got != tc.wire {
				t.Errorf("EncodeKeyset = %q, want %q", got, tc.wire)
			}
			got, err := page.DecodeKeyset(tc.wire, tc.sep, len(tc.fields))
			if err != nil || !slices.Equal(got, tc.fields) {
				t.Fatalf("DecodeKeyset = %q, %v; want %q", got, err, tc.fields)
			}
			back, err := page.ParseMicros(got[0])
			if err != nil || !back.Equal(at) || back.Location() != time.UTC {
				t.Errorf("ParseMicros = %v, %v; want %v UTC", back, err, at)
			}
		})
	}
}

func TestDecodeKeysetRejects(t *testing.T) {
	tests := []struct{ name, c string }{
		{"not base64", "%%%"},
		{"padded base64", "MTox=="},
		{"too few fields", page.EncodeKeyset(":", "1")},
		{"too many fields", page.EncodeKeyset(":", "1", "2", "3")},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := page.DecodeKeyset(tc.c, ":", 2); !errors.Is(err, page.ErrInvalidCursor) {
				t.Errorf("err = %v, want ErrInvalidCursor", err)
			}
		})
	}
}

func TestParseFieldsReject(t *testing.T) {
	if _, err := page.ParseMicros("x"); !errors.Is(err, page.ErrInvalidCursor) {
		t.Errorf("ParseMicros err = %v", err)
	}
	if _, err := page.ParseInt("1.5"); !errors.Is(err, page.ErrInvalidCursor) {
		t.Errorf("ParseInt err = %v", err)
	}
	if n, err := page.ParseInt("-4"); err != nil || n != -4 {
		t.Errorf("ParseInt(-4) = %d, %v", n, err)
	}
}

func TestIDCursor(t *testing.T) {
	if got := page.EncodeID(1234); got != "1234" {
		t.Errorf("EncodeID = %q, want plain decimal", got)
	}
	if id, err := page.DecodeID("1234"); err != nil || id != 1234 {
		t.Errorf("DecodeID = %d, %v", id, err)
	}
	for _, bad := range []string{"", "0", "-1", "abc", "1e3", "MTIz"} {
		if _, err := page.DecodeID(bad); !errors.Is(err, page.ErrInvalidCursor) {
			t.Errorf("DecodeID(%q) err = %v, want ErrInvalidCursor", bad, err)
		}
	}
}
