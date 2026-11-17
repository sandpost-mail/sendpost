package delivery

import (
	"context"
	"crypto/tls"
	"errors"
	"io"
	"net"
	"net/textproto"
	"strings"
	"time"

	"github.com/emersion/go-smtp"
)

// errNoStartTLS means the relay's EHLO answer does not list STARTTLS (or it
// only speaks HELO). Sandpost never falls back to plaintext.
var errNoStartTLS = errors.New("the relay does not offer STARTTLS")

// errDataAfterStartTLS means the relay sent bytes after its STARTTLS reply and
// before the handshake: they could be injected plaintext (CVE-2011-0411 style).
var errDataAfterStartTLS = errors.New("the relay sent data before the TLS handshake")

// startTLS speaks the plaintext part of a STARTTLS session on conn (greeting,
// EHLO, STARTTLS) and completes the TLS handshake, every step bounded by
// commandTimeout. go-smtp's NewClientStartTLS would run these steps with its
// 5 minute defaults and introduce itself as "localhost", and it reports a
// missing STARTTLS only as an error string.
//
// The returned connection replays a synthetic greeting so a fresh smtp.Client
// on it starts with EHLO, as RFC 3207 requires after the upgrade.
func startTLS(ctx context.Context, conn net.Conn, cfg *tls.Config, password string) (net.Conn, *RelayError) {
	tp := textproto.NewConn(conn) // never closed here: closing it closes conn
	step := func() { _ = conn.SetDeadline(time.Now().Add(commandTimeout)) }

	step()
	if _, _, err := tp.ReadResponse(220); err != nil {
		return nil, classify(stageConnect, smtpErr(err), password)
	}
	step()
	_, ehlo, err := command(tp, 250, "EHLO "+localName)
	var se *smtp.SMTPError
	switch {
	case errors.As(err, &se) && (se.Code == 500 || se.Code == 502):
		return nil, classify(stageTLS, errNoStartTLS, password) // HELO only: no extensions
	case err != nil:
		return nil, classify(stageConnect, err, password)
	case !hasExtension(ehlo, "STARTTLS"):
		return nil, classify(stageTLS, errNoStartTLS, password)
	}
	step()
	if _, _, err := command(tp, 220, "STARTTLS"); err != nil {
		return nil, classify(stageTLS, err, password)
	}
	if tp.R.Buffered() > 0 {
		return nil, classify(stageTLS, errDataAfterStartTLS, password)
	}
	tc := tls.Client(conn, cfg)
	step()
	if err := tc.HandshakeContext(ctx); err != nil {
		return nil, classify(stageTLS, err, password)
	}
	_ = conn.SetDeadline(time.Time{})
	return &greetedConn{Conn: tc, r: io.MultiReader(strings.NewReader("220 "+localName+" tls\r\n"), tc)}, nil
}

func command(tp *textproto.Conn, expect int, line string) (int, string, error) {
	id, err := tp.Cmd("%s", line)
	if err != nil {
		return 0, "", err
	}
	tp.StartResponse(id)
	defer tp.EndResponse(id)
	code, msg, err := tp.ReadResponse(expect)
	return code, msg, smtpErr(err)
}

// smtpErr turns a protocol reply error into the *smtp.SMTPError classify knows.
func smtpErr(err error) error {
	var pe *textproto.Error
	if errors.As(err, &pe) {
		return &smtp.SMTPError{Code: pe.Code, Message: pe.Msg}
	}
	return err
}

// hasExtension reports whether an EHLO reply (first line the domain, one
// extension per following line) lists ext.
func hasExtension(reply, ext string) bool {
	lines := strings.Split(reply, "\n")
	for _, l := range lines[1:] {
		if f := strings.Fields(l); len(f) > 0 && strings.EqualFold(f[0], ext) {
			return true
		}
	}
	return false
}

// greetedConn is the TLS connection after STARTTLS, reading a synthetic
// greeting first.
type greetedConn struct {
	net.Conn
	r io.Reader
}

func (c *greetedConn) Read(p []byte) (int, error) { return c.r.Read(p) }
