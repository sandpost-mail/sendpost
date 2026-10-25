package policy_test

import (
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/sandpost-mail/sandpost/internal/policy"
)

const rawMsg = "From: App <app@example.test>\r\nTo: Real <real@corp.example>, Other <other@elsewhere.test>\r\n" +
	"Cc: cc@corp.example\r\nBcc: secret@elsewhere.test\r\nSubject: Welcome\r\n\r\nHello body\r\n"

var okTransport = policy.TransportState{Present: true, Enabled: true}

func msg(envelope ...string) policy.Message {
	return policy.Message{
		Raw: []byte(rawMsg), EnvelopeTo: envelope,
		To: []string{"real@corp.example", "other@elsewhere.test"}, Cc: []string{"cc@corp.example"},
	}
}

func allowPolicy(list ...string) *policy.Policy {
	return &policy.Policy{Mode: policy.ModeAllow, Allowlist: list}
}

func redirectPolicy(redirect string, list ...string) *policy.Policy {
	return &policy.Policy{Mode: policy.ModeRedirect, Allowlist: list, RedirectAddress: redirect, SubjectPrefix: "[staging]"}
}

func decisions(p policy.Plan) map[string]string {
	out := map[string]string{}
	for _, d := range p.Decisions {
		out[d.Recipient] = string(d.Action) + ":" + d.Effective
	}
	return out
}

func envelopes(p policy.Plan) [][]string {
	var out [][]string
	for _, o := range p.Outbound {
		out = append(out, o.Recipients())
	}
	return out
}

func TestEvaluateCaptureSendsNothing(t *testing.T) {
	for name, pol := range map[string]*policy.Policy{
		"no policy":           nil,
		"capture":             {Mode: policy.ModeCapture},
		"capture + allowlist": {Mode: policy.ModeCapture, Allowlist: []string{"@corp.example"}},
	} {
		t.Run(name, func(t *testing.T) {
			plan, err := policy.Evaluate(policy.Input{Policy: pol, Transport: okTransport, Message: msg("real@corp.example")})
			if err != nil || len(plan.Outbound) != 0 || len(plan.Decisions) != 0 {
				t.Errorf("plan %+v err %v, want empty", plan, err)
			}
		})
	}
}

func TestEvaluateAllow(t *testing.T) {
	tests := []struct {
		name     string
		list     []string
		envelope []string
		wantDec  map[string]string
		wantEnv  [][]string
	}{
		{"address match", []string{"real@corp.example"}, []string{"real@corp.example", "other@elsewhere.test"},
			map[string]string{"real@corp.example": "deliver:real@corp.example", "other@elsewhere.test": "skip:"},
			[][]string{{"real@corp.example"}}},
		{"domain match", []string{"@corp.example"}, []string{"a@corp.example", "b@corp.example", "c@other.test"},
			map[string]string{"a@corp.example": "deliver:a@corp.example", "b@corp.example": "deliver:b@corp.example", "c@other.test": "skip:"},
			[][]string{{"a@corp.example", "b@corp.example"}}},
		{"bare domain entry, case insensitive", []string{"Corp.Example"}, []string{"Real@CORP.example"},
			map[string]string{"Real@corp.example": "deliver:Real@corp.example"}, [][]string{{"Real@corp.example"}}},
		{"subdomain is not matched", []string{"@corp.example"}, []string{"a@sub.corp.example"},
			map[string]string{"a@sub.corp.example": "skip:"}, nil},
		{"suffix lookalike is not matched", []string{"@corp.example"}, []string{"a@evilcorp.example", "a@corp.example.evil.test"},
			map[string]string{"a@evilcorp.example": "skip:", "a@corp.example.evil.test": "skip:"}, nil},
		{"empty allowlist skips everyone", nil, []string{"a@corp.example"},
			map[string]string{"a@corp.example": "skip:"}, nil},
		{"duplicates collapse", []string{"@corp.example"}, []string{"a@corp.example", "A@corp.example"},
			map[string]string{"a@corp.example": "deliver:a@corp.example"}, [][]string{{"a@corp.example"}}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			plan, err := policy.Evaluate(policy.Input{Policy: allowPolicy(tc.list...), Transport: okTransport, Message: msg(tc.envelope...)})
			if err != nil {
				t.Fatal(err)
			}
			if got := decisions(plan); !reflect.DeepEqual(got, tc.wantDec) {
				t.Errorf("decisions %v, want %v", got, tc.wantDec)
			}
			if got := envelopes(plan); !reflect.DeepEqual(got, tc.wantEnv) {
				t.Errorf("envelopes %v, want %v", got, tc.wantEnv)
			}
		})
	}
}

