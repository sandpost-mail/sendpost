-- name: CreateSession :exec
INSERT INTO sessions (id, user_id, expires_at) VALUES ($1, $2, $3);

-- name: GetSessionUser :one
SELECT u.id, u.email, u.name, u.is_admin, u.must_change_password, u.created_at, u.disabled_at,
       s.expires_at
FROM sessions s JOIN users u ON u.id = s.user_id
WHERE s.id = $1 AND s.expires_at > now() AND u.disabled_at IS NULL;

-- name: DeleteSession :exec
DELETE FROM sessions WHERE id = $1;

-- name: DeleteUserSessions :exec
DELETE FROM sessions WHERE user_id = $1;

-- name: DeleteUserSessionsExcept :exec
DELETE FROM sessions WHERE user_id = $1 AND id <> $2;

-- name: DeleteExpiredSessions :execrows
DELETE FROM sessions WHERE expires_at <= now();
