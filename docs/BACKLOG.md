# Sandpost Backlog (draft v0.1)

Derived from [PRD.md](./PRD.md) and [ARCHITECTURE.md](./ARCHITECTURE.md). Each item is one issue-sized unit. Size: S (under 1 day), M (1 to 3 days), L (3 to 5 days). Items follow the TDD rule: tests are written with or before the implementation and are part of the item.

## M0. Project setup

| ID | Item | Size |
|---|---|---|
| 0.1 | Secure the name: register the domain (`.dev` or fallback), create the `sandpost-mail` GitHub org and repo, trademark search | S |
| 0.2 | Repo bootstrap: Go module `github.com/sandpost-mail/sandpost`, MIT LICENSE, README stub, `.gitignore`, `CODE_OF_CONDUCT`, `CONTRIBUTING` | S |
| 0.3 | CI: GitHub Actions for lint (golangci-lint), `go test` with Postgres service, build, Svelte check | M |
| 0.4 | Config loader (env vars plus optional file), `slog` logging, `serve`/`migrate`/`admin` subcommand skeleton | S |
| 0.5 | Postgres setup: pgx pool, goose migrations, sqlc config, Testcontainers test helper | M |
| 0.6 | Dev environment: docker-compose (app, Postgres), Makefile, seed script | S |

## M1. Capture core

| ID | Item | Size |
|---|---|---|
| 1.1 | Schema: projects, smtp_credentials, messages, message_recipients, attachments, message_links, tags | M |
| 1.2 | Credential service: server-generated secrets, HMAC storage, revoke, constant-time verify | M |
| 1.3 | SMTP listener (go-smtp): AUTH PLAIN and LOGIN, TLS/STARTTLS config, reject unauthenticated `MAIL FROM`, per-IP auth failure rate limit, max message size | L |
| 1.4 | Ingest: enmime parse, transaction insert, link extraction, attachment metadata, fixtures for tricky mail (multipart, inline, charsets, huge headers) | L |
| 1.5 | Tag rules (header and recipient based) applied at ingest | S |
| 1.6 | Search: tsvector plus pg_trgm indexes, query parser for sender, recipient, subject, tag, date, body | L |
| 1.7 | `LISTEN/NOTIFY` event bus with per-project channels and SSE endpoint | M |
| 1.8 | Bootstrap admin on first boot (`ADMIN_EMAIL`/`ADMIN_PASSWORD`) | S |
| 1.9 | Svelte shell: SvelteKit SPA, go:embed, layout, project switcher placeholder | M |
| 1.10 | UI: message list (infinite scroll, live updates), message detail (HTML sandbox iframe, text, source, headers, attachments, links) | L |
| 1.11 | UI: project settings page for SMTP credentials with copyable env snippet | M |

Exit criteria: an app can send through authenticated SMTP and a developer sees the message live in the UI.

Status: 1.1 to 1.11 implemented (see ARCHITECTURE, M1 implementation notes). Interim: no authentication on the native API until M2.

## M2. Access control

| ID | Item | Size |
|---|---|---|
| 2.1 | Schema: users, groups, group_members, project_groups, sessions, api_tokens, audit_log | M |
| 2.2 | Authn: argon2id login, sessions, CSRF, forced password change, logout | M |
| 2.3 | `authz` package: role matrix, effective role resolution, project-scoped repository (no handler queries project data directly) | L |
| 2.4 | API tokens (user and project), HMAC storage, prefix for secret scanners, revoke | M |
| 2.5 | Native API `/_/api/v1`: users, groups, projects, credentials, tokens, audit | L |
| 2.6 | Audit log writes for admin actions and API endpoint | M |
| 2.7 | Cross-tenant leakage test suite (every endpoint with foreign project ids) and authz matrix tests | M |
| 2.8 | UI: login, admin pages (users, groups, projects, role assignment), token management, audit view | L |
| 2.9 | OpenAPI spec generated or validated in CI | M |

Exit criteria: two groups with different roles on two projects only see what they should, proven by tests.

Status: 2.1 to 2.9 implemented (see ARCHITECTURE, M2 implementation notes and M2 UI notes). The cross-tenant suite proves the exit criteria at the API level (`internal/api/access_test.go`).

## M3. Mailpit-compatible API

| ID | Item | Size |
|---|---|---|
| 3.1 | Pin the target Mailpit version and extract its OpenAPI as the contract | S |
| 3.2 | Token-scoped middleware on `/api/v1` (Bearer or Basic password) plus `/p/{slug}/api/v1` alias | M |
| 3.3 | Endpoints: list, search, get message, raw, html, text, attachments, headers, delete, delete-all, mark read, tags | L |
| 3.4 | Run Mailpit client libraries and its test examples against Sandpost, publish a compatibility matrix | L |
| 3.5 | Compatibility documentation: migration guide from per-project Mailpit | S |

