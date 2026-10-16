-- name: ListTagRules :many
SELECT id, project_id, kind, header_name, pattern, tag, created_at
FROM tag_rules WHERE project_id = $1 ORDER BY id;

-- name: CreateTagRule :one
INSERT INTO tag_rules (project_id, kind, header_name, pattern, tag)
VALUES ($1, $2, $3, $4, $5)
RETURNING id, project_id, kind, header_name, pattern, tag, created_at;

-- name: DeleteTagRule :execrows
DELETE FROM tag_rules WHERE project_id = $1 AND id = $2;

-- name: UpsertTags :many
INSERT INTO tags (project_id, name)
SELECT @project_id::bigint, n FROM unnest(@names::text[]) AS n
ON CONFLICT (project_id, name) DO UPDATE SET name = EXCLUDED.name
RETURNING id;

-- name: AttachTags :exec
INSERT INTO message_tags (message_id, tag_id)
SELECT @message_id::bigint, t FROM unnest(@tag_ids::bigint[]) AS t
ON CONFLICT DO NOTHING;
