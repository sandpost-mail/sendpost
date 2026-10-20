// Package auth handles password login and server side browser sessions.
//
// The cookie carries a random 256 bit value. Only its HMAC is stored, so a
// database leak does not yield usable sessions. The CSRF token of a session is
// derived from the stored session id with a separate key, which needs no extra
// storage and is bound to that session.
package auth

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/sandpost-mail/sandpost/internal/authz"
	"github.com/sandpost-mail/sandpost/internal/passwords"
	"github.com/sandpost-mail/sandpost/internal/store/sqlcgen"
	"github.com/sandpost-mail/sandpost/internal/users"
)

const (
	// SessionTTL is the absolute lifetime of a session.
	SessionTTL     = 24 * time.Hour
	sessionKeyCtx  = "sandpost/session/v1"
	csrfKeyCtx     = "sandpost/csrf/v1"
	maxEmailLength = 254
)

// ErrInvalidCredentials is returned for an unknown user, a wrong password and
// a disabled account alike, so the response does not reveal which.
var ErrInvalidCredentials = errors.New("invalid email or password")

// ErrNoSession is returned when a cookie does not name a live session.
var ErrNoSession = errors.New("no valid session")

// Session is a newly created login.
type Session struct {
	// Token is the cookie value, shown to the client once.
	Token     string
	CSRFToken string
	ExpiresAt time.Time
	User      users.User
}

// Service logs users in and resolves cookies to principals.
type Service struct {
	q          *sqlcgen.Queries
	sessionKey []byte
	csrfKey    []byte
	now        func() time.Time
	// dummyHash is verified for unknown emails so response time does not
	// reveal whether an account exists.
	dummyHash string
}

// NewService returns a Service. Dedicated keys are derived from SECRET_KEY.
func NewService(db sqlcgen.DBTX, secretKey string) (*Service, error) {
	dummy, err := passwords.Hash("not-a-real-password")
	if err != nil {
		return nil, fmt.Errorf("dummy hash: %w", err)
	}
	return &Service{
		q: sqlcgen.New(db), sessionKey: derive(secretKey, sessionKeyCtx), csrfKey: derive(secretKey, csrfKeyCtx),
		now: time.Now, dummyHash: dummy,
	}, nil
}

// Login verifies the password and starts a session.
func (s *Service) Login(ctx context.Context, email, password string) (Session, error) {
	if len(email) > maxEmailLength || len(password) > 1024 {
		return Session{}, ErrInvalidCredentials
	}
	row, err := s.q.GetUserForLogin(ctx, email)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		_ = passwords.Verify(password, s.dummyHash)
		return Session{}, ErrInvalidCredentials
	case err != nil:
		return Session{}, fmt.Errorf("lookup user: %w", err)
	}
	if err := passwords.Verify(password, row.PasswordHash); err != nil {
		if errors.Is(err, passwords.ErrMismatch) {
			return Session{}, ErrInvalidCredentials
		}
		return Session{}, fmt.Errorf("verify password: %w", err)
	}
	if row.DisabledAt != nil {
		return Session{}, ErrInvalidCredentials
	}
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return Session{}, fmt.Errorf("random bytes: %w", err)
	}
	token := base64.RawURLEncoding.EncodeToString(raw)
	id := s.sessionID(token)
	expires := s.now().Add(SessionTTL)
	if err := s.q.CreateSession(ctx, sqlcgen.CreateSessionParams{ID: id, UserID: row.ID, ExpiresAt: expires}); err != nil {
		return Session{}, fmt.Errorf("create session: %w", err)
	}
	_, _ = s.q.DeleteExpiredSessions(ctx) // best effort housekeeping
	return Session{
		Token: token, CSRFToken: s.csrf(id), ExpiresAt: expires,
		User: users.User{
			ID: row.ID, Email: row.Email, Name: row.Name, IsAdmin: row.IsAdmin,
			MustChangePassword: row.MustChangePassword, CreatedAt: row.CreatedAt,
		},
	}, nil
}

// Authenticate resolves a cookie value to a Principal.
func (s *Service) Authenticate(ctx context.Context, token string) (authz.Principal, error) {
	if token == "" || len(token) > 128 {
		return authz.Principal{}, ErrNoSession
	}
	id := s.sessionID(token)
	row, err := s.q.GetSessionUser(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return authz.Principal{}, ErrNoSession
	}
	if err != nil {
		return authz.Principal{}, fmt.Errorf("lookup session: %w", err)
	}
	return authz.Principal{
		Kind: authz.KindSession, UserID: row.ID, IsAdmin: row.IsAdmin, MustChangePassword: row.MustChangePassword,
		Ceiling: authz.RoleAdmin, SessionID: id, ExpiresAt: row.ExpiresAt,
	}, nil
}

// Logout ends the session of p. It is a no-op for other kinds.
func (s *Service) Logout(ctx context.Context, p authz.Principal) error {
	if p.Kind != authz.KindSession {
		return nil
	}
	if err := s.q.DeleteSession(ctx, p.SessionID); err != nil {
		return fmt.Errorf("delete session: %w", err)
	}
	return nil
}

// CSRFToken returns the CSRF token bound to the session of p.
func (s *Service) CSRFToken(p authz.Principal) string { return s.csrf(p.SessionID) }

// CheckCSRF reports whether got is the CSRF token of the session of p.
func (s *Service) CheckCSRF(p authz.Principal, got string) bool {
	return got != "" && hmac.Equal([]byte(s.csrf(p.SessionID)), []byte(got))
}

func (s *Service) sessionID(token string) []byte {
	mac := hmac.New(sha256.New, s.sessionKey)
	mac.Write([]byte(token))
	return mac.Sum(nil)
}

func (s *Service) csrf(sessionID []byte) string {
	mac := hmac.New(sha256.New, s.csrfKey)
	mac.Write(sessionID)
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

func derive(secretKey, context string) []byte {
	mac := hmac.New(sha256.New, []byte(secretKey))
	mac.Write([]byte(context))
	return mac.Sum(nil)
}
