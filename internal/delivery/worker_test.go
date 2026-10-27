package delivery_test

import (
	"log/slog"
	"strings"
	"testing"

	"github.com/emersion/go-smtp"

	"github.com/sandpost-mail/sandpost/internal/delivery"
	"github.com/sandpost-mail/sandpost/internal/testutil"
)

func TestAllowModeDeliversOnlyAllowlisted(t *testing.T) {
	e := newEnv(t, testutil.RelayOptions{})
	e.transport(delivery.TLSNone, true)
	e.policy("allow", "", "real@corp.example")
	mid := e.capture("real@corp.example", "stranger@elsewhere.test")

	args := e.jobArgs()
	if len(args) != 1 || args[0].MessageID != mid || args[0].Run != delivery.RunAuto {
		t.Fatalf("jobs %+v", args)
	}
	if err := e.run(args[0], false); err != nil {
		t.Fatal(err)
	}
	got := statuses(e.deliveries(mid))
	if got["real@corp.example"] != delivery.StatusSent || got["stranger@elsewhere.test"] != delivery.StatusSkipped {
		t.Errorf("statuses %v", got)
	}
	env := e.relay.Envelopes()
	if len(env) != 1 || strings.Join(env[0].To, ",") != "real@corp.example" {
		t.Fatalf("envelopes %+v", env)
	}
	if strings.Contains(strings.ToLower(string(env[0].Data)), "bcc:") || strings.Contains(string(env[0].Data), "hidden@elsewhere.test") {
		t.Errorf("Bcc leaked:\n%s", env[0].Data)
	}
	for _, a := range e.relay.RcptAttempts() {
		if a != "real@corp.example" {
			t.Errorf("the relay was asked about %s", a)
		}
	}
	// Running the job again (a retry) sends nothing twice.
	if err := e.run(args[0], false); err != nil {
		t.Fatal(err)
	}
	if len(e.relay.Envelopes()) != 1 {
		t.Errorf("a repeated run sent the message again: %d", len(e.relay.Envelopes()))
	}
}

func TestRedirectModeSplitsAndRewrites(t *testing.T) {
	e := newEnv(t, testutil.RelayOptions{})
	e.transport(delivery.TLSNone, true)
	e.policy("redirect", "pm@corp.example", "real@corp.example")
	mid := e.capture("real@corp.example", "stranger@elsewhere.test", "hidden@elsewhere.test")
	if err := e.run(e.jobArgs()[0], false); err != nil {
		t.Fatal(err)
	}
	rows := e.deliveries(mid)
	for _, d := range rows {
		if d.Status != delivery.StatusSent {
			t.Errorf("%+v", d)
		}
	}
	byRecipient := map[string]delivery.Delivery{}
	for _, d := range rows {
		byRecipient[d.Recipient] = d
	}
	if byRecipient["stranger@elsewhere.test"].EffectiveRecipient != "pm@corp.example" || byRecipient["stranger@elsewhere.test"].Kind != "redirect" ||
		byRecipient["real@corp.example"].EffectiveRecipient != "real@corp.example" {
		t.Errorf("rows %+v", rows)
	}
	env := e.relay.Envelopes()
	if len(env) != 2 {
		t.Fatalf("want 2 messages (real and redirected), got %d: %+v", len(env), env)
	}
	var direct, redirected testutil.Envelope
	for _, m := range env {
		if m.To[0] == "pm@corp.example" {
			redirected = m
		} else {
			direct = m
		}
	}
	if len(direct.To) != 1 || direct.To[0] != "real@corp.example" || strings.Contains(string(direct.Data), "X-Original") || strings.Contains(string(direct.Data), "[sandbox]") {
		t.Errorf("direct %+v\n%s", direct.To, direct.Data)
	}
	if len(redirected.To) != 1 || !strings.Contains(string(redirected.Data), "X-Original-To: ") || !strings.Contains(string(redirected.Data), "real@corp.example") ||
		!strings.Contains(string(redirected.Data), "Subject: [sandbox] Welcome\r\n") {
		t.Errorf("redirected %+v\n%s", redirected.To, redirected.Data)
	}
	for _, a := range e.relay.RcptAttempts() {
		if a != "real@corp.example" && a != "pm@corp.example" {
			t.Errorf("a recipient outside the allowlist and the redirect address was contacted: %s", a)
		}
	}
}

