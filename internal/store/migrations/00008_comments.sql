-- +goose Up
-- M5.5 comments on messages. A comment belongs to one message and, redundantly,
-- to its project (every query filters on project_id, as for deliveries).
-- user_id is NULL for a comment written with a project token, and after the
-- author was deleted: author_label keeps who wrote it (the email, or the
-- token prefix) so the thread stays readable. body is plain text, escaped by
-- the client when rendered, never HTML.
CREATE TABLE comments (
    id           bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    project_id   bigint NOT NULL REFERENCES projects (id) ON DELETE CASCADE,
    message_id   bigint NOT NULL REFERENCES messages (id) ON DELETE CASCADE,
    user_id      bigint REFERENCES users (id) ON DELETE SET NULL,
    author_label text NOT NULL CHECK (char_length(author_label) BETWEEN 1 AND 320),
    body         text NOT NULL CHECK (char_length(body) BETWEEN 1 AND 2000 AND position(E'\\x00' in body) = 0),
    created_at   timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX comments_message_idx ON comments (project_id, message_id, id);
CREATE INDEX comments_activity_idx ON comments (project_id, created_at DESC, id DESC);

-- Project activity reads message.release rows of the audit log by project.
-- audit_log_project_idx (project_id, id DESC) already serves that.

-- +goose Down
DROP TABLE comments;