func TestEvaluateRedirectSplitsAllowedFromRedirected(t *testing.T) {
	plan, err := policy.Evaluate(policy.Input{
		Policy:    redirectPolicy("pm@corp.example", "real@corp.example"),
		Transport: okTransport,
		Message:   msg("real@corp.example", "other@elsewhere.test", "secret@elsewhere.test"),
	})
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{
		"real@corp.example":     "deliver:real@corp.example",
		"other@elsewhere.test":  "redirect:pm@corp.example",
		"secret@elsewhere.test": "redirect:pm@corp.example",
	}
	if got := decisions(plan); !reflect.DeepEqual(got, want) {
		t.Errorf("decisions %v, want %v", got, want)
	}
	if len(plan.Outbound) != 2 {
		t.Fatalf("want two outbound messages, got %d", len(plan.Outbound))
	}
	direct, redirected := plan.Outbound[0], plan.Outbound[1]
	if direct.Kind() != policy.KindAllowed || !reflect.DeepEqual(direct.Recipients(), []string{"real@corp.example"}) {
		t.Errorf("direct %v %v", direct.Kind(), direct.Recipients())
	}
	// The redirected recipients become ONE envelope recipient, the redirect address.
	if redirected.Kind() != policy.KindRedirect || !reflect.DeepEqual(redirected.Recipients(), []string{"pm@corp.example"}) {
		t.Errorf("redirected %v %v", redirected.Kind(), redirected.Recipients())
	}
	if !reflect.DeepEqual(redirected.Covers(), []string{"other@elsewhere.test", "secret@elsewhere.test"}) {
		t.Errorf("covers %v", redirected.Covers())
	}

	d, r := string(direct.Data()), string(redirected.Data())
	// A real recipient never sees redirect headers.
	for _, h := range []string{"X-Original-To", "X-Original-Cc", "[staging]"} {
		if strings.Contains(d, h) {
			t.Errorf("direct message must not carry %q:\n%s", h, d)
		}
	}
	// The redirect target sees where the mail was meant to go.
	for _, want := range []string{"X-Original-To: real@corp.example, other@elsewhere.test\r\n", "X-Original-Cc: cc@corp.example\r\n", "Subject: [staging] Welcome\r\n"} {
		if !strings.Contains(r, want) {
			t.Errorf("redirected message lacks %q:\n%s", want, r)
		}
	}
	// Bcc never travels, and the body is untouched.
	for _, m := range []string{d, r} {
		if strings.Contains(strings.ToLower(m), "bcc:") || strings.Contains(m, "secret@elsewhere.test") {
			t.Errorf("Bcc leaked:\n%s", m)
		}
		if !strings.HasSuffix(m, "\r\n\r\nHello body\r\n") {
			t.Errorf("body changed:\n%q", m)
		}
	}
}

func TestEvaluateRedirectEverythingWhenAllowlistEmpty(t *testing.T) {
	plan, err := policy.Evaluate(policy.Input{Policy: redirectPolicy("pm@corp.example"), Transport: okTransport, Message: msg("a@x.test", "b@y.test")})
	if err != nil {
		t.Fatal(err)
	}
	if got := envelopes(plan); !reflect.DeepEqual(got, [][]string{{"pm@corp.example"}}) {
		t.Errorf("envelopes %v", got)
	}
}

func TestEvaluateEnvelopeSender(t *testing.T) {
	in := policy.Input{Policy: allowPolicy("@corp.example"), Transport: okTransport, Message: msg("a@corp.example")}
	plan, _ := policy.Evaluate(in)
	if plan.Outbound[0].Sender() != "" {
		t.Errorf("default sender must be the null sender, got %q", plan.Outbound[0].Sender())
	}
	in.Transport.Sender = "Noreply@Sandbox.Example"
	plan, err := policy.Evaluate(in)
	if err != nil || plan.Outbound[0].Sender() != "Noreply@sandbox.example" {
		t.Errorf("sender %v err %v", plan.Outbound, err)
	}
}

