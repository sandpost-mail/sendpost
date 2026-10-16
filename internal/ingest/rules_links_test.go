package ingest

import (
	"reflect"
	"testing"
)

func TestWildcardMatch(t *testing.T) {
	tests := []struct {
		pattern, s string
		want       bool
	}{
		{"", "anything", true},
		{"a@b.test", "A@B.test", true},
		{"a@b.test", "x@b.test", false},
		{"*@b.test", "someone@B.TEST", true},
		{"*@b.test", "someone@c.test", false},
		{"qa+*@x.test", "qa+run7@x.test", true},
		{"qa+*@x.test", "qa@x.test", false},
		{"*run*", "a-run-b", true},
		{"a*a", "a", false},
		{"a*a", "aa", true},
		{"a*b*c", "a1b2c", true},
		{"a*b*c", "a1c2b", false},
		{"*", "", true},
	}
	for _, tt := range tests {
		if got := wildcardMatch(tt.pattern, tt.s); got != tt.want {
			t.Errorf("wildcardMatch(%q,%q)=%v want %v", tt.pattern, tt.s, got, tt.want)
		}
	}
}

func TestApplyRules(t *testing.T) {
	headers := map[string][]string{"X-Run-Id": {"run-42"}, "X-Env": {"staging", "Prod"}, "Subject": {"x"}}
	rcpts := []Recipient{{KindTo, "qa+7@example.test"}, {KindEnvelope, "ops@corp.test"}}
	tests := []struct {
		name  string
		rules []Rule
		want  []string
	}{
		{"no rules", nil, []string{}},
		{"header value placeholder", []Rule{{RuleHeader, "x-run-id", "", "run:{value}"}}, []string{"run:run-42"}},
		{"header presence", []Rule{{RuleHeader, "X-Env", "", "has-env"}}, []string{"has-env"}},
		{"header pattern selects values", []Rule{{RuleHeader, "X-Env", "prod", "env:{value}"}}, []string{"env:Prod"}},
		{"header absent", []Rule{{RuleHeader, "X-Missing", "", "t"}}, []string{}},
		{"recipient glob", []Rule{{RuleRecipient, "", "qa+*@example.test", "qa"}}, []string{"qa"}},
		{"recipient envelope match", []Rule{{RuleRecipient, "", "*@corp.test", "corp"}}, []string{"corp"}},
		{"recipient no match", []Rule{{RuleRecipient, "", "*@other.test", "o"}}, []string{}},
		{"dedupe and sort", []Rule{
			{RuleRecipient, "", "*", "b"}, {RuleRecipient, "", "*", "a"}, {RuleRecipient, "", "*", "b"},
		}, []string{"a", "b"}},
		{"unknown kind ignored", []Rule{{"weird", "", "", "t"}}, []string{}},
		{"blank tag dropped", []Rule{{RuleRecipient, "", "*", "  \n "}}, []string{}},
		{"control characters stripped", []Rule{{RuleRecipient, "", "*", "a\nb"}}, []string{"ab"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ApplyRules(tt.rules, headers, rcpts)
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("got %v want %v", got, tt.want)
			}
		})
	}
}

func TestApplyRulesDoesNotMutateInputs(t *testing.T) {
	headers := map[string][]string{"X-A": {"v"}}
	rules := []Rule{{RuleHeader, "X-A", "", "t:{value}"}}
	_ = ApplyRules(rules, headers, nil)
	if rules[0].Tag != "t:{value}" || headers["X-A"][0] != "v" {
		t.Error("inputs were mutated")
	}
}

func TestNormalizeTagTruncates(t *testing.T) {
	long := make([]rune, 300)
	for i := range long {
		long[i] = 'é'
	}
	if got := len([]rune(normalizeTag(string(long)))); got != maxTagRunes {
		t.Errorf("runes %d", got)
	}
}

func TestExtractLinks(t *testing.T) {
	tests := []struct {
		name, html, text string
		want             []string
	}{
		{"empty", "", "", nil},
		{"trailing punctuation", "", "see https://a.test/x. and (https://b.test/y), https://c.test/z?a=(1)!", []string{
			"https://a.test/x", "https://b.test/y", "https://c.test/z?a=(1)",
		}},
		{"html anchors and area", `<a href="https://a.test">a</a><area href="http://b.test/m"><a href="ftp://x">f</a><a name=n>`,
			"", []string{"https://a.test", "http://b.test/m"}},
		{"dedupe across html and text", `<a href="https://a.test">a</a>`, "https://a.test", []string{"https://a.test"}},
		{"case insensitive scheme", `<a href="HTTPS://A.test">a</a>`, "", []string{"HTTPS://A.test"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ExtractLinks(tt.html, tt.text); !reflect.DeepEqual(got, tt.want) {
				t.Errorf("got %v want %v", got, tt.want)
			}
		})
	}
}

func TestExtractLinksIsBounded(t *testing.T) {
	var text string
	for i := 0; i < maxLinks+100; i++ {
		text += "https://a.test/" + string(rune('a'+i%26)) + string(rune('a'+(i/26)%26)) + string(rune('a'+(i/676)%26)) + " "
	}
	if got := len(ExtractLinks("", text)); got != maxLinks {
		t.Errorf("links %d", got)
	}
}

func TestCleanAndTruncate(t *testing.T) {
	if got := clean("a\x00b\xffc"); got != "ab�c" {
		t.Errorf("clean %q", got)
	}
	if got := truncateRunes("héllo", 2); got != "hé" {
		t.Errorf("truncate %q", got)
	}
	if got := truncateRunes("abc", 10); got != "abc" {
		t.Errorf("truncate %q", got)
	}
	if got := truncateBytes("héllo", 2); got != "h" {
		t.Errorf("truncateBytes %q", got)
	}
}
