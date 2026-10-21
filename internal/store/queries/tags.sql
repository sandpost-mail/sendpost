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
-- Tag names are unique without regard to case (the first spelling wins).
-- Names go in sorted order, so concurrent writers lock tag rows in the same
-- order and can not deadlock.
INSERT INTO tags (project_id, name)
SELECT @project_id::bigint, n
FROM (SELECT DISTINCT ON (lower(x)) x AS n FROM unnest(@names::text[]) AS x ORDER BY lower(x), x) AS d
ORDER BY lower(n)
ON CONFLICT (project_id, lower(name)) DO UPDATE SET name = tags.name
RETURNING id;

-- name: AttachTags :exec
INSERT INTO message_tags (message_id, tag_id)
SELECT @message_id::bigint, t FROM unnest(@tag_ids::bigint[]) AS t
ON CONFLICT DO NOTHING;