func TestCaptureModeQueuesNothing(t *testing.T) {
	e := newEnv(t, testutil.RelayOptions{})
	e.transport(delivery.TLSNone, true)
	e.capture("real@corp.example") // no policy at all
	e.policy("capture", "", "real@corp.example")
	e.capture("real@corp.example")
	if n := len(e.jobArgs()); n != 0 {
		t.Errorf("%d job(s) queued in capture mode", n)
	}
	if e.relay.Connections() != 0 {
		t.Error("the relay was contacted")
	}
}

// Every misconfiguration blocks the run: nothing is sent, the relay is not
// even contacted, and the reason is recorded.
func TestMisconfigurationBlocksEverything(t *testing.T) {
	enabledAllow := func(e *env) {
		e.transport(delivery.TLSNone, true)
		e.policy("allow", "", "@corp.example")
	}
	enabledRedirect := func(e *env) {
		e.transport(delivery.TLSNone, true)
		e.policy("redirect", "pm@corp.example", "@corp.example")
	}
	tests := []struct {
		name  string
		setup func(e *env, mid int64)
		want  string
	}{
		{"transport missing", func(e *env, _ int64) { e.policy("allow", "", "@corp.example") }, "no transport"},
		{"transport disabled", func(e *env, _ int64) { e.transport(delivery.TLSNone, false); e.policy("allow", "", "@corp.example") }, "disabled"},
		{"transport deleted after the policy was set", func(e *env, _ int64) {
			enabledAllow(e)
			e.exec(`DELETE FROM transports`)
		}, "no transport"},
		{"allowlist corrupted in the database", func(e *env, _ int64) {
			enabledAllow(e)
			e.exec(`UPDATE delivery_policies SET allowlist = '["*"]'::jsonb`)
		}, "allowlist"},
		{"allowlist holds non strings", func(e *env, _ int64) {
			enabledAllow(e)
			e.exec(`UPDATE delivery_policies SET allowlist = '[1, {"a": 2}]'::jsonb`)
		}, "allowlist"},
		{"redirect address removed in the database", func(e *env, _ int64) {
			enabledRedirect(e)
			e.exec(`UPDATE delivery_policies SET redirect_address = ''`)
		}, "redirect"},
		{"redirect address invalid in the database", func(e *env, _ int64) {
			enabledRedirect(e)
			e.exec(`UPDATE delivery_policies SET redirect_address = 'pm@corp.example, other@evil.test'`)
		}, "redirect"},
		{"subject prefix with header injection", func(e *env, _ int64) {
			enabledRedirect(e)
			e.exec(`UPDATE delivery_policies SET subject_prefix = E'x\r\nBcc: evil@evil.test'`)
		}, "prefix"},
		{"transport sender invalid in the database", func(e *env, _ int64) {
			enabledAllow(e)
			e.exec(`UPDATE transports SET sender = 'not an address'`)
		}, "sender"},
		{"stored password undecryptable", func(e *env, _ int64) {
			enabledAllow(e)
			e.exec(`UPDATE transports SET username = 'u', tls_mode = 'starttls',
				secret_enc = '\x0102030405060708090a0b0c0d0e0f1011121314151617181920'::bytea`)
		}, "decrypt"},
		{"envelope recipient unparsable", func(e *env, mid int64) {
			enabledRedirect(e)
			e.exec(`INSERT INTO message_recipients (message_id, kind, address) VALUES ($1, 'envelope', 'x@evil.test>, RCPT TO:<y@evil.test')`, mid)
		}, "recipient"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			e := newEnv(t, testutil.RelayOptions{})
			// The job exists because the policy was not capture when the message arrived.
			e.exec(`INSERT INTO delivery_policies (project_id, mode, allowlist) VALUES ($1, 'allow', '[]')`, e.pid)
			mid := e.capture("a@corp.example")
			e.exec(`DELETE FROM delivery_policies`)
			tc.setup(e, mid)

			if err := e.run(e.auto(mid), false); err != nil {
				t.Fatalf("a blocked run must end without a retry request: %v", err)
			}
			if e.relay.Connections() != 0 || len(e.relay.Envelopes()) != 0 {
				t.Fatalf("outbound traffic: %d connection(s), %d message(s)", e.relay.Connections(), len(e.relay.Envelopes()))
			}
			rows := e.deliveries(mid)
			blocked := false
			for _, d := range rows {
				if d.Status == delivery.StatusSent || d.Status == delivery.StatusPending {
					t.Errorf("row %+v", d)
				}
				if d.Status == delivery.StatusBlocked && strings.Contains(strings.ToLower(d.Error), tc.want) {
					blocked = true
				}
			}
			if !blocked {
				t.Errorf("no blocked row mentioning %q: %+v", tc.want, rows)
			}
			// Running it again changes nothing.
			if err := e.run(e.auto(mid), true); err != nil || e.relay.Connections() != 0 {
				t.Errorf("second run: %v, %d connections", err, e.relay.Connections())
			}
		})
	}
}

