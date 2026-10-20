// Package tokens issues and verifies API tokens, owned by a user or pinned to
// one project.
//
// Like SMTP credentials, tokens are server generated with 256 bits of
// entropy, so a keyed HMAC-SHA256 is enough to store them. The visible prefix
// (spu_ for user tokens, spp_ for project tokens) lets secret scanners
// recognize a leaked token. The secret is returned once, at creation.
package tokens

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/sandpost-mail/sandpost/internal/audit"
	"github.com/sandpost-mail/sandpost/internal/authz"
	"github.com/sandpost-mail/sandpost/internal/store"
	"github.com/sandpost-mail/sandpost/internal/store/sqlcgen"
)

const (
	userPrefix     = "spu_"
	projectPrefix  = "spp_"
	displayLen     = 12 // prefix plus 8 random characters, safe to show
	maxLabelLength = 100
	keyContext     = "sandpost/api-token/v1"
)

// Domain errors.
var (
	ErrInvalid  = errors.New("invalid token")
	ErrNotFound = errors.New("token not found")
	// ErrBadInput wraps validation problems of a creation request.
	ErrBadInput = errors.New("invalid input")
)

// Token is a stored token, never including its secret.
type Token struct {
	ID          int64      `json:"id"`
	OwnerUserID *int64     `json:"owner_user_id"`
	ProjectID   *int64     `json:"project_id"`
	Prefix      string     `json:"prefix"`
	Label       string     `json:"label"`
	Scopes      []string   `json:"scopes"`
	CreatedAt   time.Time  `json:"created_at"`
	RevokedAt   *time.Time `json:"revoked_at"`
	LastUsedAt  *time.Time `json:"last_used_at"`
}

// Issued is a freshly created token. Secret is shown once.
type Issued struct {
	Token
	Secret string `json:"secret"`
}

// Service manages API tokens.
type Service struct {
	db  store.DB
	key []byte
}

// NewService returns a Service. A dedicated HMAC key is derived from the
// instance SECRET_KEY.
func NewService(db store.DB, secretKey string) *Service {
	mac := hmac.New(sha256.New, []byte(secretKey))
	mac.Write([]byte(keyContext))
	return &Service{db: db, key: mac.Sum(nil)}
}

// CreateForUser issues a token that acts as userID, limited by scopes (read,
// write, manage). A token never exceeds what its owner may do.
func (s *Service) CreateForUser(ctx context.Context, userID int64, label string, scopes []string) (Issued, error) {
	label, scopes, err := validate(label, scopes, false)
	if err != nil {
		return Issued{}, err
	}
	return s.create(ctx, sqlcgen.CreateTokenParams{
		OwnerUserID: pgtype.Int8{Int64: userID, Valid: true}, Label: label, Scopes: scopes,
	}, userPrefix, audit.Entry{Action: "token.create", Target: fmt.Sprintf("user:%d", userID)})
}

// CreateForProject issues a token pinned to the scope's project. Project
// tokens can only read or write messages, never manage the project.
func (s *Service) CreateForProject(ctx context.Context, scope authz.Scope, label string, scopes []string) (Issued, error) {
	label, scopes, err := validate(label, scopes, true)
	if err != nil {
		return Issued{}, err
	}
	return s.create(ctx, sqlcgen.CreateTokenParams{
		ProjectID: pgtype.Int8{Int64: scope.ProjectID(), Valid: true}, Label: label, Scopes: scopes,
	}, projectPrefix, audit.Entry{Action: "token.create", ProjectID: scope.ProjectID(), Target: fmt.Sprintf("project:%d", scope.ProjectID())})
}

func validate(label string, scopes []string, projectToken bool) (string, []string, error) {
	label = strings.TrimSpace(label)
	if len([]rune(label)) > maxLabelLength {
		return "", nil, fmt.Errorf("%w: label is limited to %d characters", ErrBadInput, maxLabelLength)
	}
	if len(scopes) == 0 {
		return "", nil, fmt.Errorf("%w: at least one scope is required", ErrBadInput)
	}
	clean := make([]string, 0, len(scopes))
	for _, sc := range scopes {
		if _, ok := authz.ScopeRole(sc); !ok || (projectToken && sc == "manage") {
			allowed := "read, write or manage"
			if projectToken {
				allowed = "read or write"
			}
			return "", nil, fmt.Errorf("%w: scope must be %s", ErrBadInput, allowed)
		}
		if !slices.Contains(clean, sc) {
			clean = append(clean, sc)
		}
	}
	slices.Sort(clean)
	return label, clean, nil
}

func (s *Service) create(ctx context.Context, p sqlcgen.CreateTokenParams, prefix string, e audit.Entry) (Issued, error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return Issued{}, fmt.Errorf("random bytes: %w", err)
	}
	secret := prefix + base64.RawURLEncoding.EncodeToString(raw)
	p.TokenHmac = s.digest(secret)
	p.Prefix = secret[:displayLen]
	if id, ok := audit.Actor(ctx); ok {
		p.CreatedBy = pgtype.Int8{Int64: id, Valid: true}
	}
	var out Issued
	err := store.InTx(ctx, s.db, func(q *sqlcgen.Queries) error {
		row, err := q.CreateToken(ctx, p)
		if err != nil {
			return err
		}
		out = Issued{Token: Token{
			ID: row.ID, OwnerUserID: opt(row.OwnerUserID), ProjectID: opt(row.ProjectID), Prefix: row.Prefix,
			Label: row.Label, Scopes: row.Scopes, CreatedAt: row.CreatedAt,
		}, Secret: secret}
		e.Detail = map[string]any{"token_id": row.ID, "label": row.Label, "scopes": row.Scopes}
		return audit.Record(ctx, q, e)
	})
	if store.IsForeignKeyViolation(err) {
		return Issued{}, ErrNotFound
	}
	if err != nil {
		return Issued{}, fmt.Errorf("create token: %w", err)
	}
	return out, nil
}

