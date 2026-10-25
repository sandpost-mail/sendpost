-- name: GetTransport :one
SELECT id, project_id, kind, host, port, username, secret_enc, tls_mode, sender, enabled, created_at, updated_at
FROM transports WHERE project_id = $1;

-- name: UpsertTransport :one
INSERT INTO transports (project_id, kind, host, port, username, secret_enc, tls_mode, sender, enabled)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
ON CONFLICT (project_id) DO UPDATE SET
    kind = EXCLUDED.kind, host = EXCLUDED.host, port = EXCLUDED.port, username = EXCLUDED.username,
    secret_enc = EXCLUDED.secret_enc, tls_mode = EXCLUDED.tls_mode, sender = EXCLUDED.sender,
    enabled = EXCLUDED.enabled, updated_at = now()
RETURNING id, project_id, kind, host, port, username, secret_enc, tls_mode, sender, enabled, created_at, updated_at;

-- name: DeleteTransport :execrows
DELETE FROM transports WHERE project_id = $1;

-- name: GetDeliveryPolicy :one
SELECT project_id, mode, allowlist, redirect_address, subject_prefix, updated_at
FROM delivery_policies WHERE project_id = $1;

-- name: UpsertDeliveryPolicy :one
INSERT INTO delivery_policies (project_id, mode, allowlist, redirect_address, subject_prefix)
VALUES ($1, $2, $3, $4, $5)
ON CONFLICT (project_id) DO UPDATE SET
    mode = EXCLUDED.mode, allowlist = EXCLUDED.allowlist, redirect_address = EXCLUDED.redirect_address,
    subject_prefix = EXCLUDED.subject_prefix, updated_at = now()
RETURNING project_id, mode, allowlist, redirect_address, subject_prefix, updated_at;

-- name: DeliveryPolicyMode :one
SELECT mode FROM delivery_policies WHERE project_id = $1;

-- name: GetDeliverySource :one
-- The raw message for the delivery worker, only when it belongs to the project.
SELECT id, raw FROM messages WHERE id = @message_id AND project_id = @project_id;

-- name: ListDeliveryRecipients :many
SELECT r.kind, r.address
FROM message_recipients r JOIN messages m ON m.id = r.message_id
WHERE m.id = @message_id AND m.project_id = @project_id
ORDER BY r.kind, r.address;

-- name: UpsertDelivery :exec
-- Rows already sent, failed or blocked keep their outcome. A pending row follows
-- the current decision, so a policy change between attempts is honoured.
INSERT INTO deliveries (project_id, message_id, run, kind, recipient, effective_recipient, status, error)
SELECT m.project_id, m.id, @run::text, @kind::text, @recipient::text, @effective_recipient::text, @status::text, @error::text
FROM messages m WHERE m.id = @message_id AND m.project_id = @project_id
ON CONFLICT (message_id, run, recipient) DO UPDATE SET
    kind = EXCLUDED.kind, effective_recipient = EXCLUDED.effective_recipient,
    status = EXCLUDED.status, error = EXCLUDED.error, updated_at = now()
WHERE deliveries.status = 'pending';

-- name: ListRunDeliveries :many
SELECT id, project_id, message_id, run, kind, recipient, effective_recipient, status, error, attempts,
       created_at, updated_at, sent_at
FROM deliveries
WHERE project_id = @project_id AND message_id = @message_id AND run = @run
ORDER BY id;

-- name: ListMessageDeliveries :many
SELECT id, project_id, message_id, run, kind, recipient, effective_recipient, status, error, attempts,
       created_at, updated_at, sent_at
FROM deliveries
WHERE project_id = @project_id AND message_id = @message_id
ORDER BY id;

-- name: MarkDeliverySent :exec
UPDATE deliveries SET status = 'sent', error = '', attempts = attempts + 1, sent_at = now(), updated_at = now()
WHERE project_id = @project_id AND id = @id AND status = 'pending';

-- name: MarkDeliveryFailed :exec
UPDATE deliveries SET status = 'failed', error = @error::text, attempts = attempts + 1, updated_at = now()
WHERE project_id = @project_id AND id = @id AND status = 'pending';

-- name: RecordDeliveryRetry :exec
UPDATE deliveries SET error = @error::text, attempts = attempts + 1, updated_at = now()
WHERE project_id = @project_id AND id = @id AND status = 'pending';

-- name: FailPendingRun :exec
UPDATE deliveries SET status = 'failed', error = @error::text, updated_at = now()
WHERE project_id = @project_id AND message_id = @message_id AND run = @run AND status = 'pending';

-- name: BlockRun :exec
-- A run that the engine refused: pending rows become blocked, and a row with
-- an empty recipient records why.
UPDATE deliveries SET status = 'blocked', error = @error::text, updated_at = now()
WHERE project_id = @project_id AND message_id = @message_id AND run = @run AND status = 'pending';
