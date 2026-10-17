-- name: CreateGroup :one
INSERT INTO groups (name) VALUES ($1) RETURNING id, name, created_at;

-- name: GetGroup :one
SELECT id, name, created_at FROM groups WHERE id = $1;

-- name: ListGroups :many
SELECT g.id, g.name, g.created_at,
       (SELECT count(*) FROM group_members m WHERE m.group_id = g.id)::int AS member_count
FROM groups g ORDER BY lower(g.name), g.id;

-- name: RenameGroup :one
UPDATE groups SET name = $2 WHERE id = $1 RETURNING id, name, created_at;

-- name: DeleteGroup :execrows
DELETE FROM groups WHERE id = $1;

-- name: AddGroupMember :exec
INSERT INTO group_members (group_id, user_id) VALUES ($1, $2) ON CONFLICT DO NOTHING;

-- name: RemoveGroupMember :execrows
DELETE FROM group_members WHERE group_id = $1 AND user_id = $2;

-- name: ListGroupMembers :many
SELECT u.id, u.email, u.name, u.is_admin, u.must_change_password, u.created_at, u.disabled_at
FROM group_members m JOIN users u ON u.id = m.user_id
WHERE m.group_id = $1 ORDER BY lower(u.email), u.id;

-- name: SetProjectGroup :exec
INSERT INTO project_groups (project_id, group_id, role) VALUES ($1, $2, $3)
ON CONFLICT (project_id, group_id) DO UPDATE SET role = EXCLUDED.role;

-- name: RemoveProjectGroup :execrows
DELETE FROM project_groups WHERE project_id = $1 AND group_id = $2;

-- name: ListProjectGroups :many
SELECT g.id AS group_id, g.name AS group_name, pg.role
FROM project_groups pg JOIN groups g ON g.id = pg.group_id
WHERE pg.project_id = $1 ORDER BY lower(g.name), g.id;
