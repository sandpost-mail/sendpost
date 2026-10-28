-- InsertComment only inserts when the message belongs to the project, so a
-- foreign or unknown message id yields no row. The author label is the
-- user's email, else the prefix of the project token.
-- name: InsertComment :one
INSERT INTO comments (project_id, message_id, user_id, author_label, body)
SELECT m.project_id, m.id, sqlc.narg('user_id')::bigint,
       COALESCE((SELECT u.email FROM users u WHERE u.id = sqlc.narg('user_id')::bigint),
                (SELECT 'project token ' || t.prefix FROM api_tokens t
                 WHERE t.id = sqlc.narg('token_id')::bigint AND t.project_id = m.project_id),
                'unknown'),
       @body::text
FROM messages m
WHERE m.project_id = @project_id AND m.id = @message_id
RETURNING id, project_id, message_id, user_id, author_label, body, created_at;

-- name: ListComments :many
SELECT id, project_id, message_id, user_id, author_label, body, created_at
FROM comments
WHERE project_id = @project_id AND message_id = @message_id
  AND id > @after_id::bigint
ORDER BY id
LIMIT @page_size::int;

-- name: CountComments :one
SELECT count(*) FROM comments WHERE project_id = $1 AND message_id = $2;

-- ListActivity merges the safe sources of a project's activity feed: received
-- messages, comments and manual releases (audit rows are picked by an explicit
-- action, never the whole log, so transport and policy changes stay owner only).
-- Keyset order is (at, kind, id) descending, kind compared with the C collation.
-- name: ListActivity :many
WITH ev AS (
    (SELECT m.received_at AS at, 'message'::text AS kind, m.id AS id, m.id AS message_id,
            m.subject AS subject, m.from_addr AS actor, ''::text AS detail
     FROM messages m
     WHERE m.project_id = @project_id
       AND (sqlc.narg('cursor_at')::timestamptz IS NULL
            OR (m.received_at, 'message'::text COLLATE "C", m.id)
               < (sqlc.narg('cursor_at')::timestamptz, sqlc.narg('cursor_kind')::text COLLATE "C", sqlc.narg('cursor_id')::bigint))
     ORDER BY m.received_at DESC, m.id DESC
     LIMIT @page_size::int)
    UNION ALL
    (SELECT c.created_at, 'comment'::text, c.id, c.message_id,
            m.subject, c.author_label, left(c.body, 200)
     FROM comments c JOIN messages m ON m.id = c.message_id AND m.project_id = c.project_id
     WHERE c.project_id = @project_id
       AND (sqlc.narg('cursor_at')::timestamptz IS NULL
            OR (c.created_at, 'comment'::text COLLATE "C", c.id)
               < (sqlc.narg('cursor_at')::timestamptz, sqlc.narg('cursor_kind')::text COLLATE "C", sqlc.narg('cursor_id')::bigint))
     ORDER BY c.created_at DESC, c.id DESC
     LIMIT @page_size::int)
    UNION ALL
    (SELECT a.at, 'release'::text, a.id, COALESCE(m.id, 0), COALESCE(m.subject, ''),
            COALESCE(u.email, ''), COALESCE((SELECT string_agg(x, ', ') FROM jsonb_array_elements_text(
                CASE WHEN jsonb_typeof(a.detail -> 'targets') = 'array' THEN a.detail -> 'targets' ELSE '[]'::jsonb END) x), '')
     FROM audit_log a
     LEFT JOIN users u ON u.id = a.actor_user_id
     LEFT JOIN messages m ON m.project_id = a.project_id AND a.target ~ '^message:[0-9]{1,18}$'
                         AND m.id = substring(a.target FROM 9)::bigint
     WHERE a.project_id = @project_id AND a.action = 'message.release'
       AND (sqlc.narg('cursor_at')::timestamptz IS NULL
            OR (a.at, 'release'::text COLLATE "C", a.id)
               < (sqlc.narg('cursor_at')::timestamptz, sqlc.narg('cursor_kind')::text COLLATE "C", sqlc.narg('cursor_id')::bigint))
     ORDER BY a.at DESC, a.id DESC
     LIMIT @page_size::int)
)
SELECT at::timestamptz AS at, kind::text AS kind, id::bigint AS id, message_id::bigint AS message_id,
       subject::text AS subject, actor::text AS actor, detail::text AS detail
FROM ev
ORDER BY at DESC, kind COLLATE "C" DESC, id DESC
LIMIT @page_size::int;
