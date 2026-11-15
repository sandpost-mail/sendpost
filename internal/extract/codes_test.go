package extract_test

import (
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/sandpost-mail/sandpost/internal/extract"
)

func vals(cs []extract.Code) []string {
	out := []string{}
	for _, c := range cs {
		out = append(out, c.Value)
	}
	return out
}

func TestCodes(t *testing.T) {
	tests := []struct {
		name, text string
		want       []string
		kind       string
	}{
		{"plain", "Your verification code is 482913.", []string{"482913"}, extract.KindKeyword},
		{"colon", "Code: 4829", []string{"4829"}, extract.KindKeyword},
		{"five digits", "Use PIN 48291 to continue", []string{"48291"}, extract.KindKeyword},
		{"eight digits", "One-time code 12345678", []string{"12345678"}, extract.KindKeyword},
		{"grouped space", "Your security code is 482 913", []string{"482913"}, extract.KindKeyword},
		{"grouped dash", "OTP: 482-913", []string{"482913"}, extract.KindKeyword},
		{"alphanumeric", "Your access code: AB12-CD34", []string{"AB12CD34"}, extract.KindKeyword},
		{"alnum token", "login token\nX7K9Q2ZP", nil, ""},
		{"next line is not glue", "Your code\n123456", []string{"123456"}, extract.KindDigits},
		{"case insensitive", "YOUR OTP IS 998877", []string{"998877"}, extract.KindKeyword},
		{"word without digit", "Enter the code below to continue", nil, ""},
		{"distinct, in order", "code 111111 ... pin 222222 ... code 111111", []string{"111111", "222222"}, extract.KindKeyword},
		{"keyword beats bare digits", "Order 123456 shipped. Your code is 654321", []string{"654321"}, extract.KindKeyword},
		{"bare 6 digits fallback", "Welcome!\n\n 482913 \n\nBye", []string{"482913"}, extract.KindDigits},
		{"bare digits ignore numbers with separators", "Total 1,234.56 on 2026-10-14 at 12:30:45 call +33123456", nil, ""},
		{"urls are ignored", "Click https://x.test/verify?token=998877 or code 112233 here", []string{"112233"}, extract.KindKeyword},
		{"urls only", "https://x.test/a/123456/b", nil, ""},
		{"too long digit run", "ref 1234567890123", nil, ""},
		{"empty", "", nil, ""},
		{"adjacent bare codes", "Codes: 111111 222222 333333", []string{"111111", "222222", "333333"}, extract.KindDigits},
		{"two bare codes", "123456 654321", []string{"123456", "654321"}, extract.KindDigits},
		{"bare code after phone prefix", "+123456 or 654321+", []string{"654321"}, extract.KindDigits},
		{"seven digit run is not a code", "1234567 and 7654321", nil, ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := extract.Codes(tc.text, "")
			if err != nil {
				t.Fatal(err)
			}
			if tc.want == nil {
				tc.want = []string{}
			}
			if !reflect.DeepEqual(vals(got), tc.want) {
				t.Fatalf("got %v want %v", vals(got), tc.want)
			}
			for _, c := range got {
				if c.Kind != tc.kind {
					t.Errorf("kind %q want %q", c.Kind, tc.kind)
				}
			}
		})
	}
}

func TestCustomPattern(t *testing.T) {
	text := "ref ABC-1234 and ref XYZ-9999, ABC-1234 again"
	got, err := extract.Codes(text, `[A-Z]{3}-\d{4}`)
	if err != nil || !reflect.DeepEqual(vals(got), []string{"ABC-1234", "XYZ-9999"}) || got[0].Kind != extract.KindCustom {
		t.Fatalf("whole match: %v %v", got, err)
	}
	got, err = extract.Codes(text, `ref [A-Z]{3}-(\d{4})`)
	if err != nil || !reflect.DeepEqual(vals(got), []string{"1234", "9999"}) {
		t.Fatalf("group 1: %v %v", got, err)
	}
	// Group 1 is the code, even when later groups exist.
	got, err = extract.Codes("code: 4242 (expires) code: 5151", `code: (\d+)( \(expires\))?`)
	if err != nil || !reflect.DeepEqual(vals(got), []string{"4242", "5151"}) {
		t.Fatalf("group 1 with optional group 2: %v %v", vals(got), err)
	}
	got, _ = extract.Codes("code 123456", `nomatch`)
	if len(got) != 0 {
		t.Fatalf("custom pattern replaces the heuristics: %v", got)
	}
}

func TestBadPattern(t *testing.T) {
	for _, p := range []string{`(`, `(?=x)`, strings.Repeat("a", extract.MaxPatternLength+1)} {
		if _, err := extract.Codes("x", p); !errors.Is(err, extract.ErrBadPattern) {
			t.Errorf("%q: %v", p, err)
		}
	}
}

func TestLimits(t *testing.T) {
	var b strings.Builder
	for i := 0; i < 100; i++ {
		b.WriteString("code 1000")
		b.WriteString(strings.Repeat("0", 0))
		b.WriteString(string(rune('0' + i%10)))
		b.WriteString(string(rune('0' + i/10%10)))
		b.WriteString("\n")
	}
	got, _ := extract.Codes(b.String(), "")
	if len(got) != extract.MaxCodes {
		t.Errorf("got %d codes, want cap %d", len(got), extract.MaxCodes)
	}
	// A pattern matching the empty string everywhere still stops at the cap.
	got, err := extract.Codes(strings.Repeat("ab", 100<<10), `x*`)
	if err != nil || len(got) != 0 {
		t.Errorf("empty matches: %v %v", got, err)
	}
	// Distinct codes past many duplicates are still found up to MaxCodes.
	b.Reset()
	b.WriteString(strings.Repeat("A1 ", 500))
	for i := 0; i < 30; i++ {
		b.WriteString("B" + strings.Repeat("0", 3) + string(rune('0'+i/10)) + string(rune('0'+i%10)) + " ")
	}
	got, _ = extract.Codes(b.String(), `[A-Z]\d+`)
	if len(got) != extract.MaxCodes {
		t.Errorf("got %d codes past duplicates, want %d", len(got), extract.MaxCodes)
	}
	// Text beyond the scan window is ignored, and a huge input stays fast.
	got, _ = extract.Codes(strings.Repeat("x", 300<<10)+" code 123456", "")
	if len(got) != 0 {
		t.Errorf("text past the scan limit must be ignored: %v", got)
	}
}