// TestEvaluateFailsClosed is the catalogue of states in which nothing may be
// sent. Each one must return ErrBlocked and an empty plan.
func TestEvaluateFailsClosed(t *testing.T) {
	good := msg("real@corp.example")
	tests := []struct {
		name string
		in   policy.Input
	}{
		{"unknown mode", policy.Input{Policy: &policy.Policy{Mode: "relay", Allowlist: []string{"@corp.example"}}, Transport: okTransport, Message: good}},
		{"empty mode", policy.Input{Policy: &policy.Policy{Allowlist: []string{"@corp.example"}}, Transport: okTransport, Message: good}},
		{"mode case differs", policy.Input{Policy: &policy.Policy{Mode: "ALLOW", Allowlist: []string{"@corp.example"}}, Transport: okTransport, Message: good}},
		{"no transport (allow)", policy.Input{Policy: allowPolicy("@corp.example"), Message: good}},
		{"no transport (redirect)", policy.Input{Policy: redirectPolicy("pm@corp.example"), Message: good}},
		{"transport disabled", policy.Input{Policy: allowPolicy("@corp.example"), Transport: policy.TransportState{Present: true}, Message: good}},
		{"enabled but not present", policy.Input{Policy: allowPolicy("@corp.example"), Transport: policy.TransportState{Enabled: true}, Message: good}},
		{"invalid sender", policy.Input{Policy: allowPolicy("@corp.example"), Transport: policy.TransportState{Present: true, Enabled: true, Sender: "not an address"}, Message: good}},
		{"wildcard allowlist entry", policy.Input{Policy: allowPolicy("*"), Transport: okTransport, Message: good}},
		{"wildcard domain entry", policy.Input{Policy: allowPolicy("@*.corp.example"), Transport: okTransport, Message: good}},
		{"one bad entry among good ones", policy.Input{Policy: allowPolicy("@corp.example", "x y@z.test"), Transport: okTransport, Message: good}},
		{"empty allowlist entry", policy.Input{Policy: allowPolicy("@corp.example", ""), Transport: okTransport, Message: good}},
		{"redirect without address", policy.Input{Policy: redirectPolicy("", "@corp.example"), Transport: okTransport, Message: good}},
		{"redirect address invalid", policy.Input{Policy: redirectPolicy("pm@localhost"), Transport: okTransport, Message: good}},
		{"redirect address with display name", policy.Input{Policy: redirectPolicy("PM <pm@corp.example>"), Transport: okTransport, Message: good}},
		{"redirect address is a list", policy.Input{Policy: redirectPolicy("a@corp.example,b@corp.example"), Transport: okTransport, Message: good}},
		{"subject prefix with CRLF", policy.Input{Policy: &policy.Policy{Mode: policy.ModeRedirect, RedirectAddress: "pm@corp.example", SubjectPrefix: "x\r\nBcc: evil@x.test"}, Transport: okTransport, Message: good}},
		{"subject prefix too long", policy.Input{Policy: &policy.Policy{Mode: policy.ModeAllow, SubjectPrefix: strings.Repeat("x", 101)}, Transport: okTransport, Message: good}},
		{"no envelope recipients", policy.Input{Policy: allowPolicy("@corp.example"), Transport: okTransport, Message: msg()}},
		{"unparsable recipient among good ones", policy.Input{Policy: allowPolicy("@corp.example"), Transport: okTransport, Message: msg("real@corp.example", "garbage")}},
		{"recipient with display name", policy.Input{Policy: allowPolicy("@corp.example"), Transport: okTransport, Message: msg("Real <real@corp.example>")}},
		{"recipient with quoted local part", policy.Input{Policy: redirectPolicy("pm@corp.example"), Transport: okTransport, Message: msg(`"x@evil.test"@corp.example`)}},
		{"recipient with injected command", policy.Input{Policy: allowPolicy("@corp.example"), Transport: okTransport, Message: msg("a@corp.example\r\nRCPT TO:<x@evil.test>")}},
		{"empty recipient string", policy.Input{Policy: redirectPolicy("pm@corp.example"), Transport: okTransport, Message: msg("")}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			plan, err := policy.Evaluate(tc.in)
			if !errors.Is(err, policy.ErrBlocked) {
				t.Fatalf("err = %v, want ErrBlocked", err)
			}
			var be *policy.BlockedError
			if !errors.As(err, &be) || be.Reason == "" {
				t.Errorf("a block must say why: %v", err)
			}
			if len(plan.Outbound) != 0 || len(plan.Decisions) != 0 {
				t.Errorf("a blocked plan must be empty: %+v", plan)
			}
		})
	}
}

