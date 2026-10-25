# Sandpost regression test strategy

Scope: how we keep M1 to M4 from breaking while M5 and M6 land. Complements section 7 of [ARCHITECTURE.md](./ARCHITECTURE.md). Each item is marked **in place** (exists and runs in CI today) or **gap**.

Baseline measured on 2026-10-25 (`go test -race -cover ./...`, `-race -count=3` on the time sensitive packages): about 11.8k lines of Go tests and 0.9k of web tests, every Go package at 81% to 100% statement coverage (lowest: `store` 81.4%, `cmd/sandpost` 82.8%), no flaky failure in three repeated runs of `cmd/sandpost`, `delivery`, `smtpd` and `events`.

## 1. What must never regress

The strategy is organized around invariants, not test types. A change that touches one of these needs its guarding test to stay green and, when behavior changes on purpose, to change in the same PR.

| Invariant | Guarding tests | Tier | State |
|---|---|---|---|
| Misconfiguration never sends real mail (fail closed, allowlist, redirect, no Bcc leak) | `policy/engine_test.go` (table of every blocking case), `delivery/worker_test.go`, `cmd/sandpost/e2e_delivery_test.go` (trap relay gets no connection) | PR | in place |
| Queued mail re-evaluates policy on every attempt (switch to capture or narrow allowlist stops it) | `delivery/worker_test.go`, `river_test.go` | PR | in place |
| Tenant isolation: foreign project is 404, low role is 403, tokens never exceed owner | `api/access_test.go`, `compat_access_test.go`, `project_role_test.go`, `authz/*_test.go`; routes table drives matrix, leakage and OpenAPI checks | PR | in place |
| Native API matches `docs/openapi.yaml` | `api/openapi_test.go` plus redocly lint | PR | in place |
| Mailpit v1.31.6 contract: routes, shapes, auth, error text | `api/compat_contract_test.go`, `compat_schema_test.go` (responses validated against the vendored swagger), `compat_test.go`, `MAILPIT_COMPATIBILITY.md` kept in step by a test | PR | in place at contract level |
| Real Mailpit clients work against Sandpost (backlog 3.4) | none | Nightly | **gap** |
| Capture integrity: tricky MIME, charsets, NUL bytes, missing boundaries never lose or corrupt mail | `ingest/parse_test.go`, `ingest_test.go`, 8 fixtures in `ingest/testdata` | PR | in place, fixed corpus only |
| Parsers survive hostile input (ingest, search query, address and header parsing) | none, no `func Fuzz` anywhere | Nightly | **gap** |
| Secrets stay out of logs and audit detail | `delivery/worker_test.go` (`TestWorkerLogsHoldNoSecrets`), log scans in `e2e_test.go` and `e2e_delivery_test.go` | PR | in place |
| Credentials: constant time HMAC, rate limit (10 failures then 454), 552 over size | `credentials`, `smtpd`, `tokens` tests | PR | in place |
| Sessions, CSRF, forced password change | `api/auth_test.go`, `auth/auth_test.go`, `e2e_test.go` | PR | in place |
| HTML preview cannot run scripts or load remote content | header assertions in `api/preview_test.go` and `v1_test.go` (CSP contains sandbox, no `allow-scripts`) | PR | headers only, **gap** for real browser behavior |
| Search used for deletion selects exactly what it lists | `search/*_test.go`, `api/compat_test.go` | PR | in place |
| Concurrent ingest and tag pruning do not deadlock or lose tags | `messages/messages_test.go` and compat tests | PR | in place, no stress test |
| Upgrade from any released schema keeps data | `store_test.go` applies migrations to an empty database only | Pre-release | **gap** |
| The shipped image and compose stack boot and serve | CI builds the image, never runs it | Pre-release | **gap** |
| Web UI logic: access rules, delivery warnings, forms | 12 vitest files in `web/src` | PR | in place |
| User journeys in a browser | none (backlog 6.7) | Pre-release | **gap** |

## 2. Gaps found while measuring

