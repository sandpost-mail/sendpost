package smtpd

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"errors"
	"io"
	"log/slog"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/emersion/go-sasl"
	"github.com/emersion/go-smtp"

	"github.com/sandpost-mail/sandpost/internal/config"
	"github.com/sandpost-mail/sandpost/internal/credentials"
	"github.com/sandpost-mail/sandpost/internal/ingest"
)

const (
	goodUser = "sp_user"
	goodPass = "sps_secret"
	projectA = int64(7)
)

type fakeVerifier struct {
	mu    sync.Mutex
	calls int
	err   error
}

func (f *fakeVerifier) Verify(_ context.Context, u, p string) (credentials.Identity, error) {
	f.mu.Lock()
	f.calls++
	f.mu.Unlock()
	if f.err != nil {
		return credentials.Identity{}, f.err
	}
	if u == goodUser && p == goodPass {
		return credentials.Identity{CredentialID: 1, ProjectID: projectA}, nil
	}
	return credentials.Identity{}, credentials.ErrInvalid
}

type fakeIngester struct {
	mu   sync.Mutex
	got  []ingest.Input
	err  error
	next int64
}

func (f *fakeIngester) Ingest(_ context.Context, in ingest.Input) (int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return 0, f.err
	}
	f.got = append(f.got, in)
	f.next++
	return f.next, nil
}

func (f *fakeIngester) inputs() []ingest.Input {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]ingest.Input(nil), f.got...)
}

type harness struct {
	addr string
	v    *fakeVerifier
	ing  *fakeIngester
}

type syncBuilder struct {
	mu sync.Mutex
	sb strings.Builder
}

func (b *syncBuilder) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.sb.Write(p)
}

func (b *syncBuilder) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.sb.String()
}

func start(t *testing.T, mutate func(*Options)) (*harness, *syncBuilder) {
	t.Helper()
	v, ing := &fakeVerifier{}, &fakeIngester{}
	logs := &syncBuilder{}
	opts := Options{
		Verifier: v, Ingester: ing,
		Log:             slog.New(slog.NewTextHandler(logs, &slog.HandlerOptions{Level: slog.LevelDebug})),
		MaxMessageBytes: 1 << 20,
	}
	if mutate != nil {
		mutate(&opts)
	}
	srv, err := New(opts)
	if err != nil {
		t.Fatal(err)
	}
	ln, err := (&net.ListenConfig{}).Listen(context.Background(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- srv.Serve(ctx, ln) }()
	t.Cleanup(func() {
		cancel()
		if err := <-done; err != nil {
			t.Errorf("serve: %v", err)
		}
	})
	return &harness{addr: ln.Addr().String(), v: v, ing: ing}, logs
}

const msg = "From: a@example.test\r\nTo: b@example.test\r\nSubject: hi\r\n\r\nbody\r\n"

func dial(t *testing.T, addr string) *smtp.Client {
	t.Helper()
	c, err := smtp.Dial(addr)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close() })
	return c
}

func smtpCode(err error) int {
	var se *smtp.SMTPError
	if errors.As(err, &se) {
		return se.Code
	}
	return 0
}

func TestAuthPlainAndLoginDeliverToTheCredentialsProject(t *testing.T) {
	for name, mk := range map[string]func(u, p string) sasl.Client{
		"PLAIN": func(u, p string) sasl.Client { return sasl.NewPlainClient("", u, p) },
		"LOGIN": sasl.NewLoginClient,
	} {
		t.Run(name, func(t *testing.T) {
			h, _ := start(t, nil)
			c := dial(t, h.addr)
			if err := c.Auth(mk(goodUser, goodPass)); err != nil {
				t.Fatal(err)
			}
			if err := c.SendMail("sender@example.test", []string{"x@example.test", "y@example.test"}, strings.NewReader(msg)); err != nil {
				t.Fatal(err)
			}
			got := h.ing.inputs()
			if len(got) != 1 {
				t.Fatalf("ingested %d", len(got))
			}
			in := got[0]
			if in.ProjectID != projectA || in.EnvelopeFrom != "sender@example.test" ||
				len(in.EnvelopeTo) != 2 || !strings.Contains(string(in.Raw), "Subject: hi") {
				t.Errorf("input: %+v", in)
			}
		})
	}
}

func TestAuthIsRememberedAcrossRSETAndMessages(t *testing.T) {
	h, _ := start(t, nil)
	c := dial(t, h.addr)
	if err := c.Auth(sasl.NewPlainClient("", goodUser, goodPass)); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if err := c.SendMail("a@example.test", []string{"b@example.test"}, strings.NewReader(msg)); err != nil {
			t.Fatalf("message %d: %v", i, err)
		}
		if err := c.Reset(); err != nil {
			t.Fatal(err)
		}
	}
	if n := len(h.ing.inputs()); n != 2 {
		t.Errorf("ingested %d", n)
	}
}

