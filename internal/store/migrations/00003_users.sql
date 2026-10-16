-- +goose Up
-- Minimal users table for the first-boot admin bootstrap (M1.8). M2.1 adds
-- groups, sessions and tokens around it.
CREATE TABLE users (
    id                   bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    email                text NOT NULL,
    name                 text NOT NULL DEFAULT '',
    password_hash        text NOT NULL,
    is_admin             boolean NOT NULL DEFAULT false,
    must_change_password boolean NOT NULL DEFAULT false,
    created_at           timestamptz NOT NULL DEFAULT now(),
    disabled_at          timestamptz
);
CREATE UNIQUE INDEX users_email_lower_idx ON users (lower(email));

-- +goose Down
DROP TABLE users;
