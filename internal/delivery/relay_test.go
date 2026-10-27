package delivery_test

import (
	"context"
	"crypto/tls"
	"fmt"
	"log/slog"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/emersion/go-smtp"

	"github.com/sandpost-mail/sandpost/internal/delivery"
	"github.com/sandpost-mail/sandpost/internal/policy"
	"github.com/sandpost-mail/sandpost/internal/testutil"
)

const rawMail = "From: App <app@example.test>\r\nTo: a@corp.example\r\nSubject: hello\r\n\r\nbody\r\n"

// outbound builds a vetted Outbound the only way possible: through the engine.
func outbound(t *testing.T, sender string, rcpts ...string) policy.Outbound {
	t.Helper()
	plan, err := policy.Evaluate(policy.Input{
		Policy:    &policy.Policy{Mode: policy.ModeAllow, Allowlist: []string{"@corp.example"}},
		Transport: policy.TransportState{Present: true, Enabled: true, Sender: sender},
		Message:   policy.Message{Raw: []byte(rawMail), EnvelopeTo: rcpts},
	})
	if err != nil || len(plan.Outbound) != 1 {
		t.Fatalf("plan %+v err %v", plan, err)
	}
	return plan.Outbound[0]
}

func cfgFor(r *testutil.FakeRelay, mode string) delivery.RelayConfig {
	return delivery.RelayConfig{Host: r.Host, Port: r.Port, TLSMode: mode}
}

func relayFor(r *testutil.FakeRelay) *delivery.SMTPRelay {
	if r.Roots == nil {
		return &delivery.SMTPRelay{}
	}
	return &delivery.SMTPRelay{TLSConfig: &tls.Config{RootCAs: r.Roots, MinVersion: tls.VersionTLS12}}
}

func ctx(t *testing.T) context.Context {
	t.Helper()
	c, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	t.Cleanup(cancel)
	return c
}

func TestSendPlaintext(t *testing.T) {
	relay := testutil.StartRelay(t, testutil.RelayOptions{})
	res := (&delivery.SMTPRelay{}).Send(ctx(t), cfgFor(relay, delivery.TLSNone), outbound(t, "noreply@sandbox.example", "a@corp.example", "b@corp.example"))
	if len(res) != 2 || !res[0].Accepted() || !res[1].Accepted() {
		t.Fatalf("results %+v", res)
	}
	env := relay.Envelopes()
	if len(env) != 1 || env[0].From != "noreply@sandbox.example" || len(env[0].To) != 2 || env[0].TLS {
		t.Fatalf("envelopes %+v", env)
	}
	if !strings.Contains(string(env[0].Data), "Subject: hello") || !strings.HasSuffix(strings.TrimSpace(string(env[0].Data)), "body") {
		t.Errorf("data %q", env[0].Data)
	}
}

func TestSendNullSenderByDefault(t *testing.T) {
	relay := testutil.StartRelay(t, testutil.RelayOptions{})
	(&delivery.SMTPRelay{}).Send(ctx(t), cfgFor(relay, delivery.TLSNone), outbound(t, "", "a@corp.example"))
	if env := relay.Envelopes(); len(env) != 1 || env[0].From != "" {
		t.Errorf("want the null sender, got %+v", env)
	}
}

func TestSendStartTLSAndAuth(t *testing.T) {
	relay := testutil.StartRelay(t, testutil.RelayOptions{TLS: "starttls", User: "u", Pass: "p4ss"})
	cfg := cfgFor(relay, delivery.TLSStartTLS)
	cfg.Username, cfg.Password = "u", "p4ss"
	res := relayFor(relay).Send(ctx(t), cfg, outbound(t, "", "a@corp.example"))
	if !res[0].Accepted() {
		t.Fatalf("result %+v", res[0].Err)
	}
	env := relay.Envelopes()
	if len(env) != 1 || !env[0].TLS || !env[0].Authenticated {
		t.Errorf("envelope %+v, want TLS and AUTH", env)
	}
}

func TestSendImplicitTLS(t *testing.T) {
	relay := testutil.StartRelay(t, testutil.RelayOptions{TLS: "implicit"})
	res := relayFor(relay).Send(ctx(t), cfgFor(relay, delivery.TLSImplicit), outbound(t, "", "a@corp.example"))
	if !res[0].Accepted() {
		t.Fatalf("result %+v", res[0].Err)
	}
	if env := relay.Envelopes(); len(env) != 1 || !env[0].TLS {
		t.Errorf("envelope %+v", env)
	}
}

