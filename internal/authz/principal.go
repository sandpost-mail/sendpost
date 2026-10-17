package authz

import "time"

// Kind says how a principal authenticated.
type Kind string

// Principal kinds.
const (
	KindSession      Kind = "session"
	KindUserToken    Kind = "user_token"
	KindProjectToken Kind = "project_token"
)

// Principal is the authenticated caller. It is a plain value: build it in the
// authentication layer, never in a handler.
type Principal struct {
	Kind Kind
	// UserID is zero for project tokens.
	UserID  int64
	IsAdmin bool
	// MustChangePassword restricts the principal to the password change flow.
	MustChangePassword bool
	// ProjectID pins a project token to its project, zero otherwise.
	ProjectID int64
	// Ceiling caps the role this principal can reach, from token scopes.
	// Sessions use RoleAdmin, meaning the user's own power applies in full.
	Ceiling Role
	// TokenID is set for token principals.
	TokenID int64
	// SessionID identifies the session (the stored HMAC), for logout and CSRF.
	SessionID []byte
	// ExpiresAt is when a session ends, zero for tokens. Long lived streams
	// close at that moment.
	ExpiresAt time.Time
}

// IsUser reports whether the principal acts for a user, as opposed to a
// project token.
func (p Principal) IsUser() bool { return p.Kind != KindProjectToken && p.UserID != 0 }

// UsesCookie reports whether the principal came from a browser session, which
// is the only kind that needs CSRF protection.
func (p Principal) UsesCookie() bool { return p.Kind == KindSession }
