-- name: CreateCredential :one
INSERT INTO smtp_credentials (project_id, username, secret_hmac, label)
VALUES ($1, $2, $3, $4)
RETURNING id, project_id, username, label, created_at, revoked_at, last_used_at;

-- name: ListCredentials :many
SELECT id, project_id, username, label, created_at, revoked_at, last_used_at
FROM smtp_credentials
WHERE project_id = $1
ORDER BY id DESC;

-- name: GetActiveCredentialByUsername :one
SELECT id, project_id, secret_hmac
FROM smtp_credentials
WHERE username = $1 AND revoked_at IS NULL;

-- name: RevokeCredential :execrows
UPDATE smtp_credentials SET revoked_at = now()
WHERE project_id = $1 AND id = $2 AND revoked_at IS NULL;

-- name: TouchCredential :exec
UPDATE smtp_credentials SET last_used_at = now()
WHERE id = $1 AND (last_used_at IS NULL OR last_used_at < now() - interval '1 minute');
