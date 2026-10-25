package main

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/emersion/go-sasl"
	"github.com/emersion/go-smtp"

	"github.com/sandpost-mail/sandpost/internal/testutil"
)

const (
	e2eRelayPassword = "e2e-relay-password-42" //nolint:gosec // test value
	e2eCorp          = "real@corp.example"
	e2eStranger      = "stranger@elsewhere.test"
	e2eHidden        = "hidden@elsewhere.test"
	e2eRedirect      = "pm@corp.example"
)

// e2eStack is a running server with an admin session and a project.
type e2eStack struct {
	t       *testing.T
	base    string
	smtp    string
	sess    *session
	pid     int64
	cred    struct{ Username, Secret string }
	logs    *bytes.Buffer
	execSQL func(sql string, args ...any)
	stop    func()
}

func startStack(t *testing.T) *e2eStack {
	t.Helper()
	dsn := testutil.PostgresDSN(t)
	httpAddr, smtpAddr := freeAddr(t), freeAddr(t)
	vars := map[string]string{
		"DATABASE_URL": dsn, "SECRET_KEY": testKey, "HTTP_LISTEN": httpAddr, "SMTP_LISTEN": smtpAddr,
		"ADMIN_EMAIL": "admin@example.test", "ADMIN_PASSWORD": "a-long-admin-password",
	}
	ctx := context.Background()
	if e, _, errb := testEnv(vars); run(ctx, e, []string{"migrate", "up"}) != exitOK {
		t.Fatalf("migrate: %s", errb)
	}
	e, _, errb := testEnv(vars)
	sctx, cancel := context.WithCancel(ctx)
	done := make(chan int, 1)
	go func() { done <- run(sctx, e, []string{"serve"}) }()
	s := &e2eStack{t: t, base: "http://" + httpAddr, smtp: smtpAddr, logs: errb}
	s.stop = func() {
		cancel()
		select {
		case code := <-done:
			if code != exitOK {
				t.Errorf("serve exit %d stderr %q", code, errb)
			}
		case <-time.After(30 * time.Second):
			t.Error("serve did not stop")
		}
	}
	t.Cleanup(func() { s.stop(); s.stop = func() {} })
	if code := waitStatus(t, s.base+"/readyz"); code != http.StatusOK {
		t.Fatalf("readyz %d", code)
	}
	waitPort(t, smtpAddr)

	s.sess = login(t, s.base, "admin@example.test", "a-long-admin-password")
	s.sess.json(t, http.MethodPost, s.base+"/_/api/v1/auth/password",
		map[string]string{"current_password": "a-long-admin-password", "new_password": "a-different-admin-password"}, http.StatusNoContent, nil)
	var project struct {
		ID int64 `json:"id"`
	}
	s.sess.json(t, http.MethodPost, s.base+"/_/api/v1/projects", map[string]string{"name": "Delivery"}, http.StatusCreated, &project)
	s.pid = project.ID
	s.sess.json(t, http.MethodPost, fmt.Sprintf("%s/_/api/v1/projects/%d/credentials", s.base, s.pid), map[string]string{"label": "e2e"}, http.StatusCreated, &s.cred)

	pool := testutil.PoolFor(t, dsn)
	s.execSQL = func(sql string, args ...any) {
		t.Helper()
		if _, err := pool.Exec(ctx, sql, args...); err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
	}
	return s
}

func (s *e2eStack) url(format string, args ...any) string {
	return s.base + fmt.Sprintf("/_/api/v1/projects/%d", s.pid) + fmt.Sprintf(format, args...)
}

func (s *e2eStack) putTransport(body map[string]any, want int) {
	s.t.Helper()
	s.sess.json(s.t, http.MethodPut, s.url("/transport"), body, want, nil)
}

func (s *e2eStack) setTransport(relay *testutil.FakeRelay, enabled bool) {
	s.t.Helper()
	body := map[string]any{"kind": "smtp", "host": relay.Host, "port": relay.Port, "tls_mode": "none"}
	s.putTransport(body, http.StatusOK)
	if enabled {
		body["enabled"] = true
		s.putTransport(body, http.StatusOK)
	}
}

