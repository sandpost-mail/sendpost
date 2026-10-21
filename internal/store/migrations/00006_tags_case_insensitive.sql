-- +goose Up
-- Tag names are case insensitive (as in Mailpit): "Foo" and "foo" are one tag.
-- Existing case variants are merged into the oldest one first.
WITH ranked AS (
    SELECT id, first_value(id) OVER (PARTITION BY project_id, lower(name) ORDER BY id) AS keep FROM tags
)
INSERT INTO message_tags (message_id, tag_id)
SELECT mt.message_id, r.keep FROM message_tags mt JOIN ranked r ON r.id = mt.tag_id
WHERE r.id <> r.keep
ON CONFLICT DO NOTHING;

WITH ranked AS (
    SELECT id, first_value(id) OVER (PARTITION BY project_id, lower(name) ORDER BY id) AS keep FROM tags
)
DELETE FROM tags WHERE id IN (SELECT id FROM ranked WHERE id <> keep);

CREATE UNIQUE INDEX tags_project_lower_name_idx ON tags (project_id, lower(name));

-- +goose Down
DROP INDEX tags_project_lower_name_idx;
