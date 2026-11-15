package delivery_test

import (
	"bufio"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"io"
	"math/big"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sandpost-mail/sandpost/internal/delivery"
)

// scripted runs handle on every connection to a local port and closes the
// connection afterwards. It returns the port.
func scripted(t *testing.T, handle func(net.Conn)) int {
	t.Helper()
	return serve(t, nil, handle)
}

func serve(t *testing.T, cfg *tls.Config, handle func(net.Conn)) int {
	t.Helper()
	ln, err := (&net.ListenConfig{}).Listen(context.Background(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	if cfg != nil {
		ln = tls.NewListener(ln, cfg)
	}
	var wg sync.WaitGroup
	t.Cleanup(func() { _ = ln.Close(); wg.Wait() })
	wg.Go(func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			wg.Go(func() {
				defer func() { _ = c.Close() }()
				_ = c.SetDeadline(time.Now().Add(10 * time.Second))
				handle(c)
			})
		}
	})
	return ln.Addr().(*net.TCPAddr).Port
}

// miniRelay is a scripted SMTP server for the cases the go-smtp based fake
// relay can not produce: other AUTH mechanisms, and connections that break at
// precise points.
type miniRelay struct {
	// ext are the EHLO extension lines, for example "AUTH LOGIN".
	ext []string
	// afterDot runs after the final dot instead of the 250 reply.
	afterDot func(c net.Conn)

	mu    sync.Mutex
	auths []string // "mech user pass"
	mails int
	dots  int
}

func (m *miniRelay) handle(c net.Conn) {
	r := bufio.NewReader(c)
	say := func(s string) { _, _ = c.Write([]byte(s + "\r\n")) }
	read := func() string { l, _ := r.ReadString('\n'); return strings.TrimRight(l, "\r\n") }
	say("220 mini ready")
	for {
		line := read()
		verb := strings.ToUpper(strings.SplitN(line, " ", 2)[0])
		switch verb {
		case "":
			return
		case "EHLO":
			lines := append([]string{"mini"}, m.ext...)
			for i, l := range lines {
				if i == len(lines)-1 {
					say("250 " + l)
				} else {
					say("250-" + l)
				}
			}
		case "AUTH":
			args := strings.Fields(line)[1:]
			if len(args) == 0 || !strings.EqualFold(args[0], "LOGIN") {
				say("504 unsupported")
				continue
			}
			ir := ""
			if len(args) > 1 { // initial response: the user name
				ir = args[1]
			} else {
				say("334 " + base64.StdEncoding.EncodeToString([]byte("Username:")))
				ir = read()
			}
			user, _ := base64.StdEncoding.DecodeString(ir)
			say("334 " + base64.StdEncoding.EncodeToString([]byte("Password:")))
			pass, _ := base64.StdEncoding.DecodeString(read())
			m.mu.Lock()
			m.auths = append(m.auths, "LOGIN "+string(user)+" "+string(pass))
			m.mu.Unlock()
			say("235 ok")
		case "MAIL":
			m.mu.Lock()
			m.mails++
			m.mu.Unlock()
			say("250 ok")
		case "RCPT", "NOOP", "RSET":
			say("250 ok")
		case "DATA":
			say("354 go")
			for line := read(); line != "."; line = read() {
				_ = line // discard the message body
			}
			m.mu.Lock()
			m.dots++
			m.mu.Unlock()
			if m.afterDot != nil {
				m.afterDot(c)
				return
			}
			say("250 queued")
		case "QUIT":
			say("221 bye")
			return
		default:
			say("502 unknown")
		}
	}
}

func (m *miniRelay) counts() (auths []string, mails, dots int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]string(nil), m.auths...), m.mails, m.dots
}

// tlsPair returns a server certificate for 127.0.0.1 and a client relay that
// trusts it.
func tlsPair(t *testing.T) (*tls.Config, *delivery.SMTPRelay) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "mini"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
		KeyUsage: x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true, IsCA: true, IPAddresses: []net.IP{net.ParseIP("127.0.0.1")},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	leaf, _ := x509.ParseCertificate(der)
	roots := x509.NewCertPool()
	roots.AddCert(leaf)
	server := &tls.Config{Certificates: []tls.Certificate{{Certificate: [][]byte{der}, PrivateKey: key, Leaf: leaf}}, MinVersion: tls.VersionTLS12}
	return server, &delivery.SMTPRelay{TLSConfig: &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS12}}
}

