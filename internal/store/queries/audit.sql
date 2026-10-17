-- name: InsertAudit :exec
INSERT INTO audit_log (actor_user_id, project_id, action, target, detail)
VALUES (@actor_user_id, @project_id, @action, @target, @detail);

-- name: ListAudit :many
SELECT a.id, a.at, a.actor_user_id, COALESCE(u.email, '')::text AS actor_email,
       a.project_id, a.action, a.target, a.detail
FROM audit_log a LEFT JOIN users u ON u.id = a.actor_user_id
WHERE (sqlc.narg('project_id')::bigint IS NULL OR a.project_id = sqlc.narg('project_id')::bigint)
  AND (sqlc.narg('before_id')::bigint IS NULL OR a.id < sqlc.narg('before_id')::bigint)
ORDER BY a.id DESC
LIMIT @page_size::int;
