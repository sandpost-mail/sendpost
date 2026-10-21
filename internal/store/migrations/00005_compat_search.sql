-- +goose Up
-- M3 Mailpit compatible search. A filter is a list of terms (kind, value,
-- negate) that must all hold. kind picks the field, value is an ILIKE pattern
-- (already escaped by the caller) or, for the date, size and tag kinds, a
-- timestamp, a byte count or an exact name. A term holds when its match
-- differs from negate. An unknown kind never matches, so a mistake in the
-- caller fails closed (nothing is selected, deleted or changed).
--
-- The function takes the columns it needs, not the messages row: a whole row
-- argument would make Postgres detoast raw and html_body for every message.
-- It only reads what it is given. Callers always restrict the project in
-- their own WHERE clause, and skip the call when the filter has no term.

-- +goose StatementBegin
CREATE FUNCTION sp_filter_match(
    p_id bigint, p_from text, p_subject text, p_message_id text, p_text text,
    p_read_at timestamptz, p_received_at timestamptz, p_size bigint, p_headers jsonb,
    kinds text[], vals text[], negs boolean[])
RETURNS boolean
LANGUAGE sql STABLE
AS $$
    SELECT cardinality(kinds) = 0 OR NOT EXISTS (
        SELECT 1
        FROM unnest(kinds, vals, negs) AS t(kind, val, neg)
        WHERE (CASE t.kind
            WHEN 'from' THEN p_from ILIKE t.val
            WHEN 'to' THEN EXISTS (SELECT 1 FROM message_recipients r
                WHERE r.message_id = p_id AND r.kind = 'to' AND r.address ILIKE t.val)
            WHEN 'cc' THEN EXISTS (SELECT 1 FROM message_recipients r
                WHERE r.message_id = p_id AND r.kind = 'cc' AND r.address ILIKE t.val)
            WHEN 'bcc' THEN EXISTS (SELECT 1 FROM message_recipients r
                WHERE r.message_id = p_id AND r.address ILIKE t.val
                  AND (r.kind = 'bcc' OR (r.kind = 'envelope' AND NOT EXISTS (
                        SELECT 1 FROM message_recipients o
                        WHERE o.message_id = p_id AND o.kind IN ('to', 'cc')
                          AND lower(o.address) = lower(r.address)))))
            WHEN 'reply-to' THEN EXISTS (
                SELECT 1 FROM jsonb_array_elements_text(
                    CASE WHEN jsonb_typeof(p_headers -> 'Reply-To') = 'array'
                         THEN p_headers -> 'Reply-To' ELSE '[]'::jsonb END) h(v)
                WHERE h.v ILIKE t.val)
            WHEN 'addressed' THEN p_from ILIKE t.val
                OR EXISTS (SELECT 1 FROM message_recipients r
                           WHERE r.message_id = p_id AND r.kind <> 'envelope' AND r.address ILIKE t.val)
                OR EXISTS (
                    SELECT 1 FROM jsonb_array_elements_text(
                        CASE WHEN jsonb_typeof(p_headers -> 'Reply-To') = 'array'
                             THEN p_headers -> 'Reply-To' ELSE '[]'::jsonb END) h(v)
                    WHERE h.v ILIKE t.val)
            WHEN 'subject' THEN p_subject ILIKE t.val
            WHEN 'message-id' THEN p_message_id ILIKE t.val
            WHEN 'tag' THEN EXISTS (SELECT 1 FROM message_tags mt JOIN tags g ON g.id = mt.tag_id
                WHERE mt.message_id = p_id AND lower(g.name) = lower(t.val))
            WHEN 'is-read' THEN p_read_at IS NOT NULL
            WHEN 'is-tagged' THEN EXISTS (SELECT 1 FROM message_tags mt WHERE mt.message_id = p_id)
            WHEN 'has-attachment' THEN EXISTS (SELECT 1 FROM attachments a
                WHERE a.message_id = p_id AND NOT a.inline)
            WHEN 'has-inline' THEN EXISTS (SELECT 1 FROM attachments a
                WHERE a.message_id = p_id AND a.inline)
            WHEN 'after' THEN p_received_at >= t.val::timestamptz
            WHEN 'before' THEN p_received_at <= t.val::timestamptz
            WHEN 'larger' THEN p_size > t.val::bigint
            WHEN 'smaller' THEN p_size < t.val::bigint
            WHEN 'text' THEN p_subject ILIKE t.val
                OR p_text ILIKE t.val
                OR p_from ILIKE t.val
                OR EXISTS (SELECT 1 FROM message_recipients r
                           WHERE r.message_id = p_id AND r.address ILIKE t.val)
            ELSE NULL
        END) IS DISTINCT FROM (NOT t.neg)
    )
$$;
-- +goose StatementEnd

-- +goose Down
DROP FUNCTION sp_filter_match(bigint, text, text, text, text, timestamptz, timestamptz, bigint, jsonb, text[], text[], boolean[]);
