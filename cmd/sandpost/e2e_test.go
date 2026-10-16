package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/emersion/go-sasl"
	"github.com/emersion/go-smtp"

	"github.com/sandpost-mail/sandpost/internal/testutil"
)

// TestEndToEndSMTPToLiveUI is the M1 exit criterion: an app sends through
// authenticated SMTP and a client subscribed to the project's SSE stream sees
// the message live, then reads it through the API.
func TestEndToEndSMTPToLiveUI(t *testing.T) {
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
	base := "http://" + httpAddr
	if code := waitStatus(t, base+"/readyz"); code != http.StatusOK {
		t.Fatalf("readyz %d", code)
	}
	waitPort(t, smtpAddr)

	var project struct {
		ID int64 `json:"id"`
	}
	apiJSON(t, http.MethodPost, base+"/_/api/v1/projects", map[string]string{"name": "Demo"}, http.StatusCreated, &project)
	var cred struct{ Username, Secret string }
	apiJSON(t, http.MethodPost, fmt.Sprintf("%s/_/api/v1/projects/%d/credentials", base, project.ID),
		map[string]string{"label": "e2e"}, http.StatusCreated, &cred)

	// Live stream first, like an open browser tab.
	rctx, rcancel := context.WithTimeout(ctx, 30*time.Second)
	defer rcancel()
	req, _ := http.NewRequestWithContext(rctx, http.MethodGet, fmt.Sprintf("%s/_/api/v1/projects/%d/events", base, project.ID), nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	sc := bufio.NewScanner(resp.Body)
	waitLine(t, sc, "event: ready")

	// An unauthenticated sender is refused, a wrong secret too.
	c, err := smtp.Dial(smtpAddr)
	if err != nil {
		t.Fatal(err)
	}
	if err := c.SendMail("a@example.test", []string{"b@example.test"}, strings.NewReader("Subject: no\r\n\r\nx")); err == nil {
		t.Error("unauthenticated mail must be rejected")
	}
	_ = c.Close()

	c, err = smtp.Dial(smtpAddr)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = c.Close() }()
	if err := c.Auth(sasl.NewPlainClient("", cred.Username, cred.Secret)); err != nil {
		t.Fatal(err)
	}
	body := "From: App <app@example.test>\r\nTo: dev@example.test\r\nSubject: Welcome e2e\r\n\r\nClick https://example.test/verify?t=1\r\n"
	if err := c.SendMail("app@example.test", []string{"dev@example.test"}, strings.NewReader(body)); err != nil {
		t.Fatal(err)
	}
	waitLine(t, sc, "event: message.created")

	var page struct {
		Items []struct {
			ID      int64  `json:"id"`
			Subject string `json:"subject"`
		} `json:"items"`
	}
	apiJSON(t, http.MethodGet, fmt.Sprintf("%s/_/api/v1/projects/%d/messages?q=welcome", base, project.ID), nil, http.StatusOK, &page)
	if len(page.Items) != 1 || page.Items[0].Subject != "Welcome e2e" {
		t.Fatalf("messages: %+v", page)
	}
	var detail struct {
		Links []string `json:"links"`
	}
	apiJSON(t, http.MethodGet, fmt.Sprintf("%s/_/api/v1/projects/%d/messages/%d", base, project.ID, page.Items[0].ID), nil, http.StatusOK, &detail)
	if len(detail.Links) != 1 || detail.Links[0] != "https://example.test/verify?t=1" {
		t.Errorf("links: %v", detail.Links)
	}

	// The bootstrap admin exists, hashed.
	pool := testutil.PoolFor(t, dsn)
	var hash string
	if err := pool.QueryRow(ctx, "SELECT password_hash FROM users WHERE email='admin@example.test'").Scan(&hash); err != nil ||
		!strings.HasPrefix(hash, "$argon2id$") {
		t.Errorf("bootstrap admin: %q %v", hash, err)
	}

	// Graceful shutdown ends the open SSE stream too.
	cancel()
	select {
	case code := <-done:
		if code != exitOK {
			t.Fatalf("serve exit %d stderr %q", code, errb)
		}
	case <-time.After(20 * time.Second):
		t.Fatal("serve did not stop")
	}
	if strings.Contains(errb.String(), cred.Secret) || strings.Contains(errb.String(), "a-long-admin-password") {
		t.Error("a secret leaked into the logs")
	}
}

func apiJSON(t *testing.T, method, url string, body any, want int, out any) {
	t.Helper()
	var rdr io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rdr = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(context.Background(), method, url, rdr)
	if err != nil {
		t.Fatal(err)
	}
	if method != http.MethodGet {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	data, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != want {
		t.Fatalf("%s %s: %d %s", method, url, resp.StatusCode, data)
	}
	if out != nil {
		if err := json.Unmarshal(data, out); err != nil {
			t.Fatal(err)
		}
	}
}

func waitLine(t *testing.T, sc *bufio.Scanner, want string) {
	t.Helper()
	for sc.Scan() {
		if strings.Contains(sc.Text(), want) {
			return
		}
	}
	t.Fatalf("stream ended before %q (%v)", want, sc.Err())
}

func waitPort(t *testing.T, addr string) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if c, err := (&net.Dialer{}).DialContext(context.Background(), "tcp", addr); err == nil {
			_ = c.Close()
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("nothing listening on %s", addr)
}
