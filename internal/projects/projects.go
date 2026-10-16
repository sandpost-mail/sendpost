// Package projects manages projects and their ingest tag rules.
package projects

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"
	"unicode"

	"github.com/jackc/pgx/v5"

	"github.com/sandpost-mail/sandpost/internal/ingest"
	"github.com/sandpost-mail/sandpost/internal/store"
	"github.com/sandpost-mail/sandpost/internal/store/sqlcgen"
)

const (
	maxNameRunes = 200
	maxSlugLen   = 63
	maxTagRunes  = 100
	maxFieldLen  = 200
	fallbackSlug = "project"
)

// Domain errors.
var (
	ErrNotFound  = errors.New("not found")
	ErrSlugTaken = errors.New("slug already in use")
	ErrInvalid   = errors.New("invalid input")
)

var slugPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,62}$`)

// Project is a capture project.
type Project struct {
	ID        int64     `json:"id"`
	Slug      string    `json:"slug"`
	Name      string    `json:"name"`
	CreatedAt time.Time `json:"created_at"`
}

// TagRule is a rule applied at ingest, see ingest.ApplyRules.
type TagRule struct {
	ID         int64     `json:"id"`
	ProjectID  int64     `json:"project_id"`
	Kind       string    `json:"kind"`
	HeaderName string    `json:"header_name"`
	Pattern    string    `json:"pattern"`
	Tag        string    `json:"tag"`
	CreatedAt  time.Time `json:"created_at"`
}

// TagRuleInput is the writable part of a TagRule.
type TagRuleInput struct {
	Kind       string `json:"kind"`
	HeaderName string `json:"header_name"`
	Pattern    string `json:"pattern"`
	Tag        string `json:"tag"`
}

// Service reads and writes projects.
type Service struct {
	q *sqlcgen.Queries
}

// New returns a Service on db.
func New(db sqlcgen.DBTX) *Service { return &Service{q: sqlcgen.New(db)} }

// Create adds a project. An empty slug is derived from the name.
func (s *Service) Create(ctx context.Context, name, slug string) (Project, error) {
	name = strings.TrimSpace(name)
	if name == "" || len([]rune(name)) > maxNameRunes {
		return Project{}, fmt.Errorf("%w: name must be 1 to %d characters", ErrInvalid, maxNameRunes)
	}
	slug = strings.TrimSpace(slug)
	if slug == "" {
		slug = Slugify(name)
	}
	if !slugPattern.MatchString(slug) {
		return Project{}, fmt.Errorf("%w: slug must match %s", ErrInvalid, slugPattern)
	}
	row, err := s.q.CreateProject(ctx, sqlcgen.CreateProjectParams{Slug: slug, Name: name})
	if store.IsUniqueViolation(err) {
		return Project{}, ErrSlugTaken
	}
	if err != nil {
		return Project{}, fmt.Errorf("create project: %w", err)
	}
	return Project(row), nil
}

// Get returns one project.
func (s *Service) Get(ctx context.Context, id int64) (Project, error) {
	row, err := s.q.GetProject(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return Project{}, ErrNotFound
	}
	if err != nil {
		return Project{}, fmt.Errorf("get project: %w", err)
	}
	return Project(row), nil
}

// List returns all projects ordered by name.
func (s *Service) List(ctx context.Context) ([]Project, error) {
	rows, err := s.q.ListProjects(ctx)
	if err != nil {
		return nil, fmt.Errorf("list projects: %w", err)
	}
	out := make([]Project, 0, len(rows))
	for _, r := range rows {
		out = append(out, Project(r))
	}
	return out, nil
}

// Slugify derives a URL safe slug from a display name.
func Slugify(name string) string {
	var b strings.Builder
	dash := false
	for _, r := range strings.ToLower(name) {
		switch {
		case r < unicode.MaxASCII && (unicode.IsLetter(r) || unicode.IsDigit(r)):
			if dash && b.Len() > 0 {
				b.WriteByte('-')
			}
			dash = false
			b.WriteRune(r)
		default:
			dash = true
		}
	}
	slug := b.String()
	if len(slug) > maxSlugLen {
		slug = strings.TrimRight(slug[:maxSlugLen], "-")
	}
	if slug == "" {
		return fallbackSlug
	}
	return slug
}

// ListTagRules returns the project's rules in creation order.
func (s *Service) ListTagRules(ctx context.Context, projectID int64) ([]TagRule, error) {
	rows, err := s.q.ListTagRules(ctx, projectID)
	if err != nil {
		return nil, fmt.Errorf("list tag rules: %w", err)
	}
	out := make([]TagRule, 0, len(rows))
	for _, r := range rows {
		out = append(out, TagRule(r))
	}
	return out, nil
}

// CreateTagRule validates and stores a rule.
func (s *Service) CreateTagRule(ctx context.Context, projectID int64, in TagRuleInput) (TagRule, error) {
	in.HeaderName, in.Pattern, in.Tag = strings.TrimSpace(in.HeaderName), strings.TrimSpace(in.Pattern), strings.TrimSpace(in.Tag)
	if err := validateRule(in); err != nil {
		return TagRule{}, err
	}
	row, err := s.q.CreateTagRule(ctx, sqlcgen.CreateTagRuleParams{
		ProjectID: projectID, Kind: in.Kind, HeaderName: in.HeaderName, Pattern: in.Pattern, Tag: in.Tag,
	})
	if store.IsForeignKeyViolation(err) {
		return TagRule{}, ErrNotFound
	}
	if err != nil {
		return TagRule{}, fmt.Errorf("create tag rule: %w", err)
	}
	return TagRule(row), nil
}

func validateRule(in TagRuleInput) error {
	switch in.Kind {
	case ingest.RuleHeader:
		if in.HeaderName == "" {
			return fmt.Errorf("%w: header rules need a header_name", ErrInvalid)
		}
	case ingest.RuleRecipient:
		if in.HeaderName != "" {
			return fmt.Errorf("%w: recipient rules do not take a header_name", ErrInvalid)
		}
	default:
		return fmt.Errorf("%w: kind must be %q or %q", ErrInvalid, ingest.RuleHeader, ingest.RuleRecipient)
	}
	if in.Tag == "" || len([]rune(in.Tag)) > maxTagRunes {
		return fmt.Errorf("%w: tag must be 1 to %d characters", ErrInvalid, maxTagRunes)
	}
	if len(in.HeaderName) > maxFieldLen || len(in.Pattern) > maxFieldLen {
		return fmt.Errorf("%w: header_name and pattern are limited to %d characters", ErrInvalid, maxFieldLen)
	}
	return nil
}

// DeleteTagRule removes a rule of the project.
func (s *Service) DeleteTagRule(ctx context.Context, projectID, ruleID int64) error {
	n, err := s.q.DeleteTagRule(ctx, sqlcgen.DeleteTagRuleParams{ProjectID: projectID, ID: ruleID})
	if err != nil {
		return fmt.Errorf("delete tag rule: %w", err)
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}
