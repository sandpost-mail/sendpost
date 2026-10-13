#!/usr/bin/env sh
# Seed the local development database (the compose "db" service).
# Idempotent. M0 only has instance_settings, richer fixtures arrive with the
# M1 schema.
set -eu

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
COMPOSE="docker compose -f $ROOT/deploy/docker-compose.yml"
[ -f "$ROOT/deploy/.env" ] && COMPOSE="$COMPOSE --env-file $ROOT/deploy/.env"

$COMPOSE exec -T db psql -v ON_ERROR_STOP=1 -U sandpost -d sandpost <<'SQL'
INSERT INTO instance_settings (key, value)
VALUES ('instance_name', 'Sandpost (dev)')
ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value, updated_at = now();
SQL
echo "seed complete"