func TestUnauthenticatedMailIsRejected(t *testing.T) {
	h, _ := start(t, nil)
	c := dial(t, h.addr)
	err := c.SendMail("a@example.test", []string{"b@example.test"}, strings.NewReader(msg))
	if smtpCode(err) != 530 {
		t.Fatalf("want 530, got %v", err)
	}
	if len(h.ing.inputs()) != 0 {
		t.Error("nothing may be stored without authentication")
	}
}

func TestBadCredentialsAreRejectedWithoutDetail(t *testing.T) {
	h, logs := start(t, nil)
	c := dial(t, h.addr)
	err := c.Auth(sasl.NewPlainClient("", goodUser, "wrong-secret-value"))
	if smtpCode(err) != 535 {
		t.Fatalf("want 535, got %v", err)
	}
	if out := logs.String(); strings.Contains(out, "wrong-secret-value") || strings.Contains(out, goodUser) {
		t.Errorf("credentials leaked into logs: %s", out)
	}
	if err := c.Mail("a@example.test", nil); smtpCode(err) != 530 {
		t.Errorf("session must stay unauthenticated: %v", err)
	}
}

func TestVerifierFailureIsTemporary(t *testing.T) {
	h, logs := start(t, nil)
	h.v.err = errors.New("db down: password=hunter2")
	c := dial(t, h.addr)
	err := c.Auth(sasl.NewPlainClient("", goodUser, goodPass))
	if smtpCode(err) != 454 {
		t.Fatalf("want 454, got %v", err)
	}
	if strings.Contains(err.Error(), "hunter2") {
		t.Error("internal error leaked to the client")
	}
	_ = logs
}

func TestAuthFailureRateLimitPerIP(t *testing.T) {
	h, _ := start(t, func(o *Options) { o.Limiter = NewFailureLimiter(3, time.Minute) })
	for i := 0; i < 3; i++ {
		c := dial(t, h.addr)
		if err := c.Auth(sasl.NewPlainClient("", goodUser, "bad")); smtpCode(err) != 535 {
			t.Fatalf("attempt %d: %v", i, err)
		}
	}
	calls := h.v.calls
	c := dial(t, h.addr)
	// Even the right password is refused while blocked, and the database is not hit.
	if err := c.Auth(sasl.NewPlainClient("", goodUser, goodPass)); smtpCode(err) != 454 {
		t.Fatalf("want 454 while blocked, got %v", err)
	}
	if h.v.calls != calls {
		t.Error("blocked attempts must not reach the verifier")
	}
}

func TestMaxMessageSize(t *testing.T) {
	h, _ := start(t, func(o *Options) { o.MaxMessageBytes = 1024 })
	c := dial(t, h.addr)
	if err := c.Auth(sasl.NewPlainClient("", goodUser, goodPass)); err != nil {
		t.Fatal(err)
	}
	big := msg + strings.Repeat(strings.Repeat("x", 100)+"\r\n", 40)
	err := c.SendMail("a@example.test", []string{"b@example.test"}, strings.NewReader(big))
	if smtpCode(err) != 552 {
		t.Fatalf("want 552, got %v", err)
	}
	if len(h.ing.inputs()) != 0 {
		t.Error("oversized message must not be stored")
	}
}

func TestIngestFailureIsTemporaryAndProjectGonePermanent(t *testing.T) {
	h, logs := start(t, nil)
	c := dial(t, h.addr)
	if err := c.Auth(sasl.NewPlainClient("", goodUser, goodPass)); err != nil {
		t.Fatal(err)
	}
	h.ing.err = errors.New("insert failed: secret=abc")
	err := c.SendMail("a@example.test", []string{"b@example.test"}, strings.NewReader(msg))
	if smtpCode(err) != 451 || strings.Contains(err.Error(), "secret=abc") {
		t.Fatalf("want opaque 451, got %v", err)
	}
	if !strings.Contains(logs.String(), "ingest failed") {
		t.Error("failure must be logged server side")
	}
	_ = c.Reset()
	h.ing.err = ingest.ErrProjectNotFound
	if err := c.SendMail("a@example.test", []string{"b@example.test"}, strings.NewReader(msg)); smtpCode(err) != 554 {
		t.Fatalf("want 554, got %v", err)
	}
}

func TestUnknownMechanismIsRejected(t *testing.T) {
	h, _ := start(t, nil)
	c := dial(t, h.addr)
	err := c.Auth(sasl.NewAnonymousClient("x"))
	if err == nil {
		t.Fatal("ANONYMOUS must not be accepted")
	}
}

func selfSigned(t *testing.T) (certFile, keyFile string, pool *x509.CertPool) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tpl := &x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "localhost"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
		DNSNames: []string{"localhost"}, IPAddresses: []net.IP{net.ParseIP("127.0.0.1")},
		KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		IsCA: true, BasicConstraintsValid: true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tpl, tpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	certFile, keyFile = filepath.Join(dir, "c.pem"), filepath.Join(dir, "k.pem")
	writePEM(t, certFile, "CERTIFICATE", der)
	writePEM(t, keyFile, "EC PRIVATE KEY", keyDER)
	cert, _ := x509.ParseCertificate(der)
	pool = x509.NewCertPool()
	pool.AddCert(cert)
	return certFile, keyFile, pool
}

