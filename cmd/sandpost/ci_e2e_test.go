package main

import (
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

// TestCIHelpersEndToEnd drives the real binary entry point against a real
// server: mail in over SMTP with an X-Run-Id header, then "sandpost wait" and
// "sandpost extract" with a project token, including a wait that is released
// by mail arriving later and a run that never sees another run's mail.
func TestCIHelpersEndToEnd(t *testing.T) {
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
	e, _, _ := testEnv(vars)
	sctx, cancel := context.WithCancel(ctx)
	done := make(chan int, 1)
	go func() { done <- run(sctx, e, []string{"serve"}) }()
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(20 * time.Second):
		}
	})
	base := "http://" + httpAddr
	if code := waitStatus(t, base+"/readyz"); code != http.StatusOK {
		t.Fatalf("readyz %d", code)
	}
	waitPort(t, smtpAddr)

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

	send := func(runID, subject, text string) {
		t.Helper()
		c, err := smtp.Dial(smtpAddr)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = c.Close() }()
		if err := c.Auth(sasl.NewPlainClient("", cred.Username, cred.Secret)); err != nil {
			t.Fatal(err)
		}
		msg := "From: App <app@example.test>\r\nTo: dev@example.test\r\nSubject: " + subject + "\r\nX-Run-Id: " + runID + "\r\n\r\n" + text + "\r\n"
		if err := c.SendMail("app@example.test", []string{"dev@example.test"}, strings.NewReader(msg)); err != nil {
			t.Fatal(err)
		}
	}

	cli := map[string]string{envURL: base, envProject: fmt.Sprint(project.ID), envToken: token.Secret}

	// A wait started before the mail is released by it.
	go func() {
		time.Sleep(700 * time.Millisecond)
		send("run-a", "Welcome", "Your code is 482913. Confirm: https://app.example.test/verify?t=abc")
	}()
	code, out, errs := runCI(t, cli, "wait", "--run", "run-a", "--query", "subject:welcome", "--timeout", "20")
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