Exit criteria: an existing test suite using Mailpit works by changing the base URL and token.

Status: 3.1, 3.2, 3.3 and 3.5 implemented, 3.4 partly. Pinned to Mailpit v1.31.6 with its swagger vendored in `docs/mailpit/`. The offline compatibility suite (`internal/api/compat_*_test.go`) replays Mailpit shaped requests, validates every JSON response against that swagger and runs cross tenant tests, and `docs/MAILPIT_COMPATIBILITY.md` is the matrix (kept in step with the route table by a test). Still open in 3.4: running real Mailpit client libraries and Mailpit's own examples against Sandpost (needs network and those toolchains), so the exit criterion is proven at the contract level only. Not implemented and answering 501: send, chaos, HTML/link/SpamAssassin checks, thumbnails. Release was added with M4. See ARCHITECTURE, M3 implementation notes.

## M4. Delivery policy

| ID | Item | Size |
|---|---|---|
| 4.1 | Schema: transports, delivery_policies, deliveries. Transport secret encryption (AES-GCM from `SECRET_KEY`) | M |
| 4.2 | Policy engine (pure functions): allow, redirect, capture, recipient splitting, header preservation, Bcc stripping. Table-driven tests including every fail-closed case | L |
| 4.3 | River setup and delivery jobs with bounded retries and per-recipient status | L |
| 4.4 | SMTP transport client (SES SMTP and generic), TLS modes, connection test endpoint | M |
| 4.5 | Manual release of a message to an allowlisted address | M |
| 4.6 | UI: transport and policy settings with explicit warnings, delivery status per message | L |
| 4.7 | End-to-end test with a fake relay proving no real recipient is contacted when misconfigured | M |

Exit criteria: the redirect and allow modes are covered by tests, and any misconfiguration results in no outbound mail.

Status: 4.1 to 4.7 implemented (see ARCHITECTURE, M4 implementation notes). The engine (`internal/policy`) lists every fail-closed case in a table driven test, `cmd/sandpost/e2e_delivery_test.go` runs SMTP, River and a fake relay end to end and shows that a trap relay receives no connection under each misconfiguration. The Mailpit release endpoint now works (matrix updated). River (MPL-2.0) is a new dependency, noted in ARCHITECTURE for the release notices. Not covered: a relay is reached from the server, so owners can point it at internal addresses (documented as an SSRF note), and key rotation has no online re-encryption.

## M5. CI helpers and collaboration

| ID | Item | Size |
|---|---|---|
| 5.1 | Long-poll `wait` endpoint (filters, timeout) built on the event bus | M |
| 5.2 | `extract` endpoint for links and one-time codes | M |
| 5.3 | Per-run isolation via `X-Run-Id` header or tag filter | S |
| 5.4 | CLI (`sandpost wait`, `sandpost extract`) and official GitHub Action | L |
| 5.5 | Comments on messages (API and UI) | M |
| 5.6 | Message permalink and project activity view | M |
| 5.7 | Example repos for CI usage (Playwright, pytest, Jest) | M |

## M6. Release readiness

| ID | Item | Size |
|---|---|---|
| 6.1 | Dockerfile (distroless, non-root), multi-arch image build and publish | M |
| 6.2 | Helm chart: Deployment, SMTP TCP Service, Ingress, optional Postgres, ServiceMonitor, values docs | L |
| 6.3 | Prometheus metrics, health endpoints, structured logging review | M |
| 6.4 | Documentation site: quickstart, concepts, migration from Mailpit, Kubernetes, security model | L |
| 6.5 | Load test (hundreds of messages per second, 100k messages per project listing) | M |
| 6.6 | Security review pass (auth, HTML preview, policy engine, secrets) and dependency/license audit including River (MPL-2.0) notices | M |
| 6.7 | E2E suite with Playwright against docker-compose | L |
| 6.8 | v0.1.0 release: changelog, tags, release notes | S |

## Cross-cutting rules
- Every PR keeps 80 percent or more coverage on touched packages.
- No handler queries project data without the scoped repository.
- No secret appears in logs. A log-scanning test covers the common paths.
- Docs change with the code that changes behavior.

## Suggested order and parallelism
M0 first. After M1.1 to M1.4, UI work (1.9 to 1.11) and the SMTP and ingest work can run in parallel. M2 can start once M1 schema is stable. M3 and M4 are independent of each other once M2 is done. M5 depends on M1.7 and M3. M6 starts alongside M4.

## Out of backlog (post v1)
Retention and quotas, OIDC/SSO, unauthenticated SMTP and CIDR routing, public share links, IMAP/POP3, per-environment redirect targets, language SDKs, WebSocket, blob store.
