-- Messages for the long poll wait endpoint: the filters of ListMessages plus a
-- run filter (header X-Run-Id, tag equal to the run id or tag run:<id>) and an id floor,
-- oldest first so a poller can resume from the last id it saw.
-- name: MatchMessages :many
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
  AND m.id > @after_id::bigint
  AND (sqlc.narg('from_like')::text IS NULL OR m.from_addr ILIKE sqlc.narg('from_like')::text)
  AND (sqlc.narg('to_like')::text IS NULL OR EXISTS (
        SELECT 1 FROM message_recipients r
        WHERE r.message_id = m.id AND r.address ILIKE sqlc.narg('to_like')::text))
  AND (sqlc.narg('subject_like')::text IS NULL OR m.subject ILIKE sqlc.narg('subject_like')::text)
  AND (sqlc.narg('tag')::text IS NULL OR EXISTS (
        SELECT 1 FROM message_tags mt JOIN tags t ON t.id = mt.tag_id
        WHERE mt.message_id = m.id AND t.name = sqlc.narg('tag')::text))
  AND (sqlc.narg('run')::text IS NULL
       OR EXISTS (SELECT 1 FROM jsonb_each(m.headers) h
                  WHERE lower(h.key) = 'x-run-id' AND h.value ? sqlc.narg('run')::text)
       OR EXISTS (SELECT 1 FROM message_tags mt JOIN tags t ON t.id = mt.tag_id
                  WHERE mt.message_id = m.id
                    AND t.name IN (sqlc.narg('run')::text, 'run:' || sqlc.narg('run')::text)))
  AND (sqlc.narg('received_after')::timestamptz IS NULL
       OR m.received_at >= sqlc.narg('received_after')::timestamptz)
  AND (sqlc.narg('received_before')::timestamptz IS NULL
       OR m.received_at < sqlc.narg('received_before')::timestamptz)
  AND (sqlc.narg('body')::text IS NULL
       OR m.search @@ websearch_to_tsquery('simple', sqlc.narg('body')::text))
  AND (sqlc.narg('free')::text IS NULL
       OR m.search @@ websearch_to_tsquery('simple', sqlc.narg('free')::text)
       OR m.subject ILIKE sqlc.narg('free_like')::text)
ORDER BY m.id ASC
LIMIT @page_size::int;
