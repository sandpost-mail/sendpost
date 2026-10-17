package testutil

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/sandpost-mail/sandpost/internal/passwords"
	"github.com/sandpost-mail/sandpost/internal/store/sqlcgen"
)

// User inserts a user with a placeholder password hash (nobody can log in
// with it, which keeps tests fast) and returns its id.
func User(t *testing.T, pool *pgxpool.Pool, email string, admin bool) int64 {
	t.Helper()
	u, err := sqlcgen.New(pool).CreateUser(context.Background(), sqlcgen.CreateUserParams{
		Email: email, Name: email, PasswordHash: "!", IsAdmin: admin,
	})
	if err != nil {
		t.Fatalf("create user %q: %v", email, err)
	}
	return u.ID
}

// UserWithPassword inserts a user that can really log in with password.
func UserWithPassword(t *testing.T, pool *pgxpool.Pool, email, password string, admin, mustChange bool) int64 {
	t.Helper()
	hash, err := passwords.Hash(password)
	if err != nil {
		t.Fatal(err)
	}
	u, err := sqlcgen.New(pool).CreateUser(context.Background(), sqlcgen.CreateUserParams{
		Email: email, Name: email, PasswordHash: hash, IsAdmin: admin, MustChangePassword: mustChange,
	})
	if err != nil {
		t.Fatalf("create user %q: %v", email, err)
	}
	return u.ID
}

// Group inserts a group and returns its id.
func Group(t *testing.T, pool *pgxpool.Pool, name string) int64 {
	t.Helper()
	g, err := sqlcgen.New(pool).CreateGroup(context.Background(), name)
	if err != nil {
		t.Fatalf("create group %q: %v", name, err)
	}
	return g.ID
}

// Member adds a user to a group.
func Member(t *testing.T, pool *pgxpool.Pool, groupID, userID int64) {
	t.Helper()
	err := sqlcgen.New(pool).AddGroupMember(context.Background(),
		sqlcgen.AddGroupMemberParams{GroupID: groupID, UserID: userID})
	if err != nil {
		t.Fatalf("add member: %v", err)
	}
}

// Assign gives a group a role (viewer, member or owner) on a project.
func Assign(t *testing.T, pool *pgxpool.Pool, projectID, groupID int64, role string) {
	t.Helper()
	err := sqlcgen.New(pool).SetProjectGroup(context.Background(),
		sqlcgen.SetProjectGroupParams{ProjectID: projectID, GroupID: groupID, Role: role})
	if err != nil {
		t.Fatalf("assign group: %v", err)
	}
}
