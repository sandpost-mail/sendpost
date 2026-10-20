// Package groups manages groups, their members and the role each group holds
// on a project.
package groups

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/sandpost-mail/sandpost/internal/audit"
	"github.com/sandpost-mail/sandpost/internal/authz"
	"github.com/sandpost-mail/sandpost/internal/store"
	"github.com/sandpost-mail/sandpost/internal/store/sqlcgen"
	"github.com/sandpost-mail/sandpost/internal/users"
)

const maxNameRunes = 100

// Domain errors.
var (
	ErrNotFound  = errors.New("not found")
	ErrNameTaken = errors.New("group name already in use")
	ErrInvalid   = errors.New("invalid input")
)

// Group is a set of users that can hold roles on projects.
type Group struct {
	ID          int64     `json:"id"`
	Name        string    `json:"name"`
	CreatedAt   time.Time `json:"created_at"`
	MemberCount int       `json:"member_count"`
}

// Assignment is the role a group holds on a project.
type Assignment struct {
	GroupID   int64  `json:"group_id"`
	GroupName string `json:"group_name"`
	Role      string `json:"role"`
}

// Service manages groups. Group and membership methods need the global
// groups.manage capability, the assignment methods take a project Scope that
// already passed the project.groups check.
type Service struct {
	db store.DB
}

// NewService returns a Service on db.
func NewService(db store.DB) *Service { return &Service{db: db} }

// Create adds a group.
func (s *Service) Create(ctx context.Context, name string) (Group, error) {
	name, err := validName(name)
	if err != nil {
		return Group{}, err
	}
	var out Group
	err = store.InTx(ctx, s.db, func(q *sqlcgen.Queries) error {
		g, err := q.CreateGroup(ctx, name)
		if err != nil {
			return err
		}
		out = Group{ID: g.ID, Name: g.Name, CreatedAt: g.CreatedAt}
		return audit.Record(ctx, q, audit.Entry{Action: "group.create", Target: target(g.ID), Detail: map[string]any{"name": name}})
	})
	if store.IsUniqueViolation(err) {
		return Group{}, ErrNameTaken
	}
	if err != nil {
		return Group{}, fmt.Errorf("create group: %w", err)
	}
	return out, nil
}

// Get returns one group.
func (s *Service) Get(ctx context.Context, id int64) (Group, error) {
	g, err := sqlcgen.New(s.db).GetGroup(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return Group{}, ErrNotFound
	}
	if err != nil {
		return Group{}, fmt.Errorf("get group: %w", err)
	}
	return Group{ID: g.ID, Name: g.Name, CreatedAt: g.CreatedAt}, nil
}

// List returns every group with its member count.
func (s *Service) List(ctx context.Context) ([]Group, error) {
	rows, err := sqlcgen.New(s.db).ListGroups(ctx)
	if err != nil {
		return nil, fmt.Errorf("list groups: %w", err)
	}
	out := make([]Group, 0, len(rows))
	for _, r := range rows {
		out = append(out, Group{ID: r.ID, Name: r.Name, CreatedAt: r.CreatedAt, MemberCount: int(r.MemberCount)})
	}
	return out, nil
}

// Rename changes a group name.
func (s *Service) Rename(ctx context.Context, id int64, name string) (Group, error) {
	name, err := validName(name)
	if err != nil {
		return Group{}, err
	}
	var out Group
	err = store.InTx(ctx, s.db, func(q *sqlcgen.Queries) error {
		g, err := q.RenameGroup(ctx, sqlcgen.RenameGroupParams{ID: id, Name: name})
		if err != nil {
			return err
		}
		out = Group{ID: g.ID, Name: g.Name, CreatedAt: g.CreatedAt}
		return audit.Record(ctx, q, audit.Entry{Action: "group.rename", Target: target(id), Detail: map[string]any{"name": name}})
	})
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		return Group{}, ErrNotFound
	case store.IsUniqueViolation(err):
		return Group{}, ErrNameTaken
	case err != nil:
		return Group{}, fmt.Errorf("rename group: %w", err)
	}
	return out, nil
}

// Delete removes a group together with its memberships and role assignments.
func (s *Service) Delete(ctx context.Context, id int64) error {
	return s.mutate(ctx, "delete group", func(q *sqlcgen.Queries) (int64, error) {
		return q.DeleteGroup(ctx, id)
	}, audit.Entry{Action: "group.delete", Target: target(id)})
}

