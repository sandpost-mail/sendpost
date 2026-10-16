-- name: CountUsers :one
SELECT count(*) FROM users;

-- name: CreateAdminIfNoUsers :execrows
INSERT INTO users (email, name, password_hash, is_admin, must_change_password)
SELECT @email::text, @name::text, @password_hash::text, true, true
WHERE NOT EXISTS (SELECT 1 FROM users);
