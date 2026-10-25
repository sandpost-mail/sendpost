package delivery_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/sandpost-mail/sandpost/internal/delivery"
	"github.com/sandpost-mail/sandpost/internal/testutil"
)

func ptr(s string) *string { return &s }

func validTransport() delivery.TransportInput {
	return delivery.TransportInput{
		Kind: delivery.KindSMTP, Host: "smtp.example.com", Port: 587, TLSMode: delivery.TLSStartTLS,
		Username: "user", Secret: ptr("hunter2-password"), Sender: "Noreply@Example.com",
	}
}

func TestPutTransportValidation(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(in *delivery.TransportInput)
	}{
		{"unknown kind", func(in *delivery.TransportInput) { in.Kind = "sendmail" }},
		{"empty host", func(in *delivery.TransportInput) { in.Host = "" }},
		{"host with scheme", func(in *delivery.TransportInput) { in.Host = "smtp://smtp.example.com" }},
		{"host with port", func(in *delivery.TransportInput) { in.Host = "smtp.example.com:587" }},
		{"host with space", func(in *delivery.TransportInput) { in.Host = "smtp .example.com" }},
		{"host with newline", func(in *delivery.TransportInput) { in.Host = "a.example.com\nb" }},
		{"host with double dot", func(in *delivery.TransportInput) { in.Host = "a..example.com" }},
		{"port zero", func(in *delivery.TransportInput) { in.Port = 0 }},
		{"port too large", func(in *delivery.TransportInput) { in.Port = 70000 }},
		{"unknown tls mode", func(in *delivery.TransportInput) { in.TLSMode = "ssl" }},
		{"username without TLS", func(in *delivery.TransportInput) { in.TLSMode = delivery.TLSNone }},
		{"username with newline", func(in *delivery.TransportInput) { in.Username = "u\r\nx" }},
		{"invalid sender", func(in *delivery.TransportInput) { in.Sender = "Name <a@b.example>" }},
		{"username without password", func(in *delivery.TransportInput) { in.Secret = nil }},
		{"cleared password with username", func(in *delivery.TransportInput) { in.Secret = ptr("") }},
		{"password without username", func(in *delivery.TransportInput) { in.Username = "" }},
		{"ses without username", func(in *delivery.TransportInput) { in.Kind = delivery.KindSES; in.Username = ""; in.Secret = nil }},
		{"ses without sender", func(in *delivery.TransportInput) { in.Kind = delivery.KindSES; in.Sender = "" }},
		{"ses without TLS", func(in *delivery.TransportInput) {
			in.Kind = delivery.KindSES
			in.TLSMode = delivery.TLSNone
			in.Username = ""
			in.Secret = nil
		}},
	}
	e := newEnv(t, testutil.RelayOptions{})
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			in := validTransport()
			tc.mutate(&in)
			_, err := e.svc.PutTransport(e.ctx, e.scope, in)
			mustErrIs(t, err, delivery.ErrInvalid)
		})
	}
	if _, err := e.svc.GetTransport(e.ctx, e.scope); !errors.Is(err, delivery.ErrNotFound) {
		t.Errorf("a rejected input must store nothing: %v", err)
	}
}

func TestPutTransportLifecycle(t *testing.T) {
	e := newEnv(t, testutil.RelayOptions{})
	in := validTransport()
	in.Enabled = true // ignored on creation
	created, err := e.svc.PutTransport(e.ctx, e.scope, in)
	if err != nil {
		t.Fatal(err)
	}
	if created.Enabled || !created.HasSecret || created.Sender != "Noreply@example.com" {
		t.Errorf("a new transport starts disabled with a stored password: %+v", created)
	}

	// The password is sealed, never stored or returned in clear.
	var blob []byte
	if err := e.pool.QueryRow(e.ctx, "SELECT secret_enc FROM transports").Scan(&blob); err != nil {
		t.Fatal(err)
	}
	if len(blob) == 0 || strings.Contains(string(blob), "hunter2-password") {
		t.Errorf("stored secret %q", blob)
	}

	// Same connection, no new password: the stored one stays and enabling works.
	same := in
	same.Secret = nil
	enabled, err := e.svc.PutTransport(e.ctx, e.scope, same)
	if err != nil || !enabled.Enabled || !enabled.HasSecret {
		t.Fatalf("enable: %+v %v", enabled, err)
	}

	// Changing the sender keeps it enabled (it is not a connection setting).
	changedSender := same
	changedSender.Sender = "other@example.com"
	if got, err := e.svc.PutTransport(e.ctx, e.scope, changedSender); err != nil || !got.Enabled {
		t.Errorf("sender change: %+v %v", got, err)
	}

	// Changing the host needs the password again, or the stored one could be sent anywhere.
	moved := same
	moved.Host = "attacker.example.net"
	_, err = e.svc.PutTransport(e.ctx, e.scope, moved)
	mustErrIs(t, err, delivery.ErrInvalid)
	for _, mut := range []func(*delivery.TransportInput){
		func(i *delivery.TransportInput) { i.Port = 2525 },
		func(i *delivery.TransportInput) { i.Username = "someone-else" },
		func(i *delivery.TransportInput) { i.TLSMode = delivery.TLSImplicit },
		func(i *delivery.TransportInput) { i.Kind = delivery.KindSES },
	} {
		c := same
		mut(&c)
		if _, err := e.svc.PutTransport(e.ctx, e.scope, c); !errors.Is(err, delivery.ErrInvalid) {
			t.Errorf("a connection change without a password must be refused: %+v -> %v", c, err)
		}
	}
	// With a new password the change is accepted but the transport is disabled again.
	moved.Secret = ptr("new-password-123")
	after, err := e.svc.PutTransport(e.ctx, e.scope, moved)
	if err != nil || after.Enabled || after.Host != "attacker.example.net" {
		t.Errorf("a changed connection must start disabled: %+v %v", after, err)
	}
	if got, _ := e.svc.GetTransport(e.ctx, e.scope); got.Enabled {
		t.Error("stored transport is still enabled")
	}

	// Delete.
	if err := e.svc.DeleteTransport(e.ctx, e.scope); err != nil {
		t.Fatal(err)
	}
	mustErrIs(t, e.svc.DeleteTransport(e.ctx, e.scope), delivery.ErrNotFound)
	if _, err := e.svc.GetTransport(e.ctx, e.scope); !errors.Is(err, delivery.ErrNotFound) {
		t.Errorf("get after delete: %v", err)
	}
}

