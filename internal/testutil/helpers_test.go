package testutil

import (
	"context"
	"crypto/tls"
	"errors"
	"net"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/emersion/go-sasl"
	"github.com/emersion/go-smtp"

	"github.com/sandpost-mail/sandpost/internal/store/sqlcgen"
)

const relayMsg = "Subject: hi\r\n\r\nbody\r\n"

func relayAddr(r *FakeRelay) string { return net.JoinHostPort(r.Host, strconv.Itoa(r.Port)) }

func TestRelayPlaintextRecordsAndRejects(t *testing.T) {
	r := StartRelay(t, RelayOptions{
		RejectRcpt: func(addr string) *smtp.SMTPError {
			if strings.HasPrefix(addr, "bad@") {
				return &smtp.SMTPError{Code: 550, Message: "no such user"}
			}
			return nil
		},
	})
	c, err := smtp.Dial(relayAddr(r))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = c.Close() }()
	if err := c.SendMail("a@x.test", []string{"bad@x.test"}, strings.NewReader(relayMsg)); err == nil {
		t.Error("a rejected recipient must fail the send")
	}
	if err := c.SendMail("a@x.test", []string{"ok@x.test"}, strings.NewReader(relayMsg)); err != nil {
		t.Fatal(err)
	}
	_ = c.Quit()
	envs := r.Envelopes()
	if len(envs) != 1 || envs[0].From != "a@x.test" || !slices.Equal(envs[0].To, []string{"ok@x.test"}) || envs[0].TLS || envs[0].Authenticated {
		t.Errorf("envelopes = %+v", envs)
	}
	if got := r.RcptAttempts(); !slices.Equal(got, []string{"bad@x.test", "ok@x.test"}) {
		t.Errorf("rcpt attempts = %v", got)
	}
	if r.Connections() != 1 {
		t.Errorf("connections = %d, want 1", r.Connections())
	}
}

func TestRelayStartTLSWithAuth(t *testing.T) {
	r := StartRelay(t, RelayOptions{TLS: "starttls", User: "u", Pass: "p", CertNames: []string{"127.0.0.1", "Relay.Test"}})
	c, err := smtp.DialStartTLS(relayAddr(r), &tls.Config{RootCAs: r.Roots, ServerName: "relay.test", MinVersion: tls.VersionTLS12})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = c.Close() }()
	if err := c.SendMail("a@x.test", []string{"b@x.test"}, strings.NewReader(relayMsg)); err == nil {
		t.Error("MAIL before AUTH must be refused")
	}
	if err := c.Auth(sasl.NewPlainClient("", "u", "wrong")); err == nil {
		t.Error("a wrong password must fail")
	}
	if err := c.Auth(sasl.NewPlainClient("", "u", "p")); err != nil {
		t.Fatal(err)
	}
	if err := c.SendMail("a@x.test", []string{"b@x.test"}, strings.NewReader(relayMsg)); err != nil {
		t.Fatal(err)
	}
	envs := r.Envelopes()
	if len(envs) != 1 || !envs[0].TLS || !envs[0].Authenticated {
		t.Errorf("envelopes = %+v", envs)
	}
	if got := r.SeenPasswords(); !slices.Equal(got, []string{"wrong", "p"}) {
		t.Errorf("passwords = %v", got)
	}
}

func TestRelayImplicitTLSRejectData(t *testing.T) {
	r := StartRelay(t, RelayOptions{TLS: "implicit", RejectData: &smtp.SMTPError{Code: 554, Message: "no"}})
	c, err := smtp.DialTLS(relayAddr(r), &tls.Config{RootCAs: r.Roots, ServerName: "localhost", MinVersion: tls.VersionTLS12})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = c.Close() }()
	err = c.SendMail("a@x.test", []string{"b@x.test"}, strings.NewReader(relayMsg))
	var se *smtp.SMTPError
	if !errors.As(err, &se) || se.Code != 554 {
		t.Errorf("send err = %v, want 554", err)
	}
	if len(r.Envelopes()) != 0 {
		t.Error("a rejected DATA must not be recorded")
	}
}

func TestAccessHelpers(t *testing.T) {
	pool := Pool(t)
	ctx := context.Background()
	pid := Project(t, pool, "alpha")
	uid := User(t, pool, "a@x.test", false)
	admin := UserWithPassword(t, pool, "b@x.test", "correct horse battery", true, true)
	gid := Group(t, pool, "team")
	Member(t, pool, gid, uid)
	Assign(t, pool, pid, gid, "member")

	q := sqlcgen.New(pool)
	u, err := q.GetUserForLogin(ctx, "b@x.test")
	if err != nil || !u.IsAdmin || !u.MustChangePassword || u.PasswordHash == "!" {
		t.Errorf("admin user = %+v, %v", u, err)
	}
	if uid == admin || gid == 0 {
		t.Errorf("ids uid %d admin %d gid %d", uid, admin, gid)
	}
}