func (s *e2eStack) setPolicy(mode, redirect string, allow ...string) {
	s.t.Helper()
	body := map[string]any{"mode": mode, "allowlist": allow, "redirect_address": redirect, "subject_prefix": "[sandbox]"}
	s.sess.json(s.t, http.MethodPut, s.url("/policy"), body, http.StatusOK, nil)
}

// send submits a message through the SMTP listener with the project credential.
func (s *e2eStack) send(to ...string) int64 {
	s.t.Helper()
	c, err := smtp.Dial(s.smtp)
	if err != nil {
		s.t.Fatal(err)
	}
	defer func() { _ = c.Close() }()
	if err := c.Auth(sasl.NewPlainClient("", s.cred.Username, s.cred.Secret)); err != nil {
		s.t.Fatal(err)
	}
	body := "From: App <app@example.test>\r\nTo: " + strings.Join(to, ", ") + "\r\nBcc: " + e2eHidden + "\r\nSubject: Welcome\r\n\r\nHello\r\n"
	rcpt := append(append([]string(nil), to...), e2eHidden)
	if err := c.SendMail("app@example.test", rcpt, strings.NewReader(body)); err != nil {
		s.t.Fatal(err)
	}
	var page struct {
		Items []struct {
			ID int64 `json:"id"`
		} `json:"items"`
	}
	s.sess.json(s.t, http.MethodGet, s.url("/messages?limit=1"), nil, http.StatusOK, &page)
	if len(page.Items) != 1 {
		s.t.Fatalf("messages %+v", page)
	}
	return page.Items[0].ID
}

type deliveryRow struct {
	Recipient string `json:"recipient"`
	Effective string `json:"effective_recipient"`
	Status    string `json:"status"`
	Error     string `json:"error"`
	Kind      string `json:"kind"`
}

// waitTerminal polls the delivery status of a message until it has rows and
// none is pending. It never sleeps for a fixed time.
func (s *e2eStack) waitTerminal(mid int64) []deliveryRow {
	s.t.Helper()
	deadline := time.Now().Add(40 * time.Second)
	for time.Now().Before(deadline) {
		var out struct {
			Items []deliveryRow `json:"items"`
		}
		s.sess.json(s.t, http.MethodGet, s.url("/messages/%d/deliveries", mid), nil, http.StatusOK, &out)
		pending := false
		for _, r := range out.Items {
			pending = pending || r.Status == "pending"
		}
		if len(out.Items) > 0 && !pending {
			return out.Items
		}
		time.Sleep(100 * time.Millisecond)
	}
	s.t.Fatalf("message %d never reached a terminal delivery state", mid)
	return nil
}