1. **A skipped integration tier looks green.** `testutil.PostgresDSN` skips under `-short` or when Docker is unavailable. In CI `TEST_DATABASE_URL` is set, so a database failure fails the test rather than skipping, which is right. But nothing asserts that the integration tests ran, and a local `go test` without Docker silently skips most of the suite (`api`, `delivery`, `cmd/sandpost`, `store` all depend on it). Fix: in CI, run with `-v` and fail if any test reports `SKIP` other than the known one (`TestListScalesWithIndexes` under `-short`).
2. **Coverage is reported, not enforced.** CI prints the total with `tail -1`. The backlog rule is 80% on touched packages. Today all packages clear it, so a per package floor costs nothing now and stops erosion.
3. **No fuzzing.** Ingest, the search query parser, `policy/address.go` and `policy/headers.go` take untrusted input and have only example tests.
4. **Mailpit compatibility is contract level only.** The swagger replay proves shapes, not that client libraries behave. This is the open part of backlog 3.4 and of the M3 exit criterion.
5. **Migrations are tested fresh only.** Migration 00006 merges existing tag variants and 00007 adds delivery tables, both data sensitive, with no test that upgrades a populated database.
6. **No test shows the preview sandbox working in a browser.** Header checks prove intent, not enforcement.
7. **The built artifact is never exercised.** `docker build` runs, the container is never started.
8. **Not covered by design, record the decision:** SSRF through owner chosen relay hosts (documented in ARCHITECTURE M4 notes), online key rotation. Add a test when either gets a mitigation.

## 3. Tiers

### PR gate (every pull request, target under 10 minutes)

In place: lint, `go test -race -covermode=atomic ./...` against Postgres 17, build and image build, `sqlc diff`, OpenAPI test and lint, web `check`, `test`, `build`.

Proposed additions:
- Per package coverage floor of 80% (excluding `sqlcgen`, `testutil`, `scripts`), failing the job when a package drops below it.
- Fail the job when an integration test is skipped (gap 1).
- Keep the leakage, authz matrix, OpenAPI and compat tests unconditional: they iterate over `routes.go`, so a new route without a fixture or spec entry already fails CI. Do not add escape hatches.

### Nightly (scheduled workflow on `main`)

- Fuzz targets, 2 to 5 minutes each, corpus committed under `testdata/fuzz`, seeded from `internal/ingest/testdata/*.eml`: `ingest.Parse`, the Mailpit and native search query parsers, `policy` address and header handling. Oracle for policy: never panics, and never classifies an unparseable recipient as allowed.
- `go test -race -count=5` on `cmd/sandpost`, `delivery`, `smtpd`, `events` to surface timing flakes before they hit PRs.
- Real Mailpit clients and Mailpit's own API examples pointed at a running stack with a project token (closes 3.4). Start with the official Go client and one JS or Python client, publish the result in `MAILPIT_COMPATIBILITY.md`.
- `govulncheck ./...` and `npm audit --omit=dev`.

### Pre-release (tag or release branch)

- Upgrade test: apply migrations 00001 to N-1 with a seeded dataset (tags differing by case, messages with recipients and attachments, a delivery policy), then apply N and assert the data and API answers.
- Image smoke: start the built image with compose, wait on `/readyz`, send mail through authenticated SMTP, read it through the API and the Mailpit alias, then restart the container and read it again.
- Playwright against the compose stack (backlog 6.7), a handful of journeys only: bootstrap login with forced password change, create project and credential, live arrival of a message over SSE, role limited UI (viewer sees no settings), delivery settings confirmation dialog. Include one sandbox check that renders a message containing a script and a remote image and asserts neither executes nor loads, the only behavior API tests cannot prove.
- Load test (backlog 6.5): hundreds of messages per second, listing at 100k messages per project, tracked against the previous release.

## 4. Regression rules

- Every bug fix lands with a test that fails before the fix.
- A parsing or capture bug adds an `.eml` to `internal/ingest/testdata`, which also feeds the fuzz seed corpus.
- A policy or delivery bug adds a row to the `policy` engine table, plus an e2e case if mail could have left the system.
- A new route needs an entry in `routes.go`, a leakage fixture and an `openapi.yaml` path, or CI fails.
- A change to the Mailpit surface updates `MAILPIT_COMPATIBILITY.md`, which a test checks against the route table.
- A fuzz crash is committed as a corpus file with the fix.
- A flaky test is fixed or deleted within a week, never retried in CI.
- Tests assert behavior on the public surface (SMTP, HTTP, SQL results), not private helpers, so refactors stay cheap.

## 5. Suggested order

1. Skip detection and per package coverage floor, local first: `make test-gate` (`scripts/test-gate.sh`, `COVER_MIN` overrides the 80% floor, package arguments narrow the run). Done locally, CI wiring comes later.
2. Fuzz targets for ingest, search and policy parsers, plus the nightly workflow.
3. Migration upgrade test.
4. Image smoke test in CI.
5. Mailpit client run (3.4).
6. Playwright suite and load test (6.7, 6.5) before v0.1.0.
