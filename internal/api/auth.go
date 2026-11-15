package api

import (
	"errors"
	"math"
	"net/http"
	"net/netip"
	"strconv"
	"strings"
	"time"

	"github.com/sandpost-mail/sandpost/internal/auth"
	"github.com/sandpost-mail/sandpost/internal/authz"
	"github.com/sandpost-mail/sandpost/internal/ratelimit"
	"github.com/sandpost-mail/sandpost/internal/users"
)

const (
	cookieName       = "sandpost_session"
	hostCookieName   = "__Host-" + cookieName
	loginWindow      = 5 * time.Minute
	maxLoginsPerIP   = 20
	maxLoginsPerUser = 10
	// maxConcurrentLogins bounds argon2 work (64 MiB each) on the public login route.
	maxConcurrentLogins = 4
)

// loginLimits throttles failed logins per client address and per account
// name, both with a sliding window (see ratelimit.Limiter).
//
// The account limit is strict: once an account used up its budget, even the
// right password is refused until the window moves on. The address limit only
// applies to failures: a blocked address still gets its password checked, a
// correct one logs in and a wrong one is answered 429. Behind a reverse proxy
// that is not listed in trusted, every client shares the proxy address, and a
// strict address limit would let anyone lock every user out.
type loginLimits struct {
	ip, account *ratelimit.Limiter
	trusted     []netip.Prefix
}

// newLoginLimits builds the limits. trusted lists the reverse proxies whose
// X-Forwarded-For header is believed, see clientIP.
func newLoginLimits(trusted ...netip.Prefix) *loginLimits {
	return &loginLimits{
		ip:      ratelimit.New(maxLoginsPerIP, loginWindow),
		account: ratelimit.New(maxLoginsPerUser, loginWindow),
		trusted: trusted,
	}
}

// tooManyLogins answers 429 with the time until limiter unblocks key.
func tooManyLogins(w http.ResponseWriter, limiter *ratelimit.Limiter, key string) {
	wait := limiter.RetryAfter(key)
	if wait <= 0 {
		wait = limiter.Window()
	}
	w.Header().Set("Retry-After", strconv.Itoa(int(math.Ceil(wait.Seconds()))))
	writeErrorCode(w, http.StatusTooManyRequests, "too_many_attempts", "too many failed logins, try again later")
}

// clientIP returns the address of the client that sent r. When the peer is a
// trusted proxy, X-Forwarded-For is walked from the right and the first hop
// that is not itself a trusted proxy wins: hops to its left were written by
// the client and can not be believed. Without trusted proxies the header is
// ignored.
func clientIP(r *http.Request, trusted []netip.Prefix) string {
	peer, err := netip.ParseAddrPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	addr := peer.Addr().Unmap()
	if !isTrusted(addr, trusted) {
		return addr.String()
	}
	hops := r.Header.Values("X-Forwarded-For")
	for i := len(hops) - 1; i >= 0; i-- {
		parts := strings.Split(hops[i], ",")
		for j := len(parts) - 1; j >= 0; j-- {
			hop, ok := parseHop(parts[j])
			if !ok {
				// Garbage in the chain: stop at the last address we trust.
				return addr.String()
			}
			addr = hop
			if !isTrusted(hop, trusted) {
				return hop.String()
			}
		}
	}
	return addr.String()
}

func parseHop(s string) (netip.Addr, bool) {
	s = strings.TrimSpace(s)
	if a, err := netip.ParseAddr(s); err == nil {
		return a.Unmap(), true
	}
	if ap, err := netip.ParseAddrPort(s); err == nil {
		return ap.Addr().Unmap(), true
	}
	return netip.Addr{}, false
}

func isTrusted(a netip.Addr, trusted []netip.Prefix) bool {
	for _, p := range trusted {
		if p.Contains(a) {
			return true
		}
	}
	return false
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
	// Secure follows COOKIE_SECURE so plain HTTP local setups can log in;
	// HttpOnly and SameSite=Strict are always set.
	http.SetCookie(w, &http.Cookie{ //nolint:gosec // G124: Secure is configurable (COOKIE_SECURE), see above
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
	ipKey := ratelimit.Key(clientIP(r, s.logins.trusted))
	account := strings.ToLower(strings.TrimSpace(in.Email))
	// Reserving records the failure up front (released on success), so a
	// burst of parallel guesses can not slip past the account limit.
	accRes, ok := s.logins.account.Reserve(account)
	if !ok {
		tooManyLogins(w, s.logins.account, account)
		return
	}
	ipRes, ipAllowed := s.logins.ip.Reserve(ipKey)
	release := func() {
		accRes.Release()
		ipRes.Release()
	}
	select {
	case s.loginSlots <- struct{}{}:
		defer func() { <-s.loginSlots }()
	case <-r.Context().Done():
		release()
		return
	}
	sess, err := s.Auth.Login(r.Context(), in.Email, in.Password)
	if errors.Is(err, auth.ErrInvalidCredentials) {
		if !ipAllowed {
			tooManyLogins(w, s.logins.ip, ipKey)
			return
		}
		writeErrorCode(w, http.StatusUnauthorized, "invalid_credentials", "invalid email or password")
		return
	}
	release()
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
