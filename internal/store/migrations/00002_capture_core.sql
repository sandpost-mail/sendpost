-- +goose Up
CREATE EXTENSION IF NOT EXISTS pg_trgm;

CREATE TABLE projects (
    id         bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    slug       text NOT NULL UNIQUE CHECK (slug ~ '^[a-z0-9][a-z0-9-]{0,62}$'),
    name       text NOT NULL CHECK (char_length(name) BETWEEN 1 AND 200),
    created_at timestamptz NOT NULL DEFAULT now()
);

-- Machine credentials are server generated. Only a keyed HMAC of the secret
-- is stored, never the secret itself.
CREATE TABLE smtp_credentials (
    id           bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    project_id   bigint NOT NULL REFERENCES projects (id) ON DELETE CASCADE,
    username     text NOT NULL UNIQUE,
    secret_hmac  bytea NOT NULL,
    label        text NOT NULL DEFAULT '',
    created_at   timestamptz NOT NULL DEFAULT now(),
    revoked_at   timestamptz,
    last_used_at timestamptz
);
CREATE INDEX smtp_credentials_project_idx ON smtp_credentials (project_id);

CREATE TABLE messages (
    id             bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    project_id     bigint NOT NULL REFERENCES projects (id) ON DELETE CASCADE,
    received_at    timestamptz NOT NULL DEFAULT now(),
    message_id_hdr text NOT NULL DEFAULT '',
    from_addr      text NOT NULL DEFAULT '',
    subject        text NOT NULL DEFAULT '',
    size_bytes     bigint NOT NULL CHECK (size_bytes >= 0),
    text_body      text NOT NULL DEFAULT '',
    html_body      text NOT NULL DEFAULT '',
    raw            bytea NOT NULL,
    headers        jsonb NOT NULL DEFAULT '{}'::jsonb,
    read_at        timestamptz,
    -- Full text over the body only (the subject uses the trigram index). The
    -- body is truncated so a huge message can never exceed the tsvector size
    -- limit and make the insert fail.
    search         tsvector GENERATED ALWAYS AS (
        to_tsvector('simple', left(text_body, 200000))
    ) STORED
);
CREATE INDEX messages_project_received_idx ON messages (project_id, received_at DESC, id DESC);
CREATE INDEX messages_search_idx ON messages USING gin (search);
CREATE INDEX messages_subject_trgm_idx ON messages USING gin (subject gin_trgm_ops);
CREATE INDEX messages_from_trgm_idx ON messages USING gin (from_addr gin_trgm_ops);

CREATE TABLE message_recipients (
    message_id bigint NOT NULL REFERENCES messages (id) ON DELETE CASCADE,
    kind       text NOT NULL CHECK (kind IN ('to', 'cc', 'bcc', 'envelope')),
    address    text NOT NULL,
    PRIMARY KEY (message_id, kind, address)
);
CREATE INDEX message_recipients_address_trgm_idx ON message_recipients USING gin (address gin_trgm_ops);

-- Attachment bytes are not duplicated: they are read back out of messages.raw
-- using ordinal (position in the parsed attachment then inline list).
CREATE TABLE attachments (
    id           bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    message_id   bigint NOT NULL REFERENCES messages (id) ON DELETE CASCADE,
    ordinal      integer NOT NULL,
    filename     text NOT NULL DEFAULT '',
    content_type text NOT NULL DEFAULT '',
    size_bytes   bigint NOT NULL DEFAULT 0,
    content_id   text NOT NULL DEFAULT '',
    inline       boolean NOT NULL DEFAULT false,
    UNIQUE (message_id, ordinal)
);

CREATE TABLE message_links (
    message_id bigint NOT NULL REFERENCES messages (id) ON DELETE CASCADE,
    position   integer NOT NULL,
    url        text NOT NULL,
    PRIMARY KEY (message_id, url)
);

CREATE TABLE tags (
    id         bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    project_id bigint NOT NULL REFERENCES projects (id) ON DELETE CASCADE,
    name       text NOT NULL CHECK (char_length(name) BETWEEN 1 AND 100),
    UNIQUE (project_id, name)
);

CREATE TABLE message_tags (
    message_id bigint NOT NULL REFERENCES messages (id) ON DELETE CASCADE,
    tag_id     bigint NOT NULL REFERENCES tags (id) ON DELETE CASCADE,
    PRIMARY KEY (message_id, tag_id)
);
CREATE INDEX message_tags_tag_idx ON message_tags (tag_id);

-- kind 'header': header_name must be set, pattern matches its values.
-- kind 'recipient': pattern matches any recipient address.
-- pattern uses '*' as wildcard and is case insensitive, empty matches anything.
-- tag may contain {value}, replaced by the matched header value.
CREATE TABLE tag_rules (
    id          bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    project_id  bigint NOT NULL REFERENCES projects (id) ON DELETE CASCADE,
    kind        text NOT NULL CHECK (kind IN ('header', 'recipient')),
    header_name text NOT NULL DEFAULT '',
    pattern     text NOT NULL DEFAULT '',
    tag         text NOT NULL CHECK (char_length(tag) BETWEEN 1 AND 100),
    created_at  timestamptz NOT NULL DEFAULT now(),
    CHECK (kind <> 'header' OR header_name <> '')
);
CREATE INDEX tag_rules_project_idx ON tag_rules (project_id);

-- +goose Down
DROP TABLE tag_rules;
DROP TABLE message_tags;
DROP TABLE tags;
DROP TABLE message_links;
DROP TABLE attachments;
DROP TABLE message_recipients;
DROP TABLE messages;
DROP TABLE smtp_credentials;
DROP TABLE projects;