func TestPasswordForAnotherSecretKeyIsNotUsed(t *testing.T) {
	e := newEnv(t, testutil.RelayOptions{TLS: "starttls", User: "u", Pass: "relay-pass"})
	in := delivery.TransportInput{Kind: delivery.KindSMTP, Host: e.relay.Host, Port: e.relay.Port, TLSMode: delivery.TLSStartTLS, Username: "u"}
	pw := "relay-pass"
	in.Secret = &pw
	if _, err := e.svc.PutTransport(e.ctx, e.scope, in); err != nil {
		t.Fatal(err)
	}
	in.Enabled, in.Secret = true, nil
	if _, err := e.svc.PutTransport(e.ctx, e.scope, in); err != nil {
		t.Fatal(err)
	}
	e.policy("allow", "", "@corp.example")
	mid := e.capture("a@corp.example")

	// A worker started with another SECRET_KEY (a rotated key) can not open the password.
	other := delivery.NewWorker(e.pool, "ffffffffffffffffffffffffffffffff", &delivery.SMTPRelay{}, nil)
	if err := other.Process(e.ctx, e.auto(mid), false); err != nil {
		t.Fatal(err)
	}
	if e.relay.Connections() != 0 {
		t.Error("the relay must not be contacted with an undecryptable password")
	}
	for _, d := range e.deliveries(mid) {
		if d.Status != delivery.StatusBlocked || !strings.Contains(d.Error, "decrypted") {
			t.Errorf("row %+v", d)
		}
	}
}

func TestPolicyChangeBetweenAttemptsIsHonoured(t *testing.T) {
	flaky := testutil.RelayOptions{RejectRcpt: func(string) *smtp.SMTPError {
		return &smtp.SMTPError{Code: 451, EnhancedCode: smtp.EnhancedCode{4, 3, 0}, Message: "try later"}
	}}
	t.Run("back to capture", func(t *testing.T) {
		e := newEnv(t, flaky)
		e.transport(delivery.TLSNone, true)
		e.policy("allow", "", "@corp.example")
		mid := e.capture("a@corp.example")
		if err := e.run(e.auto(mid), false); err == nil {
			t.Fatal("a transient failure must ask for a retry")
		}
		before := len(e.relay.RcptAttempts())
		e.policy("capture", "", "@corp.example")
		if err := e.run(e.auto(mid), false); err != nil {
			t.Fatal(err)
		}
		if after := len(e.relay.RcptAttempts()); after != before {
			t.Errorf("a queued retry contacted the relay after the project went back to capture")
		}
	})
	t.Run("transport disabled", func(t *testing.T) {
		e := newEnv(t, flaky)
		e.transport(delivery.TLSNone, true)
		e.policy("allow", "", "@corp.example")
		mid := e.capture("a@corp.example")
		_ = e.run(e.auto(mid), false)
		before := e.relay.Connections()
		e.transport(delivery.TLSNone, false) // replacing the transport disables it
		if err := e.run(e.auto(mid), false); err != nil {
			t.Fatal(err)
		}
		if e.relay.Connections() != before {
			t.Error("the relay was contacted with a disabled transport")
		}
		if st := statuses(e.deliveries(mid)); st["a@corp.example"] != delivery.StatusBlocked {
			t.Errorf("statuses %v", st)
		}
	})
	t.Run("allowlist narrowed", func(t *testing.T) {
		e := newEnv(t, flaky)
		e.transport(delivery.TLSNone, true)
		e.policy("allow", "", "@corp.example")
		mid := e.capture("a@corp.example")
		_ = e.run(e.auto(mid), false)
		before := len(e.relay.RcptAttempts())
		e.policy("allow", "", "@other.example")
		if err := e.run(e.auto(mid), false); err != nil {
			t.Fatal(err)
		}
		if len(e.relay.RcptAttempts()) != before {
			t.Error("a recipient removed from the allowlist was still attempted")
		}
		if st := statuses(e.deliveries(mid)); st["a@corp.example"] != delivery.StatusSkipped {
			t.Errorf("statuses %v", st)
		}
	})
}

