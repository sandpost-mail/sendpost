-- +goose Up
-- Token attribution in the audit log: the API token a write was made with.
-- NULL for session and system actions, and after the token was deleted (the
-- row survives, like actor_user_id). Tokens are normally revoked, not deleted.
ALTER TABLE audit_log ADD COLUMN actor_token_id bigint REFERENCES api_tokens (id) ON DELETE SET NULL;
-- Serves the ON DELETE SET NULL lookup when a user (and its tokens) is deleted.
CREATE INDEX audit_log_actor_token_idx ON audit_log (actor_token_id) WHERE actor_token_id IS NOT NULL;

-- +goose Down
DROP INDEX audit_log_actor_token_idx;
ALTER TABLE audit_log DROP COLUMN actor_token_id;