// Members returns the users of a group.
func (s *Service) Members(ctx context.Context, id int64) ([]users.User, error) {
	q := sqlcgen.New(s.db)
	if _, err := q.GetGroup(ctx, id); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("get group: %w", err)
	}
	rows, err := q.ListGroupMembers(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("list members: %w", err)
	}
	out := make([]users.User, 0, len(rows))
	for _, r := range rows {
		out = append(out, users.User(r))
	}
	return out, nil
}

// AddMember puts a user in a group. It is idempotent.
func (s *Service) AddMember(ctx context.Context, groupID, userID int64) error {
	err := store.InTx(ctx, s.db, func(q *sqlcgen.Queries) error {
		if err := q.AddGroupMember(ctx, sqlcgen.AddGroupMemberParams{GroupID: groupID, UserID: userID}); err != nil {
			return err
		}
		return audit.Record(ctx, q, audit.Entry{
			Action: "group.member_add", Target: target(groupID), Detail: map[string]any{"user_id": userID},
		})
	})
	if store.IsForeignKeyViolation(err) {
		return ErrNotFound
	}
	if err != nil {
		return fmt.Errorf("add member: %w", err)
	}
	return nil
}

// RemoveMember takes a user out of a group.
func (s *Service) RemoveMember(ctx context.Context, groupID, userID int64) error {
	return s.mutate(ctx, "remove member", func(q *sqlcgen.Queries) (int64, error) {
		return q.RemoveGroupMember(ctx, sqlcgen.RemoveGroupMemberParams{GroupID: groupID, UserID: userID})
	}, audit.Entry{Action: "group.member_remove", Target: target(groupID), Detail: map[string]any{"user_id": userID}})
}

// ProjectAssignments lists the groups assigned to the project.
func (s *Service) ProjectAssignments(ctx context.Context, scope authz.Scope) ([]Assignment, error) {
	rows, err := sqlcgen.New(s.db).ListProjectGroups(ctx, scope.ProjectID())
	if err != nil {
		return nil, fmt.Errorf("list project groups: %w", err)
	}
	out := make([]Assignment, 0, len(rows))
	for _, r := range rows {
		out = append(out, Assignment{GroupID: r.GroupID, GroupName: r.GroupName, Role: r.Role})
	}
	return out, nil
}

// AssignRole gives a group a role on the project, replacing any previous one.
func (s *Service) AssignRole(ctx context.Context, scope authz.Scope, groupID int64, role string) error {
	if _, ok := authz.ParseRole(role); !ok {
		return fmt.Errorf("%w: role must be viewer, member or owner", ErrInvalid)
	}
	err := store.InTx(ctx, s.db, func(q *sqlcgen.Queries) error {
		err := q.SetProjectGroup(ctx, sqlcgen.SetProjectGroupParams{ProjectID: scope.ProjectID(), GroupID: groupID, Role: role})
		if err != nil {
			return err
		}
		return audit.Record(ctx, q, audit.Entry{
			Action: "project.group_assign", ProjectID: scope.ProjectID(), Target: target(groupID),
			Detail: map[string]any{"role": role},
		})
	})
	if store.IsForeignKeyViolation(err) {
		return ErrNotFound
	}
	if err != nil {
		return fmt.Errorf("assign role: %w", err)
	}
	return nil
}

// RemoveRole withdraws a group's role on the project.
func (s *Service) RemoveRole(ctx context.Context, scope authz.Scope, groupID int64) error {
	return s.mutate(ctx, "remove role", func(q *sqlcgen.Queries) (int64, error) {
		return q.RemoveProjectGroup(ctx, sqlcgen.RemoveProjectGroupParams{ProjectID: scope.ProjectID(), GroupID: groupID})
	}, audit.Entry{Action: "project.group_remove", ProjectID: scope.ProjectID(), Target: target(groupID)})
}

// mutate runs a delete style statement and audits it when a row was affected.
func (s *Service) mutate(ctx context.Context, what string, fn func(*sqlcgen.Queries) (int64, error), e audit.Entry) error {
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
		return fmt.Errorf("%s: %w", what, err)
	}
	return nil
}

func validName(name string) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" || len([]rune(name)) > maxNameRunes {
		return "", fmt.Errorf("%w: name must be 1 to %d characters", ErrInvalid, maxNameRunes)
	}
	return name, nil
}

func target(groupID int64) string { return fmt.Sprintf("group:%d", groupID) }
