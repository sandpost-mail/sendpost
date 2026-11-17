-- Queries of the Mailpit compatible API. A filter is passed as three parallel
-- arrays (kinds, vals, negs), see sp_filter_match in migration 00005. Empty
-- arrays match every message and skip the function. Every statement restricts project_id itself.

-- name: CompatListMessages :many
SELECT m.id, m.received_at, m.message_id_hdr, m.from_addr, m.subject, m.size_bytes, m.read_at,
       m.headers,
       left(m.text_body, 1000)::text AS text_head,
       CASE WHEN m.text_body = '' THEN left(m.html_body, 4000) ELSE '' END::text AS html_head,
       COALESCE((SELECT jsonb_agg(jsonb_build_object('kind', r.kind, 'address', r.address)
                                  ORDER BY r.kind, r.address)
                 FROM message_recipients r WHERE r.message_id = m.id), '[]'::jsonb)::jsonb AS recipients,
       COALESCE((SELECT array_agg(t.name ORDER BY t.name)
                 FROM message_tags mt JOIN tags t ON t.id = mt.tag_id
                 WHERE mt.message_id = m.id), '{}')::text[] AS tag_names,
       (SELECT count(*) FROM attachments a WHERE a.message_id = m.id AND NOT a.inline)::int AS attachment_count
FROM messages m
WHERE m.project_id = @project_id
  AND (cardinality(@kinds::text[]) = 0 OR sp_filter_match(m.id, m.from_addr, m.subject, m.message_id_hdr, m.text_body, m.read_at, m.received_at, m.size_bytes, m.headers, @kinds::text[], @vals::text[], @negs::boolean[]))
ORDER BY m.received_at DESC, m.id DESC
OFFSET @row_offset::int
LIMIT @row_limit::int;

-- One page of a list or search together with the counts the Mailpit list
-- envelope needs: the whole mailbox (total, unread) and the matches. The
-- filter is evaluated once per message of the project (scored is
-- materialized), and the per message sub-selects run for the page only.
-- There is always one row: when the page is empty (no match, or an offset
-- past the end) it carries the counts and id = 0.
-- name: CompatListPage :many
WITH scored AS MATERIALIZED (
    SELECT m.id, m.received_at, m.read_at IS NULL AS unread,
           (cardinality(@kinds::text[]) = 0 OR sp_filter_match(m.id, m.from_addr, m.subject, m.message_id_hdr, m.text_body, m.read_at, m.received_at, m.size_bytes, m.headers, @kinds::text[], @vals::text[], @negs::boolean[])) AS matched
    FROM messages m
    WHERE m.project_id = @project_id
), totals AS (
    SELECT count(*)::bigint AS mailbox_total,
           (count(*) FILTER (WHERE s.unread))::bigint AS mailbox_unread,
           (count(*) FILTER (WHERE s.matched))::bigint AS matches_total,
           (count(*) FILTER (WHERE s.matched AND s.unread))::bigint AS matches_unread
    FROM scored s
), page AS (
    SELECT s.id FROM scored s
    WHERE s.matched
    ORDER BY s.received_at DESC, s.id DESC
    OFFSET @row_offset::int
    LIMIT @row_limit::int
)
SELECT t.mailbox_total, t.mailbox_unread, t.matches_total, t.matches_unread,
       COALESCE(m.id, 0)::bigint AS id,
       COALESCE(m.received_at, 'epoch'::timestamptz)::timestamptz AS received_at,
       COALESCE(m.message_id_hdr, '')::text AS message_id_hdr,
       COALESCE(m.from_addr, '')::text AS from_addr,
       COALESCE(m.subject, '')::text AS subject,
       COALESCE(m.size_bytes, 0)::bigint AS size_bytes,
       m.read_at,
       COALESCE(m.headers, '{}'::jsonb)::jsonb AS headers,
       COALESCE(left(m.text_body, 1000), '')::text AS text_head,
       COALESCE(CASE WHEN m.text_body = '' THEN left(m.html_body, 4000) ELSE '' END, '')::text AS html_head,
       COALESCE((SELECT jsonb_agg(jsonb_build_object('kind', r.kind, 'address', r.address)
                                  ORDER BY r.kind, r.address)
                 FROM message_recipients r WHERE r.message_id = m.id), '[]'::jsonb)::jsonb AS recipients,
       COALESCE((SELECT array_agg(g.name ORDER BY g.name)
                 FROM message_tags mt JOIN tags g ON g.id = mt.tag_id
                 WHERE mt.message_id = m.id), '{}')::text[] AS tag_names,
       (SELECT count(*) FROM attachments a WHERE a.message_id = m.id AND NOT a.inline)::int AS attachment_count
