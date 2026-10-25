// Package policy decides what may leave Sandpost after a message was
// captured. It is a set of pure functions: no database, no network, no clock.
// The delivery package feeds it stored state and sends only what it returns.
//
// The engine fails closed. Any misconfiguration or ambiguity (unknown mode,
// missing or disabled transport, a malformed allowlist entry, redirect
// address or recipient, an empty recipient list) produces a *BlockedError and
// no Outbound at all, so the caller has nothing it could send. The only way
// to obtain an Outbound is through Evaluate or EvaluateRelease, its fields
// are private, and the SMTP client accepts nothing else.
package policy

import (
	"errors"
	"fmt"
	"strings"
)

// Mode is the delivery mode of a project.
type Mode string

// Modes. The zero value (and anything unknown) is not a valid mode.
const (
	// ModeCapture stores mail and never sends it. It is the default.
	ModeCapture Mode = "capture"
	// ModeAllow delivers to allowlisted recipients, drops the others.
	ModeAllow Mode = "allow"
	// ModeRedirect delivers to allowlisted recipients and redirects the others
	// to one address.
	ModeRedirect Mode = "redirect"
)

// Valid reports whether m is a known mode.
func (m Mode) Valid() bool { return m == ModeCapture || m == ModeAllow || m == ModeRedirect }

// Policy is the stored policy of a project.
type Policy struct {
	Mode            Mode
	Allowlist       []string
	RedirectAddress string
	SubjectPrefix   string
}

// TransportState is what the engine needs to know about the project's relay.
// The zero value means "no transport" and blocks sending.
type TransportState struct {
	Present bool
	Enabled bool
	// Sender is the envelope sender (MAIL FROM) to use, empty for the null
	// sender. The original envelope sender is not used, bounces must not
	// reach a real mailbox.
	Sender string
}

// Message is the captured message as far as the engine cares.
type Message struct {
	Raw []byte
	// EnvelopeTo is the SMTP RCPT TO list: who the sender asked to reach.
	EnvelopeTo []string
	// To and Cc are the header addresses, only used for the X-Original-*
	// headers of redirected mail.
	To, Cc []string
}

// Input is everything Evaluate needs. Policy nil means no policy is stored,
// which is capture.
type Input struct {
	Policy    *Policy
	Transport TransportState
	Message   Message
}

// ReleaseInput is the input of EvaluateRelease: a manual release of one
// message to explicit targets.
type ReleaseInput struct {
	Policy    *Policy
	Transport TransportState
	Message   Message
	Targets   []string
}

// MaxReleaseTargets bounds one manual release.
const MaxReleaseTargets = 50

// Action is what happens to one recipient.
type Action string

// Actions.
const (
	// ActionDeliver sends to the real recipient.
	ActionDeliver Action = "deliver"
	// ActionRedirect sends to the redirect address instead.
	ActionRedirect Action = "redirect"
	// ActionSkip sends nothing for this recipient.
	ActionSkip Action = "skip"
)

// Decision is the outcome for one original recipient.
type Decision struct {
	Recipient string
	// Effective is where mail actually goes: the recipient itself, the
	// redirect address, or empty for a skip.
	Effective string
	Action    Action
	Reason    string
}

// Kind tells the purpose of an outbound message.
type Kind string

// Kinds of outbound messages.
const (
	KindAllowed  Kind = "allowed"
	KindRedirect Kind = "redirect"
	KindRelease  Kind = "release"
)

// Outbound is one message the engine allows to be sent: an envelope, the
// rewritten bytes and the original recipients it stands for. It can only be
// built by this package.
type Outbound struct {
	kind       Kind
	sender     string
	envelopeTo []string
	covers     []string
	data       []byte
}

// Kind returns the kind of the message.
func (o Outbound) Kind() Kind { return o.kind }

// Sender returns the envelope sender, empty for the null sender.
func (o Outbound) Sender() string { return o.sender }