func TestRetriesAreBoundedAndPerRecipient(t *testing.T) {
	attempts := map[string]int{}
	e := newEnv(t, testutil.RelayOptions{RejectRcpt: func(a string) *smtp.SMTPError {
		attempts[a]++
		switch a {
		case "gone@corp.example":
			return &smtp.SMTPError{Code: 550, EnhancedCode: smtp.EnhancedCode{5, 1, 1}, Message: "no such user"}
		case "busy@corp.example":
			return &smtp.SMTPError{Code: 450, EnhancedCode: smtp.EnhancedCode{4, 2, 0}, Message: "busy"}
		}
		return nil
	}})
	e.transport(delivery.TLSNone, true)
	e.policy("allow", "", "@corp.example")
	mid := e.capture("ok@corp.example", "gone@corp.example", "busy@corp.example")
	args := e.auto(mid)

	// Attempt 1: one sent, one permanently failed, one to retry.
	if err := e.run(args, false); err == nil {
		t.Fatal("a transient failure must ask for a retry")
	}
	st := statuses(e.deliveries(mid))
	if st["ok@corp.example"] != delivery.StatusSent || st["gone@corp.example"] != delivery.StatusFailed || st["busy@corp.example"] != delivery.StatusPending {
		t.Fatalf("statuses %v", st)
	}
	// Attempt 2: only the pending recipient is tried again.
	if err := e.run(args, false); err == nil {
		t.Fatal("still transient")
	}
	if attempts["ok@corp.example"] != 1 || attempts["gone@corp.example"] != 1 || attempts["busy@corp.example"] != 2 {
		t.Errorf("attempts %v: finished recipients must not be retried", attempts)
	}
	// The last attempt gives up and says so.
	if err := e.run(args, true); err != nil {
		t.Fatalf("the last attempt must not ask for another retry: %v", err)
	}
	rows := e.deliveries(mid)
	for _, d := range rows {
		if d.Recipient == "busy@corp.example" && (d.Status != delivery.StatusFailed || !strings.Contains(d.Error, "gave up") || d.Attempts != 3) {
			t.Errorf("busy: %+v", d)
		}
	}
}

func TestRelayTextIsSanitisedInDeliveryErrors(t *testing.T) {
	e := newEnv(t, testutil.RelayOptions{RejectRcpt: func(string) *smtp.SMTPError {
		return &smtp.SMTPError{Code: 550, EnhancedCode: smtp.EnhancedCode{5, 1, 1}, Message: "nope\t" + strings.Repeat("x", 1000)}
	}})
	e.transport(delivery.TLSNone, true)
	e.policy("allow", "", "@corp.example")
	mid := e.capture("a@corp.example")
	_ = e.run(e.auto(mid), false)
	d := e.deliveries(mid)[0]
	if len([]rune(d.Error)) > 300 || strings.ContainsAny(d.Error, "\r\n\t\x00") || d.Status != delivery.StatusFailed {
		t.Errorf("error text %q", d.Error)
	}
}

