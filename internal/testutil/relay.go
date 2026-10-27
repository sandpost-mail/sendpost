package testutil

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"io"
	"math/big"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/emersion/go-sasl"
	"github.com/emersion/go-smtp"
)

// RelayOptions configures a FakeRelay.
type RelayOptions struct {
	// TLS is "" (plaintext), "starttls" (offers STARTTLS) or "implicit".
	TLS string
	// User and Pass, when set, make the relay require AUTH PLAIN before MAIL.
	User, Pass string
	// CertNames are the names in the certificate (default 127.0.0.1 and localhost).
	CertNames []string
	// RejectRcpt answers a RCPT command with an error when it returns one.
	RejectRcpt func(addr string) *smtp.SMTPError
	// RejectData answers the end of DATA with this error.
	RejectData *smtp.SMTPError
}

// Envelope is one message the relay accepted.
type Envelope struct {
	From          string
	To            []string
	Data          []byte
	TLS           bool
	Authenticated bool
}

// FakeRelay is an SMTP server that records everything it is sent. Tests point
// a transport at it to prove what leaves Sandpost and what does not.
type FakeRelay struct {
	Host string
	Port int
	// Roots trusts the relay's certificate.
	Roots *x509.CertPool

	opts   RelayOptions
	mu     sync.Mutex
	conns  int
	rcpts  []string
	passes []string
	mails  []Envelope
}

// StartRelay starts a FakeRelay on a free local port, stopped at test end.
func StartRelay(t *testing.T, opts RelayOptions) *FakeRelay {
	t.Helper()
	r := &FakeRelay{Host: "127.0.0.1", opts: opts}
	ln, err := (&net.ListenConfig{}).Listen(context.Background(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	r.Port = ln.Addr().(*net.TCPAddr).Port

	srv := smtp.NewServer(&relayBackend{r: r})
	srv.Domain = "fake-relay.test"
	srv.AllowInsecureAuth = true
	srv.ReadTimeout, srv.WriteTimeout = 10*time.Second, 10*time.Second
	var serveLn net.Listener = &countingListener{Listener: ln, r: r}
	if opts.TLS != "" {
		cert, pool := selfSigned(t, opts.CertNames)
		r.Roots = pool
		cfg := &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS12}
		srv.TLSConfig = cfg
		if opts.TLS == "implicit" {
			serveLn = tls.NewListener(serveLn, cfg)
		}
	}
	go func() { _ = srv.Serve(serveLn) }()
	t.Cleanup(func() { _ = srv.Close() })
	return r
}

// Connections is the number of TCP connections the relay accepted.
func (r *FakeRelay) Connections() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.conns
}

// Envelopes returns the messages the relay accepted.
func (r *FakeRelay) Envelopes() []Envelope {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]Envelope(nil), r.mails...)
}

// RcptAttempts returns every RCPT TO address a client sent, accepted or not.
func (r *FakeRelay) RcptAttempts() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.rcpts...)
}

// SeenPasswords returns the passwords clients authenticated with.
func (r *FakeRelay) SeenPasswords() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.passes...)
}

type countingListener struct {
	net.Listener
	r *FakeRelay
}

func (l *countingListener) Accept() (net.Conn, error) {
	c, err := l.Listener.Accept()
	if err == nil {
		l.r.mu.Lock()
		l.r.conns++
		l.r.mu.Unlock()
	}
	return c, err
}

type relayBackend struct{ r *FakeRelay }

func (b *relayBackend) NewSession(c *smtp.Conn) (smtp.Session, error) {
	s := &relaySession{r: b.r, c: c}
	if b.r.opts.User != "" {
		return &relayAuthSession{relaySession: s}, nil
	}
	return s, nil
}

type relaySession struct {
	r      *FakeRelay
	c      *smtp.Conn
	authed bool
	from   string
	to     []string
}

func (s *relaySession) Mail(from string, _ *smtp.MailOptions) error {
	if s.r.opts.User != "" && !s.authed {
		return &smtp.SMTPError{Code: 530, EnhancedCode: smtp.EnhancedCode{5, 7, 0}, Message: "authentication required"}
	}
	s.from, s.to = from, nil
	return nil
}

func (s *relaySession) Rcpt(to string, _ *smtp.RcptOptions) error {
	s.r.mu.Lock()
	s.r.rcpts = append(s.r.rcpts, to)
	s.r.mu.Unlock()
	if f := s.r.opts.RejectRcpt; f != nil {
		if err := f(to); err != nil {
			return err
		}
	}
	s.to = append(s.to, to)
	return nil
}

func (s *relaySession) Data(rd io.Reader) error {
	data, err := io.ReadAll(rd)
	if err != nil {
		return err
	}
	if e := s.r.opts.RejectData; e != nil {
		return e
	}
	_, tlsOn := s.c.TLSConnectionState()
	s.r.mu.Lock()
	s.r.mails = append(s.r.mails, Envelope{From: s.from, To: append([]string(nil), s.to...), Data: data, TLS: tlsOn, Authenticated: s.authed})
	s.r.mu.Unlock()
	return nil
}

func (s *relaySession) Reset()        { s.from, s.to = "", nil }
func (s *relaySession) Logout() error { return nil }

type relayAuthSession struct{ *relaySession }

func (s *relayAuthSession) AuthMechanisms() []string { return []string{sasl.Plain} }

func (s *relayAuthSession) Auth(mech string) (sasl.Server, error) {
	if mech != sasl.Plain {
		return nil, smtp.ErrAuthUnknownMechanism
	}
	return sasl.NewPlainServer(func(_, user, pass string) error {
		s.r.mu.Lock()
		s.r.passes = append(s.r.passes, pass)
		s.r.mu.Unlock()
		if user != s.r.opts.User || pass != s.r.opts.Pass {
			return smtp.ErrAuthFailed
		}
		s.authed = true
		return nil
	}), nil
}

func selfSigned(t *testing.T, names []string) (tls.Certificate, *x509.CertPool) {
	t.Helper()
	if len(names) == 0 {
		names = []string{"127.0.0.1", "localhost"}
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "fake relay"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
		KeyUsage: x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true, IsCA: true,
	}
	for _, n := range names {
		if ip := net.ParseIP(n); ip != nil {
			tmpl.IPAddresses = append(tmpl.IPAddresses, ip)
		} else {
			tmpl.DNSNames = append(tmpl.DNSNames, strings.ToLower(n))
		}
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	leaf, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	pool := x509.NewCertPool()
	pool.AddCert(leaf)
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key, Leaf: leaf}, pool
}