// The password must never reach a relay whose TLS can not be verified, and a
// relay that does not offer STARTTLS must not receive mail in starttls mode.
func TestSendRefusesInsecureTransport(t *testing.T) {
	t.Run("starttls not offered", func(t *testing.T) {
		relay := testutil.StartRelay(t, testutil.RelayOptions{User: "u", Pass: "p4ss"})
		cfg := cfgFor(relay, delivery.TLSStartTLS)
		cfg.Username, cfg.Password = "u", "p4ss"
		res := (&delivery.SMTPRelay{}).Send(ctx(t), cfg, outbound(t, "", "a@corp.example"))
		assertPermanent(t, res[0], "does not offer STARTTLS")
		assertNothingSent(t, relay)
	})
	t.Run("unknown certificate authority", func(t *testing.T) {
		relay := testutil.StartRelay(t, testutil.RelayOptions{TLS: "starttls", User: "u", Pass: "p4ss"})
		cfg := cfgFor(relay, delivery.TLSStartTLS)
		cfg.Username, cfg.Password = "u", "p4ss"
		res := (&delivery.SMTPRelay{}).Send(ctx(t), cfg, outbound(t, "", "a@corp.example")) // system roots only
		assertPermanent(t, res[0], "")
		assertNothingSent(t, relay)
	})
	t.Run("certificate for another host name", func(t *testing.T) {
		relay := testutil.StartRelay(t, testutil.RelayOptions{TLS: "starttls", User: "u", Pass: "p4ss", CertNames: []string{"other.test"}})
		cfg := cfgFor(relay, delivery.TLSStartTLS)
		cfg.Username, cfg.Password = "u", "p4ss"
		res := relayFor(relay).Send(ctx(t), cfg, outbound(t, "", "a@corp.example"))
		assertPermanent(t, res[0], "")
		assertNothingSent(t, relay)
	})
	t.Run("implicit tls with unknown authority", func(t *testing.T) {
		relay := testutil.StartRelay(t, testutil.RelayOptions{TLS: "implicit", User: "u", Pass: "p4ss"})
		cfg := cfgFor(relay, delivery.TLSImplicit)
		cfg.Username, cfg.Password = "u", "p4ss"
		res := (&delivery.SMTPRelay{}).Send(ctx(t), cfg, outbound(t, "", "a@corp.example"))
		assertPermanent(t, res[0], "")
		assertNothingSent(t, relay)
	})
	t.Run("credentials without TLS are refused before connecting", func(t *testing.T) {
		relay := testutil.StartRelay(t, testutil.RelayOptions{User: "u", Pass: "p4ss"})
		cfg := cfgFor(relay, delivery.TLSNone)
		cfg.Username, cfg.Password = "u", "p4ss"
		res := (&delivery.SMTPRelay{}).Send(ctx(t), cfg, outbound(t, "", "a@corp.example"))
		assertPermanent(t, res[0], "without TLS")
		if relay.Connections() != 0 {
			t.Errorf("the relay was contacted %d time(s)", relay.Connections())
		}
	})
	t.Run("unknown tls mode", func(t *testing.T) {
		relay := testutil.StartRelay(t, testutil.RelayOptions{})
		res := (&delivery.SMTPRelay{}).Send(ctx(t), cfgFor(relay, "weird"), outbound(t, "", "a@corp.example"))
		assertPermanent(t, res[0], "")
		if relay.Connections() != 0 {
			t.Error("the relay must not be contacted")
		}
	})
}

func assertPermanent(t *testing.T, r delivery.RcptResult, contains string) {
	t.Helper()
	if r.Err == nil || !r.Err.Permanent {
		t.Fatalf("want a permanent error, got %+v", r.Err)
	}
	if !strings.Contains(r.Err.Text, contains) {
		t.Errorf("error %q lacks %q", r.Err.Text, contains)
	}
}

func assertNothingSent(t *testing.T, r *testutil.FakeRelay) {
	t.Helper()
	if len(r.Envelopes()) != 0 || len(r.SeenPasswords()) != 0 {
		t.Errorf("mail %d or password %v reached the relay", len(r.Envelopes()), r.SeenPasswords())
	}
}

func TestSendAuthFailureIsPermanent(t *testing.T) {
	relay := testutil.StartRelay(t, testutil.RelayOptions{TLS: "starttls", User: "u", Pass: "right"})
	cfg := cfgFor(relay, delivery.TLSStartTLS)
	cfg.Username, cfg.Password = "u", "wrong-secret"
	res := relayFor(relay).Send(ctx(t), cfg, outbound(t, "", "a@corp.example"))
	assertPermanent(t, res[0], "535")
	if strings.Contains(res[0].Err.Text, "wrong-secret") {
		t.Error("the password is in the error text")
	}
	if len(relay.Envelopes()) != 0 {
		t.Error("no mail may be sent after a failed login")
	}
}

