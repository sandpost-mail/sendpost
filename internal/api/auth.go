package api

import (
	"errors"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/sandpost-mail/sandpost/internal/auth"
	"github.com/sandpost-mail/sandpost/internal/authz"
	"github.com/sandpost-mail/sandpost/internal/smtpd"
	"github.com/sandpost-mail/sandpost/internal/users"
)

const (
	cookieName       = "sandpost_session"
	hostCookieName   = "__Host-" + cookieName
	loginWindow      = 5 * time.Minute
	maxLoginsPerIP   = 20
	maxLoginsPerUser = 10
)

// loginLimits throttles failed logins per source address and per account
// name. Both use a sliding window, see smtpd.FailureLimiter. Behind a reverse
// proxy every client shares the proxy address, so the per address limit is
// deliberately generous.
type loginLimits struct {
	ip, account *smtpd.FailureLimiter
}

func newLoginLimits() *loginLimits {
	return &loginLimits{
		ip:      smtpd.NewFailureLimiter(maxLoginsPerIP, loginWindow),
		account: smtpd.NewFailureLimiter(maxLoginsPerUser, loginWindow),
	}
}

func (l *loginLimits) blocked(ip, account string) bool {
	return l.ip.Blocked(ip) || l.account.Blocked(account)
}

func (l *loginLimits) fail(ip, account string) {
	l.ip.Fail(ip)
	l.account.Fail(account)
}

func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

func (s *server) cookieName() string {
	if s.CookieSecure {
		return hostCookieName
	}
	return cookieName
}

func (s *server) setSessionCookie(w http.ResponseWriter, value string, expires time.Time) {
	maxAge := int(time.Until(expires).Seconds())
	if value == "" {
		maxAge = -1
	}
	http.SetCookie(w, &http.Cookie{
		Name: s.cookieName(), Value: value, Path: "/", Expires: expires, MaxAge: maxAge,
		HttpOnly: true, Secure: s.CookieSecure, SameSite: http.SameSiteStrictMode,
	})
}

type sessionResponse struct {
	User      users.User `json:"user"`
	CSRFToken string     `json:"csrf_token"`
	ExpiresAt time.Time  `json:"expires_at"`
}

func (s *server) login(w http.ResponseWriter, r *http.Request, _ *call) {
	var in struct {
		Email    string `json:"email"`
		Password string `json:"password"`
	}
	if !decodeBody(w, r, &in) {
		return
	}
	ip, account := clientIP(r), strings.ToLower(strings.TrimSpace(in.Email))
	if s.logins.blocked(ip, account) {
		w.Header().Set("Retry-After", "300")
		writeErrorCode(w, http.StatusTooManyRequests, "too_many_attempts", "too many failed logins, try again later")
		return
	}
	sess, err := s.Auth.Login(r.Context(), in.Email, in.Password)
	if errors.Is(err, auth.ErrInvalidCredentials) {
		s.logins.fail(ip, account)
		writeErrorCode(w, http.StatusUnauthorized, "invalid_credentials", "invalid email or password")
		return
	}
	if err != nil {
		s.fail(w, r, err)
		return
	}
	s.setSessionCookie(w, sess.Token, sess.ExpiresAt)
	writeJSON(w, http.StatusOK, sessionResponse{User: sess.User, CSRFToken: sess.CSRFToken, ExpiresAt: sess.ExpiresAt})
}

func (s *server) logout(w http.ResponseWriter, r *http.Request, c *call) {
	if err := s.Auth.Logout(r.Context(), c.p); err != nil {
		s.fail(w, r, err)
		return
	}
	if c.p.UsesCookie() {
		s.setSessionCookie(w, "", time.Time{})
	}
	w.WriteHeader(http.StatusNoContent)
}

type meResponse struct {
	User      users.User `json:"user"`
	AuthKind  string     `json:"auth_kind"`
	CSRFToken string     `json:"csrf_token,omitempty"`
	ExpiresAt *time.Time `json:"expires_at,omitempty"`
}

func (s *server) me(w http.ResponseWriter, r *http.Request, c *call) {
	u, err := s.Users.Get(r.Context(), c.p.UserID)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	resp := meResponse{User: u, AuthKind: string(c.p.Kind)}
	if c.p.UsesCookie() {
		resp.CSRFToken = s.Auth.CSRFToken(c.p)
		resp.ExpiresAt = &c.p.ExpiresAt
	}
	writeJSON(w, http.StatusOK, resp)
}

// changePassword is browser session only: a leaked API token must not be able
// to take over the account.
func (s *server) changePassword(w http.ResponseWriter, r *http.Request, c *call) {
	if !c.p.UsesCookie() {
		s.fail(w, r, authz.ErrForbidden)
		return
	}
	var in struct {
		CurrentPassword string `json:"current_password"`
		NewPassword     string `json:"new_password"`
	}
	if !decodeBody(w, r, &in) {
		return
	}
	if err := s.Users.ChangePassword(r.Context(), c.p.UserID, in.CurrentPassword, in.NewPassword, c.p.SessionID); err != nil {
		s.fail(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
