-- +goose Up
-- M4.1 delivery policy: one optional outbound transport per project, one
-- policy per project and the record of every outbound attempt. A project
-- without a policy row is in capture mode: nothing is ever sent.

-- secret_enc is AES-GCM sealed (see internal/secrets), NULL for a relay
-- without authentication. A transport is created disabled.
CREATE TABLE transports (
    id         bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    project_id bigint NOT NULL UNIQUE REFERENCES projects (id) ON DELETE CASCADE,
    kind       text NOT NULL CHECK (kind IN ('smtp', 'ses')),
    host       text NOT NULL CHECK (char_length(host) BETWEEN 1 AND 253),
    port       integer NOT NULL CHECK (port BETWEEN 1 AND 65535),
    username   text NOT NULL DEFAULT '' CHECK (char_length(username) <= 255),
    secret_enc bytea,
    tls_mode   text NOT NULL CHECK (tls_mode IN ('starttls', 'implicit', 'none')),
    -- Envelope sender (MAIL FROM). Empty means the null sender.
    sender     text NOT NULL DEFAULT '' CHECK (char_length(sender) <= 254),
    enabled    boolean NOT NULL DEFAULT false,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);

-- The engine validates redirect_address, allowlist entries and the prefix
-- again on every send, so a row that bypassed the API still fails closed.
CREATE TABLE delivery_policies (
    project_id       bigint PRIMARY KEY REFERENCES projects (id) ON DELETE CASCADE,
    mode             text NOT NULL DEFAULT 'capture' CHECK (mode IN ('capture', 'allow', 'redirect')),
    allowlist        jsonb NOT NULL DEFAULT '[]'::jsonb CHECK (jsonb_typeof(allowlist) = 'array'),
    redirect_address text NOT NULL DEFAULT '',
    subject_prefix   text NOT NULL DEFAULT '',
    updated_at       timestamptz NOT NULL DEFAULT now()
);

-- One row per original recipient and run. run is 'auto' for what the policy
-- does after capture and 'release:<id>' for each manual release. recipient is
-- empty on the single row that records a block of the whole run.
CREATE TABLE deliveries (
    id                  bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    project_id          bigint NOT NULL REFERENCES projects (id) ON DELETE CASCADE,
    message_id          bigint NOT NULL REFERENCES messages (id) ON DELETE CASCADE,
    run                 text NOT NULL,
    kind                text NOT NULL CHECK (kind IN ('deliver', 'redirect', 'release', 'skip', 'blocked')),
    recipient           text NOT NULL,
    effective_recipient text NOT NULL DEFAULT '',
    status              text NOT NULL CHECK (status IN ('pending', 'sent', 'failed', 'skipped', 'blocked')),
    error               text NOT NULL DEFAULT '',
    attempts            integer NOT NULL DEFAULT 0,
    created_at          timestamptz NOT NULL DEFAULT now(),
    updated_at          timestamptz NOT NULL DEFAULT now(),
    sent_at             timestamptz,
    UNIQUE (message_id, run, recipient)
);
CREATE INDEX deliveries_project_message_idx ON deliveries (project_id, message_id, id);

-- +goose Down
DROP TABLE deliveries;
DROP TABLE delivery_policies;
DROP TABLE transports;