func TestSendPerRecipientResults(t *testing.T) {
	relay := testutil.StartRelay(t, testutil.RelayOptions{RejectRcpt: func(a string) *smtp.SMTPError {
		switch a {
		case "bad@corp.example":
			return &smtp.SMTPError{Code: 550, EnhancedCode: smtp.EnhancedCode{5, 1, 1}, Message: "no such user"}
		case "later@corp.example":
			return &smtp.SMTPError{Code: 450, EnhancedCode: smtp.EnhancedCode{4, 2, 0}, Message: "mailbox busy"}
		}
		return nil
	}})
	res := (&delivery.SMTPRelay{}).Send(ctx(t), cfgFor(relay, delivery.TLSNone),
		outbound(t, "", "ok@corp.example", "bad@corp.example", "later@corp.example"))
	if !res[0].Accepted() {
		t.Errorf("ok: %+v", res[0].Err)
	}
	if res[1].Err == nil || !res[1].Err.Permanent || res[1].Err.Code != 550 {
		t.Errorf("bad: %+v", res[1].Err)
	}
	if res[2].Err == nil || res[2].Err.Permanent || res[2].Err.Code != 450 {
		t.Errorf("later must be transient: %+v", res[2].Err)
	}
	env := relay.Envelopes()
	if len(env) != 1 || len(env[0].To) != 1 || env[0].To[0] != "ok@corp.example" {
		t.Errorf("only the accepted recipient may be in the delivered envelope: %+v", env)
	}
}

func TestSendAllRecipientsRejectedSendsNoData(t *testing.T) {
	relay := testutil.StartRelay(t, testutil.RelayOptions{RejectRcpt: func(string) *smtp.SMTPError {
		return &smtp.SMTPError{Code: 550, EnhancedCode: smtp.EnhancedCode{5, 1, 1}, Message: "nope"}
	}})
	res := (&delivery.SMTPRelay{}).Send(ctx(t), cfgFor(relay, delivery.TLSNone), outbound(t, "", "a@corp.example"))
	assertPermanent(t, res[0], "550")
	if len(relay.Envelopes()) != 0 {
		t.Error("no message may be submitted when every recipient was refused")
	}
}

func TestSendDataRejected(t *testing.T) {
	for code, permanent := range map[int]bool{554: true, 451: false} {
		t.Run(fmt.Sprint(code), func(t *testing.T) {
			relay := testutil.StartRelay(t, testutil.RelayOptions{RejectData: &smtp.SMTPError{Code: code, EnhancedCode: smtp.EnhancedCode{code / 100, 0, 0}, Message: "refused"}})
			res := (&delivery.SMTPRelay{}).Send(ctx(t), cfgFor(relay, delivery.TLSNone), outbound(t, "", "a@corp.example", "b@corp.example"))
			for _, r := range res {
				if r.Err == nil || r.Err.Permanent != permanent || r.Err.Code != code {
					t.Errorf("%s: %+v", r.Address, r.Err)
				}
			}
		})
	}
}

func TestSendConnectionRefusedIsTransient(t *testing.T) {
	ln, _ := (&net.ListenConfig{}).Listen(context.Background(), "tcp", "127.0.0.1:0")
	port := ln.Addr().(*net.TCPAddr).Port
	_ = ln.Close()
	res := (&delivery.SMTPRelay{}).Send(ctx(t), delivery.RelayConfig{Host: "127.0.0.1", Port: port, TLSMode: delivery.TLSNone}, outbound(t, "", "a@corp.example"))
	if res[0].Err == nil || res[0].Err.Permanent {
		t.Errorf("a refused connection may succeed later: %+v", res[0].Err)
	}
}

func TestSendHonoursContextCancellation(t *testing.T) {
	// A server that accepts and never speaks.
	ln, _ := (&net.ListenConfig{}).Listen(context.Background(), "tcp", "127.0.0.1:0")
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			t.Cleanup(func() { _ = c.Close() })
		}
	}()
	cctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	start := time.Now()
	res := (&delivery.SMTPRelay{}).Send(cctx, delivery.RelayConfig{Host: "127.0.0.1", Port: ln.Addr().(*net.TCPAddr).Port, TLSMode: delivery.TLSNone}, outbound(t, "", "a@corp.example"))
	if res[0].Err == nil || time.Since(start) > 10*time.Second {
		t.Errorf("a cancelled send must stop quickly with an error: %v after %v", res[0].Err, time.Since(start))
	}
}