// A job for a message of another project finds nothing: the load is scoped.
func TestWorkerIsScopedToTheProject(t *testing.T) {
	e := newEnv(t, testutil.RelayOptions{})
	e.transport(delivery.TLSNone, true)
	e.policy("allow", "", "@corp.example")
	otherPID := testutil.Project(t, e.pool, "other")
	foreign := e.captureIn(e.ing, otherPID, "victim@corp.example")

	if err := e.run(delivery.DeliverArgs{ProjectID: e.pid, MessageID: foreign, Run: delivery.RunAuto}, false); err != nil {
		t.Fatal(err)
	}
	if e.relay.Connections() != 0 {
		t.Error("a message of another project was delivered through this project's transport")
	}
	var n int
	if err := e.pool.QueryRow(e.ctx, "SELECT count(*) FROM deliveries").Scan(&n); err != nil || n != 0 {
		t.Errorf("deliveries rows %d %v", n, err)
	}
	// And a release with that id is a plain not found.
	_, err := e.svc.Release(e.ctx, e.scope, foreign, []string{"victim@corp.example"})
	mustErrIs(t, err, messagesErrNotFound)
	if _, err := e.svc.ListDeliveries(e.ctx, e.scope, foreign); err == nil {
		t.Error("listing deliveries of a foreign message must fail")
	}
	// A deleted message is not an error for the job.
	if err := e.run(delivery.DeliverArgs{ProjectID: e.pid, MessageID: 999999, Run: delivery.RunAuto}, false); err != nil {
		t.Errorf("deleted message: %v", err)
	}
}

func TestUsesTheTransportOfItsOwnProject(t *testing.T) {
	e := newEnv(t, testutil.RelayOptions{})
	// Project "other" has an enabled transport and a permissive policy, ours has none.
	otherPID := testutil.Project(t, e.pool, "other")
	otherScope := authzTrusted(otherPID)
	if _, err := e.svc.PutTransport(e.ctx, otherScope, delivery.TransportInput{Kind: delivery.KindSMTP, Host: e.relay.Host, Port: e.relay.Port, TLSMode: delivery.TLSNone}); err != nil {
		t.Fatal(err)
	}
	e.exec(`UPDATE transports SET enabled = true`)
	if _, err := e.svc.PutPolicy(e.ctx, otherScope, delivery.PolicyInput{Mode: "allow", Allowlist: []string{"@corp.example"}}); err != nil {
		t.Fatal(err)
	}
	e.exec(`INSERT INTO delivery_policies (project_id, mode, allowlist) VALUES ($1, 'allow', '["@corp.example"]')`, e.pid)
	mid := e.capture("a@corp.example")
	if err := e.run(e.auto(mid), false); err != nil {
		t.Fatal(err)
	}
	if e.relay.Connections() != 0 {
		t.Error("another project's transport was used")
	}
}

// Nothing in the logs may contain the relay password, the message content or
// a recipient list, whatever path a delivery takes.
func TestWorkerLogsHoldNoSecrets(t *testing.T) {
	e := newEnv(t, testutil.RelayOptions{User: "u", Pass: "relay-log-secret"})
	var buf strings.Builder
	log := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	worker := delivery.NewWorker(e.pool, testKey, &delivery.SMTPRelay{}, log)

	in := delivery.TransportInput{
		Kind: delivery.KindSMTP, Host: e.relay.Host, Port: e.relay.Port, TLSMode: delivery.TLSStartTLS,
		Username: "u", Secret: ptr("relay-log-secret"),
	}
	if _, err := e.svc.PutTransport(e.ctx, e.scope, in); err != nil {
		t.Fatal(err)
	}
	in.Secret, in.Enabled = nil, true
	if _, err := e.svc.PutTransport(e.ctx, e.scope, in); err != nil {
		t.Fatal(err)
	}
	e.policy("allow", "", "@corp.example")
	mid := e.capture("a@corp.example") // the relay offers no STARTTLS: a permanent failure path
	if err := worker.Process(e.ctx, e.auto(mid), true); err != nil {
		t.Fatal(err)
	}
	// And a blocked path.
	e.exec(`UPDATE delivery_policies SET allowlist = '["*"]'::jsonb`)
	if err := worker.Process(e.ctx, delivery.DeliverArgs{ProjectID: e.pid, MessageID: mid, Run: "release:x", Targets: []string{"a@corp.example"}}, false); err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{"relay-log-secret", "Hello", "hidden@elsewhere.test"} {
		if strings.Contains(buf.String(), secret) {
			t.Errorf("%q appears in the logs:\n%s", secret, buf.String())
		}
	}
	if !strings.Contains(buf.String(), "delivery blocked") {
		t.Errorf("the block was not logged:\n%s", buf.String())
	}
}