func TestClearingThePassword(t *testing.T) {
	e := newEnv(t, testutil.RelayOptions{})
	in := validTransport()
	if _, err := e.svc.PutTransport(e.ctx, e.scope, in); err != nil {
		t.Fatal(err)
	}
	in.Username, in.Secret, in.TLSMode = "", ptr(""), delivery.TLSNone
	got, err := e.svc.PutTransport(e.ctx, e.scope, in)
	if err != nil || got.HasSecret || got.Username != "" {
		t.Errorf("%+v %v", got, err)
	}
}

func TestTransportChangesAreAuditedWithoutSecrets(t *testing.T) {
	e := newEnv(t, testutil.RelayOptions{})
	in := validTransport()
	if _, err := e.svc.PutTransport(e.ctx, e.scope, in); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.PutPolicy(e.ctx, e.scope, delivery.PolicyInput{Mode: "allow", Allowlist: []string{"@corp.example"}}); err != nil {
		t.Fatal(err)
	}
	if err := e.svc.DeleteTransport(e.ctx, e.scope); err != nil {
		t.Fatal(err)
	}
	rows, err := e.pool.Query(e.ctx, "SELECT action, detail::text FROM audit_log WHERE project_id = $1 ORDER BY id", e.pid)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var actions []string
	for rows.Next() {
		var action, detail string
		if err := rows.Scan(&action, &detail); err != nil {
			t.Fatal(err)
		}
		actions = append(actions, action)
		if strings.Contains(detail, "hunter2-password") || strings.Contains(detail, `"user"`) {
			t.Errorf("secret or username in audit detail: %s", detail)
		}
	}
	if strings.Join(actions, ",") != "transport.update,policy.update,transport.delete" {
		t.Errorf("audit actions %v", actions)
	}
}

func TestPolicyDefaultsAndValidation(t *testing.T) {
	e := newEnv(t, testutil.RelayOptions{})
	p, err := e.svc.GetPolicy(e.ctx, e.scope)
	if err != nil || p.Mode != "capture" || p.Configured || p.Allowlist == nil || len(p.Allowlist) != 0 {
		t.Errorf("default policy %+v %v", p, err)
	}

	bad := []delivery.PolicyInput{
		{Mode: ""}, {Mode: "relay"}, {Mode: "ALLOW"},
		{Mode: "allow", Allowlist: []string{"*"}},
		{Mode: "allow", Allowlist: []string{"@*.example.com"}},
		{Mode: "allow", Allowlist: []string{"ok@example.com", "bad entry"}},
		{Mode: "redirect"}, // no redirect address
		{Mode: "redirect", RedirectAddress: "not-an-address"},
		{Mode: "redirect", RedirectAddress: "a@example.com, b@example.com"},
		{Mode: "allow", RedirectAddress: "nope"}, // a stored redirect address must be valid too
		{Mode: "allow", SubjectPrefix: "x\r\ny"},
	}
	for _, in := range bad {
		if _, err := e.svc.PutPolicy(e.ctx, e.scope, in); !errors.Is(err, delivery.ErrInvalid) {
			t.Errorf("%+v must be refused, got %v", in, err)
		}
	}
	if p, _ := e.svc.GetPolicy(e.ctx, e.scope); p.Configured {
		t.Error("a refused policy must store nothing")
	}

	saved, err := e.svc.PutPolicy(e.ctx, e.scope, delivery.PolicyInput{
		Mode: "redirect", Allowlist: []string{"Dev@Corp.example", "corp.example", "@Corp.example"},
		RedirectAddress: " PM@Corp.example ", SubjectPrefix: "[qa] ",
	})
	if err != nil {
		t.Fatal(err)
	}
	if saved.Mode != "redirect" || strings.Join(saved.Allowlist, ",") != "dev@corp.example,@corp.example" ||
		saved.RedirectAddress != "PM@corp.example" || !saved.Configured {
		t.Errorf("saved %+v", saved)
	}
	// Capture is always allowed and keeps the allowlist for releases.
	if p, err := e.svc.PutPolicy(e.ctx, e.scope, delivery.PolicyInput{Mode: "capture", Allowlist: []string{"@corp.example"}}); err != nil || p.Mode != "capture" {
		t.Errorf("%+v %v", p, err)
	}
}