FROM totals t
LEFT JOIN page p ON true
LEFT JOIN messages m ON m.id = p.id
ORDER BY m.received_at DESC NULLS LAST, m.id DESC NULLS LAST;

-- name: CompatCountMessages :one
SELECT count(*)::bigint AS total,
       (count(*) FILTER (WHERE m.read_at IS NULL))::bigint AS unread
FROM messages m
WHERE m.project_id = @project_id
  AND (cardinality(@kinds::text[]) = 0 OR sp_filter_match(m.id, m.from_addr, m.subject, m.message_id_hdr, m.text_body, m.read_at, m.received_at, m.size_bytes, m.headers, @kinds::text[], @vals::text[], @negs::boolean[]));

-- name: CompatDeleteMatching :execrows
DELETE FROM messages m
WHERE m.project_id = @project_id
  AND (cardinality(@kinds::text[]) = 0 OR sp_filter_match(m.id, m.from_addr, m.subject, m.message_id_hdr, m.text_body, m.read_at, m.received_at, m.size_bytes, m.headers, @kinds::text[], @vals::text[], @negs::boolean[]));

-- name: CompatDeleteByIDs :execrows
DELETE FROM messages WHERE project_id = @project_id AND id = ANY(@ids::bigint[]);

-- name: CompatSetReadMatching :execrows
UPDATE messages m
SET read_at = CASE WHEN @read::boolean THEN now() ELSE NULL END
WHERE m.project_id = @project_id
  AND (m.read_at IS NOT NULL) <> @read::boolean
  AND (cardinality(@kinds::text[]) = 0 OR sp_filter_match(m.id, m.from_addr, m.subject, m.message_id_hdr, m.text_body, m.read_at, m.received_at, m.size_bytes, m.headers, @kinds::text[], @vals::text[], @negs::boolean[]));

-- name: CompatSetReadByIDs :execrows
UPDATE messages
SET read_at = CASE WHEN @read::boolean THEN now() ELSE NULL END
WHERE project_id = @project_id AND id = ANY(@ids::bigint[])
  AND (read_at IS NOT NULL) <> @read::boolean;

-- name: CompatProjectMessageIDs :many
SELECT id FROM messages WHERE project_id = @project_id AND id = ANY(@ids::bigint[]);

-- name: CompatClearMessageTags :exec
DELETE FROM message_tags mt
USING messages m
WHERE mt.message_id = m.id AND m.project_id = @project_id AND m.id = ANY(@ids::bigint[]);

-- name: CompatAttachTags :exec
INSERT INTO message_tags (message_id, tag_id)
SELECT m.id, t.id
FROM messages m
JOIN tags t ON t.project_id = m.project_id AND t.id = ANY(@tag_ids::bigint[])
WHERE m.project_id = @project_id AND m.id = ANY(@ids::bigint[])
ON CONFLICT DO NOTHING;

-- Drops tags no message carries. SKIP LOCKED leaves alone a tag row that a
-- concurrent ingest or tag update has just upserted and not yet attached.
-- name: CompatPruneTags :exec
DELETE FROM tags
WHERE id IN (SELECT t.id FROM tags t
             WHERE t.project_id = @project_id
               AND NOT EXISTS (SELECT 1 FROM message_tags mt WHERE mt.tag_id = t.id)
             FOR UPDATE OF t SKIP LOCKED);

-- Only tags that are on at least one message, with their message count.
-- name: CompatListTags :many
SELECT t.name, count(*)::bigint AS messages
FROM tags t JOIN message_tags mt ON mt.tag_id = t.id
WHERE t.project_id = @project_id
GROUP BY t.id, t.name
ORDER BY t.name;

-- name: CompatRenameTag :execrows
UPDATE tags SET name = @new_name WHERE project_id = @project_id AND lower(name) = lower(@old_name);

-- name: CompatDeleteTag :execrows
DELETE FROM tags WHERE project_id = @project_id AND lower(name) = lower(@name);