// Recipients returns a copy of the envelope recipients.
func (o Outbound) Recipients() []string { return append([]string(nil), o.envelopeTo...) }

// Covers returns a copy of the original recipients this message is for.
func (o Outbound) Covers() []string { return append([]string(nil), o.covers...) }

// Data returns a copy of the message bytes to send.
func (o Outbound) Data() []byte { return append([]byte(nil), o.data...) }

// Restrict returns the part of o still needed, given which original
// recipients keep(original) says are not done yet. The second result is false
// when nothing is left. It never adds recipients.
func (o Outbound) Restrict(keep func(original string) bool) (Outbound, bool) {
	covers := make([]string, 0, len(o.covers))
	for _, c := range o.covers {
		if keep(c) {
			covers = append(covers, c)
		}
	}
	if len(covers) == 0 {
		return Outbound{}, false
	}
	out := o
	out.covers = covers
	if o.kind != KindRedirect { // allowed and release: envelope equals the covered recipients
		out.envelopeTo = append([]string(nil), covers...)
	}
	return out, true
}

// Plan is the result of an evaluation.
type Plan struct {
	// Decisions has one entry per original recipient (empty in capture mode).
	Decisions []Decision
	// Outbound holds at most one message for allowed recipients and one for
	// the redirected ones, never mixed.
	Outbound []Outbound
}

// ErrBlocked is matched by every *BlockedError.
var ErrBlocked = errors.New("delivery blocked")

// BlockedError says why nothing may be sent.
type BlockedError struct{ Reason string }

func (e *BlockedError) Error() string { return "delivery blocked: " + e.Reason }

// Is makes errors.Is(err, ErrBlocked) true.
func (e *BlockedError) Is(target error) bool { return target == ErrBlocked }

func blocked(format string, args ...any) error {
	return &BlockedError{Reason: fmt.Sprintf(format, args...)}
}

// Evaluate applies the project's policy to a captured message.
//
// Capture (or no policy) returns an empty plan. For allow and redirect, every
// envelope recipient is checked against the allowlist: a match is delivered to
// the real address, the rest are skipped (allow) or replaced by the redirect
// address (redirect). Allowed and redirected recipients go in separate
// outbound messages so neither side sees the other's headers.
func Evaluate(in Input) (Plan, error) {
	p := in.Policy
	if p == nil || p.Mode == ModeCapture {
		return Plan{}, nil
	}
	if p.Mode != ModeAllow && p.Mode != ModeRedirect {
		return Plan{}, blocked("unknown policy mode %q", p.Mode)
	}
	sender, err := checkSending(in.Transport)
	if err != nil {
		return Plan{}, err
	}
	list, err := parseAllowlist(p.Allowlist)
	if err != nil {
		return Plan{}, blocked("allowlist is invalid: %v", err)
	}
	if !validPrefix(p.SubjectPrefix) {
		return Plan{}, blocked("subject prefix is invalid")
	}
	var redirect Address
	if p.Mode == ModeRedirect {
		if redirect, err = ParseAddress(p.RedirectAddress); err != nil {
			return Plan{}, blocked("redirect address is missing or invalid")
		}
	}
	rcpts, err := parseRecipients(in.Message.EnvelopeTo)
	if err != nil {
		return Plan{}, err
	}
	return split(in.Message, p, list, redirect, sender, rcpts), nil
}

