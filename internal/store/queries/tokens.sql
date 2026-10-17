-- name: CreateToken :one
INSERT INTO api_tokens (owner_user_id, project_id, token_hmac, prefix, label, scopes, created_by)
VALUES (@owner_user_id, @project_id, @token_hmac, @prefix, @label, @scopes::text[], @created_by)
RETURNING id, owner_user_id, project_id, prefix, label, scopes, created_by, created_at, revoked_at, last_used_at;

-- name: ListUserTokens :many
SELECT id, owner_user_id, project_id, prefix, label, scopes, created_by, created_at, revoked_at, last_used_at
FROM api_tokens WHERE owner_user_id = $1 ORDER BY id DESC;

-- name: ListProjectTokens :many
SELECT id, owner_user_id, project_id, prefix, label, scopes, created_by, created_at, revoked_at, last_used_at
FROM api_tokens WHERE project_id = $1 ORDER BY id DESC;

-- name: RevokeUserToken :execrows
UPDATE api_tokens SET revoked_at = now()
WHERE owner_user_id = $1 AND id = $2 AND revoked_at IS NULL;

-- name: RevokeProjectToken :execrows
UPDATE api_tokens SET revoked_at = now()
WHERE project_id = $1 AND id = $2 AND revoked_at IS NULL;

-- The owner is joined so a disabled user's tokens stop working at once.
-- name: GetActiveTokenByHMAC :one
SELECT t.id, t.owner_user_id, t.project_id, t.scopes,
       COALESCE(u.is_admin, false)::boolean AS user_is_admin,
       COALESCE(u.must_change_password, false)::boolean AS user_must_change_password
FROM api_tokens t
LEFT JOIN users u ON u.id = t.owner_user_id
WHERE t.token_hmac = $1 AND t.revoked_at IS NULL
  AND (t.owner_user_id IS NULL OR u.disabled_at IS NULL);

-- name: TouchToken :exec
UPDATE api_tokens SET last_used_at = now()
WHERE id = $1 AND (last_used_at IS NULL OR last_used_at < now() - interval '1 minute');
