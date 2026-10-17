package users

import (
	"context"
	"errors"
	"fmt"
	"net/mail"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/sandpost-mail/sandpost/internal/audit"
	"github.com/sandpost-mail/sandpost/internal/passwords"
	"github.com/sandpost-mail/sandpost/internal/store"
	"github.com/sandpost-mail/sandpost/internal/store/sqlcgen"
)

// Input limits.
const (
	MinPasswordLength = 12
	maxPasswordLength = 128 // bounds the argon2 work an attacker can request
	maxEmailLength    = 254
	maxNameRunes      = 200
)

// Domain errors.
var (
	ErrNotFound      = errors.New("user not found")
	ErrEmailTaken    = errors.New("email already in use")
	ErrInvalid       = errors.New("invalid input")
	ErrLastAdmin     = errors.New("at least one active administrator is required")
	ErrWrongPassword = errors.New("current password is incorrect")
)

// User is an account, never including its password hash.
type User struct {
	ID                 int64      `json:"id"`
	Email              string     `json:"email"`
	Name               string     `json:"name"`
	IsAdmin            bool       `json:"is_admin"`
	MustChangePassword bool       `json:"must_change_password"`
	CreatedAt          time.Time  `json:"created_at"`
	DisabledAt         *time.Time `json:"disabled_at"`
}

// CreateInput is a new account. The user must change the password at first
// login.
type CreateInput struct {
	Email    string `json:"email"`
	Name     string `json:"name"`
	Password string `json:"password"`
	IsAdmin  bool   `json:"is_admin"`
}

// UpdateInput changes the given fields only (nil leaves a field alone).
type UpdateInput struct {
	Name     *string `json:"name"`
	IsAdmin  *bool   `json:"is_admin"`
	Disabled *bool   `json:"disabled"`
}

// Service manages user accounts. Callers must have checked the
// users.manage capability, except for ChangePassword which acts on the
// caller's own account.
type Service struct {
	db store.DB
}

// NewService returns a Service on db.
func NewService(db store.DB) *Service { return &Service{db: db} }

// Create adds an account.
func (s *Service) Create(ctx context.Context, in CreateInput) (User, error) {
	email, err := normalizeEmail(in.Email)
	if err != nil {
		return User{}, err
	}
	name := strings.TrimSpace(in.Name)
	if len([]rune(name)) > maxNameRunes {
		return User{}, fmt.Errorf("%w: name is limited to %d characters", ErrInvalid, maxNameRunes)
	}
	if name == "" {
		name = localPart(email)
	}
	if err := ValidatePassword(in.Password); err != nil {
		return User{}, err
	}
	hash, err := passwords.Hash(in.Password)
	if err != nil {
		return User{}, fmt.Errorf("hash password: %w", err)
	}
	var out User
	err = store.InTx(ctx, s.db, func(q *sqlcgen.Queries) error {
		row, err := q.CreateUser(ctx, sqlcgen.CreateUserParams{
			Email: email, Name: name, PasswordHash: hash, IsAdmin: in.IsAdmin, MustChangePassword: true,
		})
		if err != nil {
			return err
		}
		out = User(row)
		return audit.Record(ctx, q, audit.Entry{
			Action: "user.create", Target: userTarget(row.ID),
			Detail: map[string]any{"email": email, "is_admin": in.IsAdmin},
		})
	})
	if store.IsUniqueViolation(err) {
		return User{}, ErrEmailTaken
	}
	if err != nil {
		return User{}, fmt.Errorf("create user: %w", err)
	}
	return out, nil
}

// Get returns one user.
func (s *Service) Get(ctx context.Context, id int64) (User, error) {
	row, err := sqlcgen.New(s.db).GetUser(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return User{}, ErrNotFound
	}
	if err != nil {
		return User{}, fmt.Errorf("get user: %w", err)
	}
	return User(row), nil
}

// List returns every user ordered by email.
func (s *Service) List(ctx context.Context) ([]User, error) {
	rows, err := sqlcgen.New(s.db).ListUsers(ctx)
	if err != nil {
		return nil, fmt.Errorf("list users: %w", err)
	}
	out := make([]User, 0, len(rows))
	for _, r := range rows {
		out = append(out, User(r))
	}
	return out, nil
}

