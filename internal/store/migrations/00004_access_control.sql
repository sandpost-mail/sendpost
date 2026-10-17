-- +goose Up
-- M2.1 access control: groups, project role assignments, sessions, API tokens
-- and the audit log. users comes from 00003.
CREATE TABLE groups (
    id         bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    name       text NOT NULL CHECK (char_length(name) BETWEEN 1 AND 100),
    created_at timestamptz NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX groups_name_lower_idx ON groups (lower(name));

CREATE TABLE group_members (
    group_id bigint NOT NULL REFERENCES groups (id) ON DELETE CASCADE,
    user_id  bigint NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    added_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (group_id, user_id)
);
CREATE INDEX group_members_user_idx ON group_members (user_id);

CREATE TABLE project_groups (
    project_id bigint NOT NULL REFERENCES projects (id) ON DELETE CASCADE,
    group_id   bigint NOT NULL REFERENCES groups (id) ON DELETE CASCADE,
    role       text NOT NULL CHECK (role IN ('viewer', 'member', 'owner')),
    PRIMARY KEY (project_id, group_id)
);
CREATE INDEX project_groups_group_idx ON project_groups (group_id);

-- id is the HMAC of the cookie value, so a database leak does not yield
-- usable sessions.
CREATE TABLE sessions (
    id         bytea PRIMARY KEY,
    user_id    bigint NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    created_at timestamptz NOT NULL DEFAULT now(),
    expires_at timestamptz NOT NULL
);
CREATE INDEX sessions_user_idx ON sessions (user_id);
CREATE INDEX sessions_expires_idx ON sessions (expires_at);

-- A token belongs to exactly one user or one project. Only a keyed HMAC of
-- the secret is stored, prefix is the non secret display part.
CREATE TABLE api_tokens (
    id            bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    owner_user_id bigint REFERENCES users (id) ON DELETE CASCADE,
    project_id    bigint REFERENCES projects (id) ON DELETE CASCADE,
    token_hmac    bytea NOT NULL UNIQUE,
    prefix        text NOT NULL,
    label         text NOT NULL DEFAULT '',
    scopes        text[] NOT NULL,
    created_by    bigint REFERENCES users (id) ON DELETE SET NULL,
    created_at    timestamptz NOT NULL DEFAULT now(),
    revoked_at    timestamptz,
    last_used_at  timestamptz,
    CHECK ((owner_user_id IS NULL) <> (project_id IS NULL)),
    CHECK (scopes <@ ARRAY['read', 'write', 'manage']::text[] AND cardinality(scopes) > 0)
);
CREATE INDEX api_tokens_owner_idx ON api_tokens (owner_user_id) WHERE owner_user_id IS NOT NULL;
CREATE INDEX api_tokens_project_idx ON api_tokens (project_id) WHERE project_id IS NOT NULL;

-- Rows survive the deletion of their actor or project. detail never holds secrets.
CREATE TABLE audit_log (
    id            bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    at            timestamptz NOT NULL DEFAULT now(),
    actor_user_id bigint REFERENCES users (id) ON DELETE SET NULL,
    project_id    bigint REFERENCES projects (id) ON DELETE SET NULL,
    action        text NOT NULL,
    target        text NOT NULL DEFAULT '',
    detail        jsonb NOT NULL DEFAULT '{}'::jsonb
);
CREATE INDEX audit_log_at_idx ON audit_log (id DESC);
CREATE INDEX audit_log_project_idx ON audit_log (project_id, id DESC) WHERE project_id IS NOT NULL;

-- +goose Down
DROP TABLE audit_log;
DROP TABLE api_tokens;
DROP TABLE sessions;
DROP TABLE project_groups;
DROP TABLE group_members;
DROP TABLE groups;
