-- +goose Up
-- M3 Mailpit compatible search. A filter is a list of terms (kind, value,
-- negate) that must all hold. kind picks the field, value is an ILIKE pattern
-- (already escaped by the caller) or, for the date, size and tag kinds, a
-- timestamp, a byte count or an exact name. A term holds when its match
-- differs from negate. An unknown kind never matches, so a mistake in the
-- caller fails closed (nothing is selected, deleted or changed).
--
-- The function only reads the row it is given. Callers always restrict the
-- project in their own WHERE clause.

-- +goose StatementBegin
CREATE FUNCTION sp_filter_match(m messages, kinds text[], vals text[], negs boolean[])
RETURNS boolean
LANGUAGE sql STABLE
AS $$
    SELECT NOT EXISTS (
        SELECT 1
        FROM unnest(kinds, vals, negs) AS t(kind, val, neg)
        WHERE (CASE t.kind
            WHEN 'from' THEN m.from_addr ILIKE t.val
            WHEN 'to' THEN EXISTS (SELECT 1 FROM message_recipients r
                WHERE r.message_id = m.id AND r.kind = 'to' AND r.address ILIKE t.val)
            WHEN 'cc' THEN EXISTS (SELECT 1 FROM message_recipients r
                WHERE r.message_id = m.id AND r.kind = 'cc' AND r.address ILIKE t.val)
            WHEN 'bcc' THEN EXISTS (SELECT 1 FROM message_recipients r
                WHERE r.message_id = m.id AND r.address ILIKE t.val
                  AND (r.kind = 'bcc' OR (r.kind = 'envelope' AND NOT EXISTS (
                        SELECT 1 FROM message_recipients o
                        WHERE o.message_id = m.id AND o.kind IN ('to', 'cc')
                          AND lower(o.address) = lower(r.address)))))
            WHEN 'reply-to' THEN EXISTS (
                SELECT 1 FROM jsonb_array_elements_text(
                    CASE WHEN jsonb_typeof(m.headers -> 'Reply-To') = 'array'
                         THEN m.headers -> 'Reply-To' ELSE '[]'::jsonb END) h(v)
                WHERE h.v ILIKE t.val)
            WHEN 'addressed' THEN m.from_addr ILIKE t.val
                OR EXISTS (SELECT 1 FROM message_recipients r
                           WHERE r.message_id = m.id AND r.kind <> 'envelope' AND r.address ILIKE t.val)
                OR EXISTS (
                    SELECT 1 FROM jsonb_array_elements_text(
                        CASE WHEN jsonb_typeof(m.headers -> 'Reply-To') = 'array'
                             THEN m.headers -> 'Reply-To' ELSE '[]'::jsonb END) h(v)
                    WHERE h.v ILIKE t.val)
            WHEN 'subject' THEN m.subject ILIKE t.val
            WHEN 'message-id' THEN m.message_id_hdr ILIKE t.val
            WHEN 'tag' THEN EXISTS (SELECT 1 FROM message_tags mt JOIN tags g ON g.id = mt.tag_id
                WHERE mt.message_id = m.id AND g.name = t.val)
            WHEN 'is-read' THEN m.read_at IS NOT NULL
            WHEN 'is-tagged' THEN EXISTS (SELECT 1 FROM message_tags mt WHERE mt.message_id = m.id)
            WHEN 'has-attachment' THEN EXISTS (SELECT 1 FROM attachments a
                WHERE a.message_id = m.id AND NOT a.inline)
            WHEN 'has-inline' THEN EXISTS (SELECT 1 FROM attachments a
                WHERE a.message_id = m.id AND a.inline)
            WHEN 'after' THEN m.received_at >= t.val::timestamptz
            WHEN 'before' THEN m.received_at <= t.val::timestamptz
            WHEN 'larger' THEN m.size_bytes > t.val::bigint
            WHEN 'smaller' THEN m.size_bytes < t.val::bigint
            WHEN 'text' THEN m.subject ILIKE t.val
                OR m.text_body ILIKE t.val
                OR m.from_addr ILIKE t.val
                OR EXISTS (SELECT 1 FROM message_recipients r
                           WHERE r.message_id = m.id AND r.address ILIKE t.val)
            ELSE NULL
        END) IS DISTINCT FROM (NOT t.neg)
    )
$$;
-- +goose StatementEnd

-- +goose Down
DROP FUNCTION sp_filter_match(messages, text[], text[], boolean[]);