// ListForUser returns a user's tokens, newest first.
func (s *Service) ListForUser(ctx context.Context, userID int64) ([]Token, error) {
	rows, err := sqlcgen.New(s.db).ListUserTokens(ctx, pgtype.Int8{Int64: userID, Valid: true})
	if err != nil {
		return nil, fmt.Errorf("list user tokens: %w", err)
	}
	out := make([]Token, 0, len(rows))
	for _, r := range rows {
		out = append(out, fromRow(r.ID, r.OwnerUserID, r.ProjectID, r.Prefix, r.Label, r.Scopes, r.CreatedAt, r.RevokedAt, r.LastUsedAt))
	}
	return out, nil
}

// ListForProject returns the project's tokens, newest first.
func (s *Service) ListForProject(ctx context.Context, scope authz.Scope) ([]Token, error) {
	rows, err := sqlcgen.New(s.db).ListProjectTokens(ctx, pgtype.Int8{Int64: scope.ProjectID(), Valid: true})
	if err != nil {
		return nil, fmt.Errorf("list project tokens: %w", err)
	}
	out := make([]Token, 0, len(rows))
	for _, r := range rows {
		out = append(out, fromRow(r.ID, r.OwnerUserID, r.ProjectID, r.Prefix, r.Label, r.Scopes, r.CreatedAt, r.RevokedAt, r.LastUsedAt))
	}
	return out, nil
}

// RevokeForUser revokes one of a user's tokens. ErrNotFound when the token
// does not belong to the user or is already revoked.
func (s *Service) RevokeForUser(ctx context.Context, userID, tokenID int64) error {
	return s.revoke(ctx, func(q *sqlcgen.Queries) (int64, error) {
		return q.RevokeUserToken(ctx, sqlcgen.RevokeUserTokenParams{OwnerUserID: pgtype.Int8{Int64: userID, Valid: true}, ID: tokenID})
	}, audit.Entry{Action: "token.revoke", Target: fmt.Sprintf("user:%d", userID), Detail: map[string]any{"token_id": tokenID}})
}

// RevokeForProject revokes one of the project's tokens.
func (s *Service) RevokeForProject(ctx context.Context, scope authz.Scope, tokenID int64) error {
	return s.revoke(ctx, func(q *sqlcgen.Queries) (int64, error) {
		return q.RevokeProjectToken(ctx, sqlcgen.RevokeProjectTokenParams{ProjectID: pgtype.Int8{Int64: scope.ProjectID(), Valid: true}, ID: tokenID})
	}, audit.Entry{
		Action: "token.revoke", ProjectID: scope.ProjectID(), Target: fmt.Sprintf("project:%d", scope.ProjectID()),
		Detail: map[string]any{"token_id": tokenID},
	})
}

func (s *Service) revoke(ctx context.Context, fn func(*sqlcgen.Queries) (int64, error), e audit.Entry) error {
	err := store.InTx(ctx, s.db, func(q *sqlcgen.Queries) error {
		n, err := fn(q)
		if err != nil {
			return err
		}
		if n == 0 {
			return ErrNotFound
		}
		return audit.Record(ctx, q, e)
	})
	if errors.Is(err, ErrNotFound) {
		return err
	}
	if err != nil {
		return fmt.Errorf("revoke token: %w", err)
	}
	return nil
}

// Authenticate resolves a token secret to a Principal. Every failure is
// ErrInvalid. A project token is capped at the member role even if it was
// stored with broader scopes.
func (s *Service) Authenticate(ctx context.Context, secret string) (authz.Principal, error) {
	if !strings.HasPrefix(secret, userPrefix) && !strings.HasPrefix(secret, projectPrefix) {
		return authz.Principal{}, ErrInvalid
	}
	q := sqlcgen.New(s.db)
	row, err := q.GetActiveTokenByHMAC(ctx, s.digest(secret))
	if errors.Is(err, pgx.ErrNoRows) {
		return authz.Principal{}, ErrInvalid
	}
	if err != nil {
		return authz.Principal{}, fmt.Errorf("lookup token: %w", err)
	}
	ceiling, ok := authz.CeilingOf(row.Scopes)
	if !ok {
		return authz.Principal{}, ErrInvalid
	}
	_ = q.TouchToken(ctx, row.ID) // best effort bookkeeping
	if row.ProjectID.Valid {
		return authz.Principal{
			Kind: authz.KindProjectToken, ProjectID: row.ProjectID.Int64, TokenID: row.ID,
			Ceiling: min(ceiling, authz.RoleMember),
		}, nil
	}
	return authz.Principal{
		Kind: authz.KindUserToken, UserID: row.OwnerUserID.Int64, IsAdmin: row.UserIsAdmin,
		MustChangePassword: row.UserMustChangePassword, TokenID: row.ID, Ceiling: ceiling,
	}, nil
}

func (s *Service) digest(secret string) []byte {
	mac := hmac.New(sha256.New, s.key)
	mac.Write([]byte(secret))
	return mac.Sum(nil)
}

func fromRow(id int64, owner, project pgtype.Int8, prefix, label string, scopes []string, created time.Time, revoked, used *time.Time) Token {
	return Token{
		ID: id, OwnerUserID: opt(owner), ProjectID: opt(project), Prefix: prefix, Label: label,
		Scopes: scopes, CreatedAt: created, RevokedAt: revoked, LastUsedAt: used,
	}
}

func opt(v pgtype.Int8) *int64 {
	if !v.Valid {
		return nil
	}
	return &v.Int64
}