// Update changes a user. Demoting or disabling the last active administrator
// fails with ErrLastAdmin. Disabling a user ends its sessions, and its tokens
// stop working at once.
func (s *Service) Update(ctx context.Context, id int64, in UpdateInput) (User, error) {
	p := sqlcgen.UpdateUserParams{ID: id}
	detail := map[string]any{}
	if in.Name != nil {
		name := strings.TrimSpace(*in.Name)
		if len([]rune(name)) > maxNameRunes {
			return User{}, fmt.Errorf("%w: name is limited to %d characters", ErrInvalid, maxNameRunes)
		}
		p.Name = pgtype.Text{String: name, Valid: true}
		detail["name"] = name
	}
	if in.IsAdmin != nil {
		p.IsAdmin = pgtype.Bool{Bool: *in.IsAdmin, Valid: true}
		detail["is_admin"] = *in.IsAdmin
	}
	if in.Disabled != nil {
		p.Disabled = pgtype.Bool{Bool: *in.Disabled, Valid: true}
		detail["disabled"] = *in.Disabled
	}
	var out User
	err := store.InTx(ctx, s.db, func(q *sqlcgen.Queries) error {
		if err := guardLastAdmin(ctx, q, id, in); err != nil {
			return err
		}
		row, err := q.UpdateUser(ctx, p)
		if err != nil {
			return err
		}
		out = User(row)
		if in.Disabled != nil && *in.Disabled {
			if err := q.DeleteUserSessions(ctx, id); err != nil {
				return err
			}
		}
		return audit.Record(ctx, q, audit.Entry{Action: "user.update", Target: userTarget(id), Detail: detail})
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return User{}, ErrNotFound
	}
	if err != nil {
		if errors.Is(err, ErrLastAdmin) {
			return User{}, err
		}
		return User{}, fmt.Errorf("update user: %w", err)
	}
	return out, nil
}

// guardLastAdmin refuses a change that would leave no active administrator.
func guardLastAdmin(ctx context.Context, q *sqlcgen.Queries, id int64, in UpdateInput) error {
	demote := in.IsAdmin != nil && !*in.IsAdmin
	disable := in.Disabled != nil && *in.Disabled
	if !demote && !disable {
		return nil
	}
	admins, err := q.LockActiveAdmins(ctx)
	if err != nil {
		return err
	}
	if len(admins) == 1 && admins[0] == id {
		return ErrLastAdmin
	}
	return nil
}

// ResetPassword sets a new password chosen by an administrator. The user must
// change it at next login and all of its sessions end.
func (s *Service) ResetPassword(ctx context.Context, id int64, newPassword string) error {
	if err := ValidatePassword(newPassword); err != nil {
		return err
	}
	hash, err := passwords.Hash(newPassword)
	if err != nil {
		return fmt.Errorf("hash password: %w", err)
	}
	err = store.InTx(ctx, s.db, func(q *sqlcgen.Queries) error {
		n, err := q.SetUserPassword(ctx, sqlcgen.SetUserPasswordParams{ID: id, PasswordHash: hash, MustChangePassword: true})
		if err != nil {
			return err
		}
		if n == 0 {
			return ErrNotFound
		}
		if err := q.DeleteUserSessions(ctx, id); err != nil {
			return err
		}
		return audit.Record(ctx, q, audit.Entry{Action: "user.password_reset", Target: userTarget(id)})
	})
	if errors.Is(err, ErrNotFound) {
		return err
	}
	if err != nil {
		return fmt.Errorf("reset password: %w", err)
	}
	return nil
}

// ChangePassword lets a user replace its own password. Every other session of
// the user ends, keepSession (the caller's) stays. It clears
// must_change_password.
func (s *Service) ChangePassword(ctx context.Context, id int64, current, next string, keepSession []byte) error {
	if err := ValidatePassword(next); err != nil {
		return err
	}
	if current == next {
		return fmt.Errorf("%w: the new password must differ from the current one", ErrInvalid)
	}
	hash, err := sqlcgen.New(s.db).GetUserPasswordHash(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return fmt.Errorf("load password hash: %w", err)
	}
	if err := passwords.Verify(current, hash); err != nil {
		if errors.Is(err, passwords.ErrMismatch) {
			return ErrWrongPassword
		}
		return fmt.Errorf("verify password: %w", err)
	}
	newHash, err := passwords.Hash(next)
	if err != nil {
		return fmt.Errorf("hash password: %w", err)
	}
	err = store.InTx(ctx, s.db, func(q *sqlcgen.Queries) error {
		if _, err := q.SetUserPassword(ctx, sqlcgen.SetUserPasswordParams{ID: id, PasswordHash: newHash}); err != nil {
			return err
		}
		return q.DeleteUserSessionsExcept(ctx, sqlcgen.DeleteUserSessionsExceptParams{UserID: id, ID: keepSession})
	})
	if err != nil {
		return fmt.Errorf("change password: %w", err)
	}
	return nil
}

// ValidatePassword enforces the password length bounds.
func ValidatePassword(p string) error {
	if len(p) < MinPasswordLength {
		return fmt.Errorf("%w: password must be at least %d characters", ErrInvalid, MinPasswordLength)
	}
	if len(p) > maxPasswordLength {
		return fmt.Errorf("%w: password is limited to %d bytes", ErrInvalid, maxPasswordLength)
	}
	return nil
}

func normalizeEmail(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	addr, err := mail.ParseAddress(raw)
	if err != nil || addr.Address != raw || len(raw) > maxEmailLength {
		return "", fmt.Errorf("%w: email must be a plain address such as user@example.com", ErrInvalid)
	}
	return raw, nil
}

func userTarget(id int64) string { return fmt.Sprintf("user:%d", id) }
