-- name: InsertMessage :one
INSERT INTO messages (project_id, received_at, message_id_hdr, from_addr, subject,
                      size_bytes, text_body, html_body, raw, headers)
VALUES (@project_id, @received_at, @message_id_hdr, @from_addr, @subject,
        @size_bytes, @text_body, @html_body, @raw, @headers)
RETURNING id, received_at;

-- Rows are deduplicated by the caller (COPY has no ON CONFLICT).
-- name: InsertRecipients :copyfrom
INSERT INTO message_recipients (message_id, kind, address) VALUES ($1, $2, $3);

-- name: InsertAttachments :copyfrom
INSERT INTO attachments (message_id, ordinal, filename, content_type, size_bytes, content_id, inline)
VALUES ($1, $2, $3, $4, $5, $6, $7);

-- name: InsertLinks :copyfrom
INSERT INTO message_links (message_id, position, url) VALUES ($1, $2, $3);

-- name: ListMessages :many
SELECT m.id, m.received_at, m.from_addr, m.subject, m.size_bytes, m.read_at,
       COALESCE((SELECT array_agg(r.address ORDER BY r.address)
                 FROM message_recipients r
                 WHERE r.message_id = m.id AND r.kind = 'to'), '{}')::text[] AS to_addrs,
       COALESCE((SELECT array_agg(t.name ORDER BY t.name)
                 FROM message_tags mt JOIN tags t ON t.id = mt.tag_id
                 WHERE mt.message_id = m.id), '{}')::text[] AS tag_names,
       (SELECT count(*) FROM attachments a WHERE a.message_id = m.id)::int AS attachment_count
FROM messages m
WHERE m.project_id = @project_id
  AND (sqlc.narg('from_like')::text IS NULL OR m.from_addr ILIKE sqlc.narg('from_like')::text)
  AND (sqlc.narg('to_like')::text IS NULL OR EXISTS (
        SELECT 1 FROM message_recipients r
        WHERE r.message_id = m.id AND r.address ILIKE sqlc.narg('to_like')::text))
  AND (sqlc.narg('subject_like')::text IS NULL OR m.subject ILIKE sqlc.narg('subject_like')::text)
  AND (sqlc.narg('tag')::text IS NULL OR EXISTS (
        SELECT 1 FROM message_tags mt JOIN tags t ON t.id = mt.tag_id
        WHERE mt.message_id = m.id AND t.name = sqlc.narg('tag')::text))
  AND (sqlc.narg('received_after')::timestamptz IS NULL
       OR m.received_at >= sqlc.narg('received_after')::timestamptz)
  AND (sqlc.narg('received_before')::timestamptz IS NULL
       OR m.received_at < sqlc.narg('received_before')::timestamptz)
  AND (sqlc.narg('body')::text IS NULL
       OR m.search @@ websearch_to_tsquery('simple', sqlc.narg('body')::text))
  AND (sqlc.narg('free')::text IS NULL
       OR m.search @@ websearch_to_tsquery('simple', sqlc.narg('free')::text)
       OR m.subject ILIKE sqlc.narg('free_like')::text)
  AND (sqlc.narg('cursor_at')::timestamptz IS NULL
       OR (m.received_at, m.id) < (sqlc.narg('cursor_at')::timestamptz, sqlc.narg('cursor_id')::bigint))
ORDER BY m.received_at DESC, m.id DESC
LIMIT @page_size::int;

-- name: GetMessage :one
SELECT id, project_id, received_at, message_id_hdr, from_addr, subject, size_bytes,
       text_body, html_body, headers, read_at
FROM messages WHERE project_id = $1 AND id = $2;

-- name: GetMessageRaw :one
SELECT raw FROM messages WHERE project_id = $1 AND id = $2;

-- name: MarkMessageRead :execrows
UPDATE messages SET read_at = now()
WHERE project_id = $1 AND id = $2 AND read_at IS NULL;

-- name: MessageExists :one
SELECT EXISTS (SELECT 1 FROM messages WHERE project_id = $1 AND id = $2);

-- name: ListMessageRecipients :many
SELECT kind, address FROM message_recipients
WHERE message_id = $1 ORDER BY kind, address;

-- name: ListMessageAttachments :many
SELECT ordinal, filename, content_type, size_bytes, content_id, inline
FROM attachments WHERE message_id = $1 ORDER BY ordinal;

-- name: ListMessageLinks :many
SELECT url FROM message_links WHERE message_id = $1 ORDER BY position;

-- name: ListMessageTagNames :many
SELECT t.name FROM message_tags mt JOIN tags t ON t.id = mt.tag_id
WHERE mt.message_id = $1 ORDER BY t.name;

-- name: GetMessageHTML :one
SELECT html_body FROM messages WHERE project_id = $1 AND id = $2;