func TestSendAuthMechanisms(t *testing.T) {
	tests := []struct {
		name      string
		ext       []string
		permanent bool
		wantAuth  string
	}{
		{"login only", []string{"AUTH LOGIN"}, false, "LOGIN u p4ss"},
		{"no usable mechanism", []string{"AUTH CRAM-MD5 XOAUTH2"}, true, ""},
		{"no auth at all", nil, true, ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			srv, client := tlsPair(t)
			m := &miniRelay{ext: tc.ext}
			port := serve(t, srv, m.handle)
			cfg := delivery.RelayConfig{Host: "127.0.0.1", Port: port, TLSMode: delivery.TLSImplicit, Username: "u", Password: "p4ss"}
			res := client.Send(ctx(t), cfg, outbound(t, "", "a@corp.example"))
			auths, mails, _ := m.counts()
			if tc.permanent {
				assertPermanent(t, res[0], "no usable AUTH")
				if mails != 0 || len(auths) != 0 {
					t.Errorf("mail %d / auth %v sent without usable AUTH", mails, auths)
				}
				return
			}
			if !res[0].Accepted() || len(auths) != 1 || auths[0] != tc.wantAuth {
				t.Errorf("result %+v auths %v", res[0].Err, auths)
			}
		})
	}
}

// TestSendUnknownOutcomeAfterEndOfData pins U3-3: when the connection breaks
// after the final dot the relay may have queued the message. The failure stays
// transient (retried, like any MTA) and says so.
func TestSendUnknownOutcomeAfterEndOfData(t *testing.T) {
	m := &miniRelay{afterDot: func(net.Conn) {}} // hang up without a reply
	port := scripted(t, m.handle)
	res := (&delivery.SMTPRelay{}).Send(ctx(t), delivery.RelayConfig{Host: "127.0.0.1", Port: port, TLSMode: delivery.TLSNone},
		outbound(t, "", "a@corp.example"))
	if res[0].Err == nil || res[0].Err.Permanent {
		t.Fatalf("want a transient error, got %+v", res[0].Err)
	}
	if !strings.HasPrefix(res[0].Err.Text, "outcome unknown after end of data") {
		t.Errorf("text %q", res[0].Err.Text)
	}
	if _, _, dots := m.counts(); dots != 1 {
		t.Errorf("dots %d", dots)
	}
}

// TestStartTLSPreamble checks the hand-written STARTTLS steps: a relay that
// does not list STARTTLS (or only speaks HELO) fails permanently without any
// error string matching, data sent before the handshake is refused, and a
// relay that stops answering is cut off by the context.
func TestStartTLSPreamble(t *testing.T) {
	t.Run("not offered", func(t *testing.T) {
		m := &miniRelay{ext: []string{"PIPELINING", "SIZE 1000"}}
		port := scripted(t, m.handle)
		res := (&delivery.SMTPRelay{}).Check(ctx(t), delivery.RelayConfig{Host: "127.0.0.1", Port: port, TLSMode: delivery.TLSStartTLS})
		if res.OK || res.Error != "tls failed: the relay does not offer STARTTLS" {
			t.Errorf("result %+v", res)
		}
	})
	t.Run("helo only", func(t *testing.T) {
		port := scripted(t, func(c net.Conn) {
			r := bufio.NewReader(c)
			_, _ = c.Write([]byte("220 old\r\n"))
			_, _ = r.ReadString('\n')
			_, _ = c.Write([]byte("502 no ehlo\r\n"))
		})
		res := (&delivery.SMTPRelay{}).Check(ctx(t), delivery.RelayConfig{Host: "127.0.0.1", Port: port, TLSMode: delivery.TLSStartTLS})
		if res.OK || res.Error != "tls failed: the relay does not offer STARTTLS" {
			t.Errorf("result %+v", res)
		}
	})
	t.Run("injected data after STARTTLS", func(t *testing.T) {
		port := scripted(t, func(c net.Conn) {
			r := bufio.NewReader(c)
			_, _ = c.Write([]byte("220 x\r\n"))
			_, _ = r.ReadString('\n')
			_, _ = c.Write([]byte("250-x\r\n250 STARTTLS\r\n"))
			_, _ = r.ReadString('\n')
			_, _ = c.Write([]byte("220 go ahead\r\n250 injected\r\n"))
			_, _ = r.ReadString('\n')
		})
		res := (&delivery.SMTPRelay{}).Send(ctx(t), delivery.RelayConfig{Host: "127.0.0.1", Port: port, TLSMode: delivery.TLSStartTLS},
			outbound(t, "", "a@corp.example"))
		assertPermanent(t, res[0], "before the TLS handshake")
	})
	t.Run("silent after greeting is bounded by the context", func(t *testing.T) {
		port := scripted(t, func(c net.Conn) {
			_, _ = c.Write([]byte("220 x\r\n"))
			_, _ = io.Copy(io.Discard, c) // reads EHLO, never answers
		})
		cctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
		defer cancel()
		start := time.Now()
		res := (&delivery.SMTPRelay{}).Check(cctx, delivery.RelayConfig{Host: "127.0.0.1", Port: port, TLSMode: delivery.TLSStartTLS})
		if res.OK || time.Since(start) > 3*time.Second {
			t.Errorf("result %+v after %v", res, time.Since(start))
		}
	})
}
