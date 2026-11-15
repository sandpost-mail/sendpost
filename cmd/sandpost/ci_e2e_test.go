package main

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/emersion/go-sasl"
	"github.com/emersion/go-smtp"
	"github.com/jackc/pgx/v5"

	"github.com/sandpost-mail/sandpost/internal/events"
	"github.com/sandpost-mail/sandpost/internal/testutil"
)

// TestCIHelpersEndToEnd drives the real binary entry point against a real
// server: mail in over SMTP with an X-Run-Id header, then "sandpost wait" and
// "sandpost extract" with a project token, including a wait that is released
// by mail arriving later and a run that never sees another run's mail.
func TestCIHelpersEndToEnd(t *testing.T) {
	dsn := testutil.MigratedDSN(t)
	vars := map[string]string{
		"DATABASE_URL": dsn, "SECRET_KEY": testKey,
		"ADMIN_EMAIL": "admin@example.test", "ADMIN_PASSWORD": "a-long-admin-password",
	}
	srv := startServe(t, vars)
	smtpAddr := srv.smtpAddr
	base := "http://" + srv.httpAddr
	if code := waitStatus(t, base+"/readyz"); code != http.StatusOK {
		t.Fatalf("readyz %d", code)
	}

	sess := login(t, base, "admin@example.test", "a-long-admin-password")
	sess.json(t, http.MethodPost, base+"/_/api/v1/auth/password",
		map[string]string{"current_password": "a-long-admin-password", "new_password": "a-different-admin-password"},
		http.StatusNoContent, nil)
	var project struct {
		ID int64 `json:"id"`
	}
	sess.json(t, http.MethodPost, base+"/_/api/v1/projects", map[string]string{"name": "CI"}, http.StatusCreated, &project)
	pbase := fmt.Sprintf("%s/_/api/v1/projects/%d", base, project.ID)
	var cred struct{ Username, Secret string }
	sess.json(t, http.MethodPost, pbase+"/credentials", map[string]string{"label": "ci"}, http.StatusCreated, &cred)
	var token struct {
		Secret string `json:"secret"`
	}
	sess.json(t, http.MethodPost, pbase+"/tokens", map[string]any{"label": "ci", "scopes": []string{"read"}}, http.StatusCreated, &token)

	// send runs on the test goroutine only and reports failures as errors.
	send := func(runID, subject, text string) error {
		c, err := smtp.Dial(smtpAddr)
		if err != nil {
			return err
		}
		defer func() { _ = c.Close() }()
		if err := c.Auth(sasl.NewPlainClient("", cred.Username, cred.Secret)); err != nil {
			return err
		}
		msg := "From: App <app@example.test>\r\nTo: dev@example.test\r\nSubject: " + subject + "\r\nX-Run-Id: " + runID + "\r\n\r\n" + text + "\r\n"
		return c.SendMail("app@example.test", []string{"dev@example.test"}, strings.NewReader(msg))
	}

	cli := map[string]string{envURL: base, envProject: fmt.Sprint(project.ID), envToken: token.Secret}

	// A wait started before the mail is released by it. The mail is sent
	// only once the server LISTENs for the project, which only the pending
	// wait asks for, and while the wait has not returned.
	type ciResult struct {
		code      int
		out, errs string
	}
	waited := make(chan ciResult, 1)
	go func() {
		code, out, errs := runCI(t, cli, "wait", "--run", "run-a", "--query", "subject:welcome", "--timeout", "20")
		waited <- ciResult{code, out, errs}
	}()
	pool := testutil.PoolFor(t, dsn)
	listen := "LISTEN " + pgx.Identifier{events.Channel(project.ID)}.Sanitize()
	waitFor(t, func() bool {
		var n int
		err := pool.QueryRow(context.Background(),
			"SELECT count(*) FROM pg_stat_activity WHERE datname = current_database() AND query = $1", listen).Scan(&n)
		return err == nil && n > 0
	})
	select {
	case r := <-waited:
		t.Fatalf("wait returned before any mail was sent: %+v", r)
	default:
	}
	if err := send("run-a", "Welcome", "Your code is 482913. Confirm: https://app.example.test/verify?t=abc"); err != nil {
		t.Fatalf("send: %v", err)
	}
	r := <-waited
	code, out, errs := r.code, r.out, r.errs
	if code != exitOK || strings.TrimSpace(out) == "" || errs != "" {
		t.Fatalf("wait: code %d stdout %q stderr %q", code, out, errs)
	}
	id := strings.TrimSpace(out)

	// Run isolation: another run sees nothing, and times out with its own exit code.
	code, out, errs = runCI(t, cli, "wait", "--run", "run-b", "--timeout", "0")
	if code != exitTimeout || out != "" || !strings.Contains(errs, "timed out") {
		t.Fatalf("other run: code %d stdout %q stderr %q", code, out, errs)
	}
	// The run id also comes from the environment.
	cliRun := map[string]string{envURL: base, envProject: fmt.Sprint(project.ID), envToken: token.Secret, envRunID: "run-a"}
	if code, out, _ = runCI(t, cliRun, "wait", "--timeout", "5"); code != exitOK || strings.TrimSpace(out) != id {
		t.Fatalf("env run id: code %d stdout %q, want %s", code, out, id)
	}

	// extract waits by itself, or takes the id.
	if code, out, errs = runCI(t, cliRun, "extract"); code != exitOK || out != "482913\n" {
		t.Fatalf("extract code: %d %q %q", code, out, errs)
	}
	if code, out, errs = runCI(t, cliRun, "extract", "--link-contains", "verify"); code != exitOK || out != "https://app.example.test/verify?t=abc\n" {
		t.Fatalf("extract link: %d %q %q", code, out, errs)
	}
	if code, out, errs = runCI(t, cli, "extract", "--message", id, "--code-pattern", `confirm: (\S+)`, "--json"); code != exitNotFound ||
		!strings.Contains(out, `"value":null`) || !strings.Contains(errs, "has no code") {
		t.Fatalf("no match: %d %q %q", code, out, errs)
	}

	// A wrong token and a wrong project are plain failures with a hint, never a hang.
	bad := map[string]string{envURL: base, envProject: fmt.Sprint(project.ID), envToken: "spp_wrong"}
	if code, _, errs = runCI(t, bad, "wait", "--timeout", "0"); code != exitError || !strings.Contains(errs, "authentication failed") {
		t.Fatalf("bad token: %d %q", code, errs)
	}
	other := map[string]string{envURL: base, envProject: fmt.Sprint(project.ID + 100), envToken: token.Secret}
	if code, _, errs = runCI(t, other, "wait", "--timeout", "0"); code != exitError || !strings.Contains(errs, "404") {
		t.Fatalf("other project: %d %q", code, errs)
	}
}