func writePEM(t *testing.T, path, typ string, der []byte) {
	t.Helper()
	data := "-----BEGIN " + typ + "-----\n" + b64(der) + "\n-----END " + typ + "-----\n"
	if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestTLSModes(t *testing.T) {
	certFile, keyFile, roots := selfSigned(t)
	tlsCfg, err := LoadTLS(certFile, keyFile)
	if err != nil {
		t.Fatal(err)
	}
	clientTLS := &tls.Config{RootCAs: roots, ServerName: "localhost", MinVersion: tls.VersionTLS12}

	t.Run("starttls allows plaintext auth and upgrade", func(t *testing.T) {
		h, _ := start(t, func(o *Options) { o.TLSMode, o.TLSConfig = config.TLSStartTLS, tlsCfg })
		c, err := smtp.DialStartTLS(h.addr, clientTLS)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = c.Close() }()
		if err := c.Auth(sasl.NewPlainClient("", goodUser, goodPass)); err != nil {
			t.Fatal(err)
		}
		if err := c.SendMail("a@example.test", []string{"b@example.test"}, strings.NewReader(msg)); err != nil {
			t.Fatal(err)
		}
		plain := dial(t, h.addr)
		if err := plain.Auth(sasl.NewPlainClient("", goodUser, goodPass)); err != nil {
			t.Errorf("plaintext auth should work in starttls mode: %v", err)
		}
	})

	t.Run("required refuses auth before TLS", func(t *testing.T) {
		h, _ := start(t, func(o *Options) { o.TLSMode, o.TLSConfig = config.TLSRequired, tlsCfg })
		plain := dial(t, h.addr)
		if err := plain.Auth(sasl.NewPlainClient("", goodUser, goodPass)); smtpCode(err) != 523 {
			t.Errorf("want 523, got %v", err)
		}
		c, err := smtp.DialStartTLS(h.addr, clientTLS)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = c.Close() }()
		if err := c.Auth(sasl.NewPlainClient("", goodUser, goodPass)); err != nil {
			t.Errorf("auth after STARTTLS: %v", err)
		}
	})

	t.Run("implicit", func(t *testing.T) {
		h, _ := start(t, func(o *Options) { o.TLSMode, o.TLSConfig = config.TLSImplicit, tlsCfg })
		c, err := smtp.DialTLS(h.addr, clientTLS)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = c.Close() }()
		if err := c.Auth(sasl.NewPlainClient("", goodUser, goodPass)); err != nil {
			t.Fatal(err)
		}
		if err := c.SendMail("a@example.test", []string{"b@example.test"}, strings.NewReader(msg)); err != nil {
			t.Fatal(err)
		}
	})
}

func TestNewValidatesOptions(t *testing.T) {
	if _, err := New(Options{}); err == nil {
		t.Error("missing dependencies must fail")
	}
	if _, err := New(Options{Verifier: &fakeVerifier{}, Ingester: &fakeIngester{}, TLSMode: config.TLSRequired}); err == nil {
		t.Error("TLS mode without config must fail")
	}
	if _, err := LoadTLS("/nonexistent", "/nonexistent"); err == nil {
		t.Error("missing key pair must fail")
	}
}

func TestServeReturnsListenerError(t *testing.T) {
	srv, err := New(Options{Verifier: &fakeVerifier{}, Ingester: &fakeIngester{}})
	if err != nil {
		t.Fatal(err)
	}
	ln, err := (&net.ListenConfig{}).Listen(context.Background(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	_ = ln.Close()
	if err := srv.Serve(context.Background(), ln); err == nil {
		t.Error("closed listener must surface an error")
	}
}

func TestSlogAdapter(t *testing.T) {
	var sb syncBuilder
	a := slogAdapter{slog.New(slog.NewTextHandler(&sb, nil))}
	a.Printf("hello %d", 1)
	a.Println("world")
	if out := sb.String(); !strings.Contains(out, "hello 1") || !strings.Contains(out, "world") {
		t.Errorf("log: %s", out)
	}
	_ = io.Discard
}

func TestRemoteIP(t *testing.T) {
	if remoteIP(nil) != "" {
		t.Error("nil addr")
	}
	if got := remoteIP(&net.TCPAddr{IP: net.ParseIP("10.1.2.3"), Port: 5}); got != "10.1.2.3" {
		t.Errorf("got %q", got)
	}
	if got := remoteIP(&net.UnixAddr{Name: "sock"}); got != "sock" {
		t.Errorf("got %q", got)
	}
}

func TestVeryLongLinesAreAccepted(t *testing.T) {
	h, _ := start(t, nil)
	c := dial(t, h.addr)
	if err := c.Auth(sasl.NewPlainClient("", goodUser, goodPass)); err != nil {
		t.Fatal(err)
	}
	long := msg + strings.Repeat("y", 50_000) + "\r\n"
	if err := c.SendMail("a@example.test", []string{"b@example.test"}, strings.NewReader(long)); err != nil {
		t.Fatal(err)
	}
}
