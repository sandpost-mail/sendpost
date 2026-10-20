-- name: CreateProject :one
INSERT INTO projects (slug, name) VALUES ($1, $2)
RETURNING id, slug, name, created_at;

-- name: GetProject :one
SELECT id, slug, name, created_at FROM projects WHERE id = $1;

-- name: ListProjects :many
SELECT id, slug, name, created_at FROM projects ORDER BY name, id;

-- The projects a user holds a role on with the highest role rank (3 owner,
-- 2 member, 1 viewer) over all their groups, one row per project.
-- name: ListVisibleProjectsWithRank :many
SELECT p.id, p.slug, p.name, p.created_at,
       max(CASE pg.role WHEN 'owner' THEN 3 WHEN 'member' THEN 2 WHEN 'viewer' THEN 1 ELSE 0 END)::int AS rank
FROM projects p
JOIN project_groups pg ON pg.project_id = p.id
JOIN group_members gm ON gm.group_id = pg.group_id AND gm.user_id = $1
GROUP BY p.id
ORDER BY p.name, p.id;