func TestTestTransport(t *testing.T) {
	e := newEnv(t, testutil.RelayOptions{TLS: "starttls", User: "u", Pass: "relay-pass"})
	_, err := e.svc.TestTransport(e.ctx, e.scope)
	mustErrIs(t, err, delivery.ErrNotFound)

	in := delivery.TransportInput{Kind: delivery.KindSMTP, Host: e.relay.Host, Port: e.relay.Port, TLSMode: delivery.TLSStartTLS, Username: "u", Secret: ptr("relay-pass")}
	if _, err := e.svc.PutTransport(e.ctx, e.scope, in); err != nil {
		t.Fatal(err)
	}
	res, err := e.svc.TestTransport(e.ctx, e.scope) // works while disabled
	if err != nil || !res.OK || !res.TLS || !res.Authenticated {
		t.Fatalf("%+v %v", res, err)
	}
	if len(e.relay.Envelopes()) != 0 {
		t.Error("the test must not send mail")
	}

	// A wrong stored password is reported without the password.
	wrong := in
	wrong.Secret = ptr("wrong-relay-pass")
	if _, err := e.svc.PutTransport(e.ctx, e.scope, wrong); err != nil {
		t.Fatal(err)
	}
	res, err = e.svc.TestTransport(e.ctx, e.scope)
	if err != nil || res.OK || strings.Contains(res.Error, "wrong-relay-pass") || res.Error == "" {
		t.Errorf("%+v %v", res, err)
	}

	// A blob that does not open is reported as such.
	e.exec(`UPDATE transports SET secret_enc = '\x0102030405060708090a0b0c0d0e0f1011121314151617181920'::bytea`)
	res, err = e.svc.TestTransport(e.ctx, e.scope)
	if err != nil || res.OK || !strings.Contains(res.Error, "decrypted") {
		t.Errorf("%+v %v", res, err)
	}
}

// A sealed password copied to another project does not open there.
func TestSealedPasswordIsBoundToItsProject(t *testing.T) {
	e := newEnv(t, testutil.RelayOptions{TLS: "starttls", User: "u", Pass: "relay-pass"})
	in := delivery.TransportInput{Kind: delivery.KindSMTP, Host: e.relay.Host, Port: e.relay.Port, TLSMode: delivery.TLSStartTLS, Username: "u", Secret: ptr("relay-pass")}
	if _, err := e.svc.PutTransport(e.ctx, e.scope, in); err != nil {
		t.Fatal(err)
	}
	other := testutil.Project(t, e.pool, "other")
	e.exec(`INSERT INTO transports (project_id, kind, host, port, username, secret_enc, tls_mode)
	        SELECT $1, kind, host, port, username, secret_enc, tls_mode FROM transports WHERE project_id = $2`, other, e.pid)
	res, err := e.svc.TestTransport(e.ctx, authzTrusted(other))
	if err != nil || res.OK || !strings.Contains(res.Error, "decrypted") {
		t.Errorf("a password copied between projects must not open: %+v %v", res, err)
	}
}

func TestSettingsAreProjectScoped(t *testing.T) {
	e := newEnv(t, testutil.RelayOptions{})
	other := authzTrusted(testutil.Project(t, e.pool, "other"))
	if _, err := e.svc.PutTransport(e.ctx, other, validTransport()); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.PutPolicy(e.ctx, other, delivery.PolicyInput{Mode: "allow", Allowlist: []string{"@corp.example"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.GetTransport(e.ctx, e.scope); !errors.Is(err, delivery.ErrNotFound) {
		t.Errorf("transport leaked across projects: %v", err)
	}
	if p, _ := e.svc.GetPolicy(e.ctx, e.scope); p.Configured {
		t.Error("policy leaked across projects")
	}
	mustErrIs(t, e.svc.DeleteTransport(e.ctx, e.scope), delivery.ErrNotFound)
	if _, err := e.svc.GetTransport(e.ctx, other); err != nil {
		t.Errorf("the other project lost its transport: %v", err)
	}
}
