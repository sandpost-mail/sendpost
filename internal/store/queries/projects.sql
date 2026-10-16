-- name: CreateProject :one
INSERT INTO projects (slug, name) VALUES ($1, $2)
RETURNING id, slug, name, created_at;

-- name: GetProject :one
SELECT id, slug, name, created_at FROM projects WHERE id = $1;

-- name: ListProjects :many
SELECT id, slug, name, created_at FROM projects ORDER BY name, id;