// TestEndToEndDeliveryNeverReachesRealRecipientsWhenMisconfigured is the M4
// exit criterion. A positive control proves the pipeline really delivers, then
// every misconfiguration is tried against a relay that must see nothing: not
// a connection, not a RCPT command.
func TestEndToEndDeliveryNeverReachesRealRecipientsWhenMisconfigured(t *testing.T) {
	s := startStack(t)

	// Positive control: redirect mode through a working relay.
	control := testutil.StartRelay(t, testutil.RelayOptions{})
	s.setTransport(control, true)
	s.setPolicy("redirect", e2eRedirect, e2eCorp)
	mid := s.send(e2eCorp, e2eStranger)
	rows := s.waitTerminal(mid)
	for _, r := range rows {
		if r.Status != "sent" {
			t.Fatalf("the control delivery must succeed: %+v", rows)
		}
	}
	if got := len(control.Envelopes()); got != 2 {
		t.Fatalf("control relay got %d messages, want 2 (real and redirected)", got)
	}
	for _, a := range control.RcptAttempts() {
		if a != e2eCorp && a != e2eRedirect {
			t.Errorf("control relay was asked to deliver to %s", a)
		}
	}
	for _, env := range control.Envelopes() {
		if strings.Contains(strings.ToLower(string(env.Data)), "bcc:") || strings.Contains(string(env.Data), e2eHidden) {
			t.Errorf("Bcc reached the relay:\n%s", env.Data)
		}
	}
	controlConns, controlMails, controlRcpts := control.Connections(), len(control.Envelopes()), len(control.RcptAttempts())

	// From here on a trap relay is the transport. It must stay untouched.
	trap := testutil.StartRelay(t, testutil.RelayOptions{User: "relay-user", Pass: e2eRelayPassword})
	trapConns := 0 // connections that were expected (the TLS test below contacts it on purpose)
	assertUntouched := func(label string) {
		t.Helper()
		if trap.Connections() != trapConns || len(trap.RcptAttempts()) != 0 || len(trap.Envelopes()) != 0 {
			t.Fatalf("%s: the trap relay saw %d connection(s), RCPT %v, %d message(s)", label, trap.Connections(), trap.RcptAttempts(), len(trap.Envelopes()))
		}
		if control.Connections() != controlConns || len(control.Envelopes()) != controlMails {
			t.Fatalf("%s: the previous relay was contacted again", label)
		}
	}
	blocked := func(label string, mid int64) {
		t.Helper()
		rows := s.waitTerminal(mid)
		for _, r := range rows {
			if r.Status == "sent" || r.Status == "pending" {
				t.Fatalf("%s: row %+v", label, r)
			}
		}
		assertUntouched(label)
	}
	s.setTransport(trap, false) // disabled
	s.setPolicy("redirect", e2eRedirect, e2eCorp)

	t.Run("transport disabled", func(_ *testing.T) {
		blocked("disabled", s.send(e2eCorp, e2eStranger))
	})

	s.setTransport(trap, true)
	t.Run("allowlist corrupted", func(_ *testing.T) {
		s.execSQL(`UPDATE delivery_policies SET allowlist = '["*", 7]'::jsonb WHERE project_id = $1`, s.pid)
		blocked("allowlist", s.send(e2eCorp, e2eStranger))
	})
	t.Run("redirect address broken", func(_ *testing.T) {
		s.setPolicy("redirect", e2eRedirect, e2eCorp)
		s.execSQL(`UPDATE delivery_policies SET redirect_address = 'pm@corp.example, extra@evil.test' WHERE project_id = $1`, s.pid)
		blocked("redirect address", s.send(e2eCorp, e2eStranger))
	})
	t.Run("redirect address missing", func(_ *testing.T) {
		s.execSQL(`UPDATE delivery_policies SET redirect_address = '' WHERE project_id = $1`, s.pid)
		blocked("no redirect address", s.send(e2eCorp, e2eStranger))
	})
	t.Run("transport sender corrupted", func(_ *testing.T) {
		s.setPolicy("redirect", e2eRedirect, e2eCorp)
		s.execSQL(`UPDATE transports SET sender = 'a b' WHERE project_id = $1`, s.pid)
		blocked("sender", s.send(e2eCorp, e2eStranger))
		s.execSQL(`UPDATE transports SET sender = '' WHERE project_id = $1`, s.pid)
	})
	t.Run("stored password for another key", func(_ *testing.T) {
		s.execSQL(`UPDATE transports SET username = 'relay-user', tls_mode = 'starttls',
			secret_enc = '\x0102030405060708090a0b0c0d0e0f1011121314151617181920'::bytea WHERE project_id = $1`, s.pid)
		blocked("undecryptable password", s.send(e2eCorp, e2eStranger))
	})
	t.Run("password never sent without verified TLS", func(t *testing.T) {
		// A valid password, a relay that offers no TLS: the job must give up without authenticating.
		s.putTransport(map[string]any{"kind": "smtp", "host": trap.Host, "port": trap.Port, "tls_mode": "starttls", "username": "relay-user", "secret": e2eRelayPassword}, http.StatusOK)
		s.putTransport(map[string]any{"kind": "smtp", "host": trap.Host, "port": trap.Port, "tls_mode": "starttls", "username": "relay-user", "enabled": true}, http.StatusOK)
		s.setPolicy("allow", "", e2eCorp)
		mid := s.send(e2eCorp, e2eStranger)
		rows := s.waitTerminal(mid)
		var corp *deliveryRow
		for i := range rows {
			if rows[i].Recipient == e2eCorp {
				corp = &rows[i]
			}
		}
		if corp == nil || corp.Status != "failed" || !strings.Contains(corp.Error, "STARTTLS") {
			t.Fatalf("rows %+v", rows)
		}
		if len(trap.SeenPasswords()) != 0 || len(trap.RcptAttempts()) != 0 || len(trap.Envelopes()) != 0 {
			t.Fatalf("the relay saw a password %v or mail", trap.SeenPasswords())
		}
		defer func() { trapConns = trap.Connections() }()
		var check struct {
			OK    bool   `json:"ok"`
			Error string `json:"error"`
		}
		s.sess.json(t, http.MethodPost, s.url("/transport/test"), nil, http.StatusOK, &check)
		if check.OK || len(trap.SeenPasswords()) != 0 {
			t.Fatalf("the connection test must fail without sending the password: %+v", check)
		}
	})
	t.Run("transport missing", func(t *testing.T) {
		s.sess.json(t, http.MethodDelete, s.url("/transport"), nil, http.StatusNoContent, nil)
		s.setPolicy("redirect", e2eRedirect, e2eCorp)
		blocked("no transport", s.send(e2eCorp, e2eStranger))
	})
	t.Run("capture mode queues nothing", func(t *testing.T) {
		s.setTransport(trap, true)
		s.setPolicy("capture", "", e2eCorp, e2eStranger)
		mid := s.send(e2eCorp, e2eStranger)
		var out struct {
			Items []deliveryRow `json:"items"`
		}
		s.sess.json(t, http.MethodGet, s.url("/messages/%d/deliveries", mid), nil, http.StatusOK, &out)
		if len(out.Items) != 0 {
			t.Errorf("rows in capture mode: %+v", out.Items)
		}
		assertUntouched("capture")
	})
	t.Run("allow mode skips unlisted recipients", func(t *testing.T) {
		// Working relay again: only the allowlisted recipient may be contacted.
		s.setTransport(control, true)
		s.setPolicy("allow", "", e2eCorp)
		mid := s.send(e2eCorp, e2eStranger)
		rows := s.waitTerminal(mid)
		st := map[string]string{}
		for _, r := range rows {
			st[r.Recipient] = r.Status
		}
		if st[e2eCorp] != "sent" || st[e2eStranger] != "skipped" {
			t.Fatalf("statuses %v", st)
		}
		for _, a := range control.RcptAttempts()[controlRcpts:] {
			if a != e2eCorp {
				t.Errorf("an unlisted recipient was contacted: %s", a)
			}
		}
		if trap.Connections() != trapConns {
			t.Error("the trap relay was contacted")
		}
	})
	t.Run("manual release respects the allowlist", func(t *testing.T) {
		before := len(control.Envelopes())
		var apiErr struct{ Code string }
		s.sess.json(t, http.MethodPost, s.url("/messages/%d/release", mid), map[string]any{"to": []string{e2eStranger}}, http.StatusBadRequest, &apiErr)
		if apiErr.Code != "delivery_blocked" {
			t.Errorf("code %q", apiErr.Code)
		}
		s.sess.json(t, http.MethodPost, s.url("/messages/%d/release", mid), map[string]any{"to": []string{e2eCorp}}, http.StatusAccepted, nil)
		waitFor(t, func() bool { return len(control.Envelopes()) == before+1 })
		for _, a := range control.RcptAttempts() {
			if strings.HasSuffix(a, "elsewhere.test") {
				t.Errorf("release contacted %s", a)
			}
		}
	})

	s.stop()
	s.stop = func() {}
	logs := s.logs.String()
	for _, secret := range []string{e2eRelayPassword, s.cred.Secret, "a-different-admin-password"} {
		if strings.Contains(logs, secret) {
			t.Errorf("a secret leaked into the logs: %q", secret)
		}
	}
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("condition not reached in time")
}
