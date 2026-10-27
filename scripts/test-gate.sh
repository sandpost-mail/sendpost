#!/bin/sh
# Local regression gate: runs the whole Go suite with the race detector and
# fails when a test was skipped (the integration tier did not run, for example
# no Docker or TEST_DATABASE_URL) or when a package is under the coverage floor.
#
#   scripts/test-gate.sh            # all packages
#   scripts/test-gate.sh ./internal/policy/...
#   COVER_MIN=85 scripts/test-gate.sh
#
# Packages without tests (sqlcgen, testutil, scripts) print no "ok" line and
# are not measured.
set -eu

cd "$(dirname "$0")/.."
COVER_MIN="${COVER_MIN:-80}"
out="$(mktemp)"
trap 'rm -f "$out"' EXIT

if [ "$#" -eq 0 ]; then set -- ./...; fi

status=0
go test -race -count=1 -v -cover "$@" >"$out" 2>&1 || status=$?

if [ "$status" -ne 0 ]; then
	grep -E '^(--- FAIL|FAIL|panic:)' "$out" | head -40 || true
	echo "gate: tests failed (exit $status), full output: re-run with 'go test -race -v $*'" >&2
	exit 1
fi

fail=0

skips="$(grep -E '^\s*--- SKIP:' "$out" || true)"
if [ -n "$skips" ]; then
	echo "gate: skipped tests, the integration tier did not fully run:" >&2
	echo "$skips" >&2
	echo "gate: start Docker or set TEST_DATABASE_URL (a Postgres where the user can create databases)" >&2
	fail=1
fi

low="$(awk -v min="$COVER_MIN" '
	/^ok[ \t]/ {
		for (i = 1; i <= NF; i++) if ($i == "coverage:") { pct = $(i + 1); sub(/%/, "", pct) }
		if (pct + 0 < min) printf "  %s %s%%\n", $2, pct
	}' "$out")"
if [ -n "$low" ]; then
	echo "gate: packages under ${COVER_MIN}% coverage:" >&2
	echo "$low" >&2
	fail=1
fi

if [ "$fail" -ne 0 ]; then exit 1; fi
echo "gate: ok (no skips, every measured package at ${COVER_MIN}% or more)"
