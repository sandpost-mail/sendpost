-- name: CountUsers :one
SELECT count(*) FROM users;

-- name: CreateAdminIfNoUsers :execrows
INSERT INTO users (email, name, password_hash, is_admin, must_change_password)
SELECT @email::text, @name::text, @password_hash::text, true, true
WHERE NOT EXISTS (SELECT 1 FROM users);

-- name: CreateUser :one
INSERT INTO users (email, name, password_hash, is_admin, must_change_password)
VALUES ($1, $2, $3, $4, $5)
RETURNING id, email, name, is_admin, must_change_password, created_at, disabled_at;

-- name: GetUser :one
SELECT id, email, name, is_admin, must_change_password, created_at, disabled_at
FROM users WHERE id = $1;

-- name: GetUserForLogin :one
SELECT id, email, name, password_hash, is_admin, must_change_password, created_at, disabled_at
FROM users WHERE lower(email) = lower(@email::text);

-- name: GetUserPasswordHash :one
SELECT password_hash FROM users WHERE id = $1;

-- name: ListUsers :many
SELECT id, email, name, is_admin, must_change_password, created_at, disabled_at
FROM users ORDER BY lower(email), id;

-- name: UpdateUser :one
UPDATE users SET
    name        = COALESCE(sqlc.narg('name')::text, name),
    is_admin    = COALESCE(sqlc.narg('is_admin')::boolean, is_admin),
    disabled_at = CASE
        WHEN sqlc.narg('disabled')::boolean IS NULL THEN disabled_at
        WHEN sqlc.narg('disabled')::boolean THEN COALESCE(disabled_at, now())
        ELSE NULL END
WHERE id = @id
RETURNING id, email, name, is_admin, must_change_password, created_at, disabled_at;

-- name: SetUserPassword :execrows
UPDATE users SET password_hash = $2, must_change_password = $3 WHERE id = $1;

-- Locks the active admins for the rest of the transaction, so concurrent
-- demotions can not both pass the "last admin" check.
-- name: LockActiveAdmins :many
SELECT id FROM users WHERE is_admin AND disabled_at IS NULL ORDER BY id FOR UPDATE;