func split(msg Message, p *Policy, list []entry, redirect Address, sender string, rcpts []Address) Plan {
	var plan Plan
	var direct, redirected []string
	for _, a := range rcpts {
		switch {
		case allowed(list, a):
			direct = append(direct, a.String())
			plan.Decisions = append(plan.Decisions, Decision{
				Recipient: a.String(), Effective: a.String(), Action: ActionDeliver, Reason: "on the allowlist",
			})
		case p.Mode == ModeRedirect:
			redirected = append(redirected, a.String())
			plan.Decisions = append(plan.Decisions, Decision{
				Recipient: a.String(), Effective: redirect.String(), Action: ActionRedirect, Reason: "not on the allowlist",
			})
		default:
			plan.Decisions = append(plan.Decisions, Decision{
				Recipient: a.String(), Action: ActionSkip, Reason: "not on the allowlist",
			})
		}
	}
	if len(direct) > 0 {
		plan.Outbound = append(plan.Outbound, Outbound{
			kind: KindAllowed, sender: sender, envelopeTo: direct, covers: append([]string(nil), direct...),
			data: rewriteHeaders(msg.Raw, headerEdit{}),
		})
	}
	if len(redirected) > 0 {
		plan.Outbound = append(plan.Outbound, Outbound{
			kind: KindRedirect, sender: sender, envelopeTo: []string{redirect.String()}, covers: redirected,
			data: rewriteHeaders(msg.Raw, headerEdit{
				subjectPrefix: strings.TrimSpace(p.SubjectPrefix),
				originalTo:    msg.To, originalCc: msg.Cc,
			}),
		})
	}
	return plan
}

// EvaluateRelease checks a manual release. It needs a stored policy (its
// allowlist is the gate, whatever the mode is), an enabled transport, and
// every target must be on the allowlist: one refused target blocks the whole
// release. The message is sent as captured, minus Bcc and X-Original-*.
func EvaluateRelease(in ReleaseInput) (Plan, error) {
	p := in.Policy
	if p == nil {
		return Plan{}, blocked("no delivery policy is configured")
	}
	sender, err := checkSending(in.Transport)
	if err != nil {
		return Plan{}, err
	}
	list, err := parseAllowlist(p.Allowlist)
	if err != nil {
		return Plan{}, blocked("allowlist is invalid: %v", err)
	}
	if len(in.Targets) == 0 || len(in.Targets) > MaxReleaseTargets {
		return Plan{}, blocked("a release needs between 1 and %d targets", MaxReleaseTargets)
	}
	targets, err := parseRecipients(in.Targets)
	if err != nil {
		return Plan{}, err
	}
	var plan Plan
	envelope := make([]string, 0, len(targets))
	for _, a := range targets {
		if !allowed(list, a) {
			return Plan{}, blocked("%s is not on the allowlist", a.String())
		}
		envelope = append(envelope, a.String())
		plan.Decisions = append(plan.Decisions, Decision{
			Recipient: a.String(), Effective: a.String(), Action: ActionDeliver, Reason: "released manually",
		})
	}
	plan.Outbound = []Outbound{{
		kind: KindRelease, sender: sender, envelopeTo: envelope, covers: append([]string(nil), envelope...),
		data: rewriteHeaders(in.Message.Raw, headerEdit{}),
	}}
	return plan, nil
}

// checkSending verifies the transport and returns the validated envelope sender.
func checkSending(t TransportState) (string, error) {
	switch {
	case !t.Present:
		return "", blocked("no transport is configured")
	case !t.Enabled:
		return "", blocked("the transport is disabled")
	}
	if t.Sender == "" {
		return "", nil
	}
	a, err := ParseAddress(t.Sender)
	if err != nil {
		return "", blocked("the transport sender address is invalid")
	}
	return a.String(), nil
}

// parseRecipients validates every address and removes duplicates (case
// insensitive). One bad address blocks everything: a recipient we can not read
// is one we can not classify.
func parseRecipients(raw []string) ([]Address, error) {
	if len(raw) == 0 {
		return nil, blocked("the message has no envelope recipients")
	}
	seen := make(map[string]bool, len(raw))
	out := make([]Address, 0, len(raw))
	for _, r := range raw {
		a, err := ParseAddress(r)
		if err != nil {
			return nil, blocked("a recipient address is invalid")
		}
		key := strings.ToLower(a.String())
		if !seen[key] {
			seen[key] = true
			out = append(out, a)
		}
	}
	return out, nil
}