func TestSendWithoutRecipientsFails(t *testing.T) {
	var zero policy.Outbound // the zero value carries nothing and can not be sent
	res := (&delivery.SMTPRelay{}).Send(ctx(t), delivery.RelayConfig{Host: "127.0.0.1", Port: 1, TLSMode: delivery.TLSNone}, zero)
	if len(res) != 0 {
		t.Errorf("results %+v", res)
	}
}

func TestCheck(t *testing.T) {
	t.Run("ok", func(t *testing.T) {
		relay := testutil.StartRelay(t, testutil.RelayOptions{TLS: "starttls", User: "u", Pass: "p4ss"})
		cfg := cfgFor(relay, delivery.TLSStartTLS)
		cfg.Username, cfg.Password = "u", "p4ss"
		res := relayFor(relay).Check(ctx(t), cfg)
		if !res.OK || !res.TLS || !res.Authenticated || res.Error != "" {
			t.Errorf("result %+v", res)
		}
		if len(relay.Envelopes()) != 0 {
			t.Error("a check must not submit mail")
		}
	})
	t.Run("wrong password reports no relay text", func(t *testing.T) {
		relay := testutil.StartRelay(t, testutil.RelayOptions{TLS: "starttls", User: "u", Pass: "right"})
		cfg := cfgFor(relay, delivery.TLSStartTLS)
		cfg.Username, cfg.Password = "u", "wrong"
		res := relayFor(relay).Check(ctx(t), cfg)
		if res.OK || res.Error != "auth failed (535)" {
			t.Errorf("result %+v", res)
		}
	})
	t.Run("certificate problem", func(t *testing.T) {
		relay := testutil.StartRelay(t, testutil.RelayOptions{TLS: "starttls"})
		res := (&delivery.SMTPRelay{}).Check(ctx(t), cfgFor(relay, delivery.TLSStartTLS))
		if res.OK || res.Error != "tls failed: certificate verification failed" {
			t.Errorf("result %+v", res)
		}
	})
	t.Run("no starttls", func(t *testing.T) {
		relay := testutil.StartRelay(t, testutil.RelayOptions{})
		res := (&delivery.SMTPRelay{}).Check(ctx(t), cfgFor(relay, delivery.TLSStartTLS))
		if res.OK || res.Error != "tls failed: the relay does not offer STARTTLS" {
			t.Errorf("result %+v", res)
		}
	})
	t.Run("connection refused", func(t *testing.T) {
		ln, _ := (&net.ListenConfig{}).Listen(context.Background(), "tcp", "127.0.0.1:0")
		port := ln.Addr().(*net.TCPAddr).Port
		_ = ln.Close()
		res := (&delivery.SMTPRelay{}).Check(ctx(t), delivery.RelayConfig{Host: "127.0.0.1", Port: port, TLSMode: delivery.TLSNone})
		if res.OK || res.Error != "connect failed" {
			t.Errorf("a refused port must not leak details: %+v", res)
		}
	})
	t.Run("not an smtp server", func(t *testing.T) {
		ln, _ := (&net.ListenConfig{}).Listen(context.Background(), "tcp", "127.0.0.1:0")
		t.Cleanup(func() { _ = ln.Close() })
		go func() {
			c, err := ln.Accept()
			if err == nil {
				_, _ = c.Write([]byte("HTTP/1.1 200 OK secret-internal-banner\r\n"))
				_ = c.Close()
			}
		}()
		res := (&delivery.SMTPRelay{}).Check(ctx(t), delivery.RelayConfig{Host: "127.0.0.1", Port: ln.Addr().(*net.TCPAddr).Port, TLSMode: delivery.TLSNone})
		if res.OK || strings.Contains(res.Error, "banner") {
			t.Errorf("remote text leaked: %+v", res)
		}
	})
}

func TestRelayConfigDoesNotPrintPassword(t *testing.T) {
	cfg := delivery.RelayConfig{Host: "h.example", Port: 587, Username: "user", Password: "s3cret-pass", TLSMode: delivery.TLSStartTLS}
	var buf strings.Builder
	log := slog.New(slog.NewTextHandler(&buf, nil))
	log.Info("x", "relay", cfg)
	for _, s := range []string{fmt.Sprint(cfg), fmt.Sprintf("%v", cfg), fmt.Sprintf("%+v", cfg), fmt.Sprintf("%#v", cfg), buf.String()} {
		if strings.Contains(s, "s3cret-pass") {
			t.Errorf("password leaked in %q", s)
		}
	}
}
