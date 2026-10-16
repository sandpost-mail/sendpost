package smtpd

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net"

	"github.com/emersion/go-sasl"
	"github.com/emersion/go-smtp"

	"github.com/sandpost-mail/sandpost/internal/credentials"
	"github.com/sandpost-mail/sandpost/internal/ingest"
)

var (
	errAuthRequired = &smtp.SMTPError{
		Code: 530, EnhancedCode: smtp.EnhancedCode{5, 7, 0}, Message: "Authentication required",
	}
	errTooManyFailures = &smtp.SMTPError{
		Code: 454, EnhancedCode: smtp.EnhancedCode{4, 7, 0},
		Message: "Too many authentication failures, try again later",
	}
	errAuthTemporary = &smtp.SMTPError{
		Code: 454, EnhancedCode: smtp.EnhancedCode{4, 7, 0}, Message: "Temporary authentication failure",
	}
	errStoreTemporary = &smtp.SMTPError{
		Code: 451, EnhancedCode: smtp.EnhancedCode{4, 3, 0}, Message: "Temporary failure storing the message",
	}
	errProjectGone = &smtp.SMTPError{
		Code: 554, EnhancedCode: smtp.EnhancedCode{5, 7, 1}, Message: "Project no longer exists",
	}
)

type backend struct {
	verifier Verifier
	ingester Ingester
	limiter  *FailureLimiter
	log      *slog.Logger
}

// NewSession implements smtp.Backend.
func (b *backend) NewSession(c *smtp.Conn) (smtp.Session, error) {
	return &session{b: b, ip: remoteIP(c.Conn().RemoteAddr())}, nil
}

func remoteIP(a net.Addr) string {
	if a == nil {
		return ""
	}
	host, _, err := net.SplitHostPort(a.String())
	if err != nil {
		return a.String()
	}
	return host
}

// session is one SMTP connection. Auth state survives RSET, the envelope does not.
type session struct {
	b  *backend
	ip string

	identity *credentials.Identity
	from     string
	to       []string
}

var _ smtp.AuthSession = (*session)(nil)

// AuthMechanisms implements smtp.AuthSession.
func (s *session) AuthMechanisms() []string { return []string{sasl.Plain, sasl.Login} }

// Auth implements smtp.AuthSession.
func (s *session) Auth(mech string) (sasl.Server, error) {
	switch mech {
	case sasl.Plain:
		return sasl.NewPlainServer(func(_, username, password string) error {
			return s.authenticate(username, password)
		}), nil
	case sasl.Login:
		return newLoginServer(s.authenticate), nil
	}
	return nil, smtp.ErrAuthUnknownMechanism
}

func (s *session) authenticate(username, password string) error {
	if s.b.limiter.Blocked(s.ip) {
		s.b.log.Warn("smtp auth blocked by rate limit", "ip", s.ip)
		return errTooManyFailures
	}
	ctx, cancel := context.WithTimeout(context.Background(), ingestTimeout)
	defer cancel()
	id, err := s.b.verifier.Verify(ctx, username, password)
	switch {
	case errors.Is(err, credentials.ErrInvalid):
		s.b.limiter.Fail(s.ip)
		// The username is deliberately not logged: clients often paste a
		// secret into the wrong field.
		s.b.log.Warn("smtp auth failed", "ip", s.ip)
		return smtp.ErrAuthFailed
	case err != nil:
		s.b.log.Error("smtp auth error", "error", err)
		return errAuthTemporary
	}
	s.identity = &id
	s.b.log.Debug("smtp auth ok", "project_id", id.ProjectID, "credential_id", id.CredentialID)
	return nil
}

// Mail implements smtp.Session. Nothing is accepted before authentication.
func (s *session) Mail(from string, _ *smtp.MailOptions) error {
	if s.identity == nil {
		return errAuthRequired
	}
	s.from = from
	s.to = nil
	return nil
}

// Rcpt implements smtp.Session.
func (s *session) Rcpt(to string, _ *smtp.RcptOptions) error {
	if s.identity == nil {
		return errAuthRequired
	}
	s.to = append(s.to, to)
	return nil
}

// Data implements smtp.Session. The server already enforces MaxMessageBytes
// and returns 552 to the client when it is exceeded.
func (s *session) Data(r io.Reader) error {
	if s.identity == nil {
		return errAuthRequired
	}
	raw, err := io.ReadAll(r)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), ingestTimeout)
	defer cancel()
	id, err := s.b.ingester.Ingest(ctx, ingest.Input{
		ProjectID: s.identity.ProjectID, EnvelopeFrom: s.from, EnvelopeTo: s.to, Raw: raw,
	})
	switch {
	case errors.Is(err, ingest.ErrProjectNotFound):
		return errProjectGone
	case err != nil:
		s.b.log.Error("ingest failed", "project_id", s.identity.ProjectID, "error", err)
		return errStoreTemporary
	}
	s.b.log.Info("message captured", "project_id", s.identity.ProjectID, "message_id", id, "bytes", len(raw))
	return nil
}

// Reset implements smtp.Session.
func (s *session) Reset() {
	s.from = ""
	s.to = nil
}

// Logout implements smtp.Session.
func (s *session) Logout() error { return nil }