func TestEvaluateRelease(t *testing.T) {
	pol := allowPolicy("@corp.example", "qa@partner.test")
	in := func(targets ...string) policy.ReleaseInput {
		return policy.ReleaseInput{Policy: pol, Transport: okTransport, Message: msg("real@corp.example"), Targets: targets}
	}
	plan, err := policy.EvaluateRelease(in("a@corp.example", "QA@partner.test", "a@corp.example"))
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Outbound) != 1 || plan.Outbound[0].Kind() != policy.KindRelease ||
		!reflect.DeepEqual(plan.Outbound[0].Recipients(), []string{"a@corp.example", "QA@partner.test"}) {
		t.Fatalf("plan %+v", plan.Outbound)
	}
	data := string(plan.Outbound[0].Data())
	if strings.Contains(strings.ToLower(data), "bcc:") || strings.Contains(data, "X-Original") {
		t.Errorf("release must strip Bcc and add nothing:\n%s", data)
	}

	t.Run("works whatever the mode", func(t *testing.T) {
		i := in("a@corp.example")
		i.Policy = &policy.Policy{Mode: policy.ModeCapture, Allowlist: []string{"@corp.example"}}
		if _, err := policy.EvaluateRelease(i); err != nil {
			t.Errorf("release in capture mode: %v", err)
		}
	})

	blockedCases := map[string]policy.ReleaseInput{
		"target not on the allowlist":    in("a@corp.example", "x@evil.test"),
		"only target refused":            in("x@evil.test"),
		"subdomain of an allowed domain": in("a@sub.corp.example"),
		"no targets":                     in(),
		"invalid target":                 in("not-an-address"),
		"target with display name":       in("A <a@corp.example>"),
		"too many targets":               in(manyTargets(policy.MaxReleaseTargets + 1)...),
		"no policy":                      {Transport: okTransport, Message: msg("a@corp.example"), Targets: []string{"a@corp.example"}},
		"empty allowlist":                {Policy: allowPolicy(), Transport: okTransport, Message: msg("a@corp.example"), Targets: []string{"a@corp.example"}},
		"invalid allowlist":              {Policy: allowPolicy("*"), Transport: okTransport, Message: msg("a@corp.example"), Targets: []string{"a@corp.example"}},
		"no transport":                   {Policy: pol, Message: msg("a@corp.example"), Targets: []string{"a@corp.example"}},
		"transport disabled":             {Policy: pol, Transport: policy.TransportState{Present: true}, Message: msg("a@corp.example"), Targets: []string{"a@corp.example"}},
	}
	for name, rin := range blockedCases {
		t.Run("blocked/"+name, func(t *testing.T) {
			plan, err := policy.EvaluateRelease(rin)
			if !errors.Is(err, policy.ErrBlocked) || len(plan.Outbound) != 0 {
				t.Errorf("plan %+v err %v, want blocked and empty", plan, err)
			}
		})
	}
}

func manyTargets(n int) []string {
	out := make([]string, n)
	for i := range out {
		out[i] = fmt.Sprintf("u%d@corp.example", i)
	}
	return out
}

func TestOutboundIsCopiedOnAccess(t *testing.T) {
	plan, _ := policy.Evaluate(policy.Input{Policy: allowPolicy("@corp.example"), Transport: okTransport, Message: msg("a@corp.example")})
	o := plan.Outbound[0]
	o.Recipients()[0] = "evil@evil.test"
	o.Covers()[0] = "evil@evil.test"
	o.Data()[0] = 'X'
	if o.Recipients()[0] != "a@corp.example" || o.Covers()[0] != "a@corp.example" || o.Data()[0] == 'X' {
		t.Error("mutating an accessor result changed the Outbound")
	}
}

func TestRestrict(t *testing.T) {
	plan, _ := policy.Evaluate(policy.Input{
		Policy: redirectPolicy("pm@corp.example", "a@corp.example", "b@corp.example"), Transport: okTransport,
		Message: msg("a@corp.example", "b@corp.example", "x@y.test", "z@y.test"),
	})
	direct, redirected := plan.Outbound[0], plan.Outbound[1]

	left, ok := direct.Restrict(func(r string) bool { return r != "a@corp.example" })
	if !ok || !reflect.DeepEqual(left.Recipients(), []string{"b@corp.example"}) {
		t.Errorf("restrict direct: %v %v", left.Recipients(), ok)
	}
	if _, ok := direct.Restrict(func(string) bool { return false }); ok {
		t.Error("nothing left must report false")
	}
	left, ok = redirected.Restrict(func(r string) bool { return r == "z@y.test" })
	if !ok || !reflect.DeepEqual(left.Recipients(), []string{"pm@corp.example"}) || !reflect.DeepEqual(left.Covers(), []string{"z@y.test"}) {
		t.Errorf("restrict redirected: %v %v %v", left.Recipients(), left.Covers(), ok)
	}
	// Restrict can only narrow: it never adds a recipient.
	for _, o := range plan.Outbound {
		r, _ := o.Restrict(func(string) bool { return true })
		if !reflect.DeepEqual(r.Recipients(), o.Recipients()) {
			t.Errorf("restrict(all) changed recipients %v -> %v", o.Recipients(), r.Recipients())
		}
	}
}

func TestModeValid(t *testing.T) {
	for m, want := range map[policy.Mode]bool{"capture": true, "allow": true, "redirect": true, "": false, "Allow": false, "x": false} {
		if m.Valid() != want {
			t.Errorf("Mode(%q).Valid() = %v", m, !want)
		}
	}
}

func TestBlockedErrorMessage(t *testing.T) {
	err := error(&policy.BlockedError{Reason: "because"})
	if err.Error() != "delivery blocked: because" || !errors.Is(err, policy.ErrBlocked) || errors.Is(err, errors.New("other")) {
		t.Errorf("unexpected error behaviour: %v", err)
	}
}
