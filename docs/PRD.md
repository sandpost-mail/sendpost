# PRD: Sandpost

Status: draft v0.1
Name: Sandpost (pending domain and trademark checks)
License: MIT
Module path: github.com/sandpost-mail/sandpost

## 1. Problem

Mailpit and similar tools are designed to run one instance per project. In a team or company with many projects and environments this means many instances to deploy and secure, no shared access control, and no visibility across projects. Mailpit's maintainer has declined to add multi-tenancy ([axllent/mailpit#301](https://github.com/axllent/mailpit/issues/301)). Hosted tools with teams (Mailtrap, Mailosaur) are proprietary and not self-hostable. No open-source tool offers multiple projects, per-project SMTP credentials and role-based access in one service.

## 2. Goal

One self-hosted service that replaces per-project Mailpit instances. Any number of projects send mail to it, each isolated by its own SMTP credentials. People access projects through a web UI and API according to their group and role.

Success looks like:
- A team replaces N Mailpit instances with one deployment.
- Moving an app over takes only a change of SMTP host and credentials.
- Existing test code that uses the Mailpit API works with a changed base URL and token.

## 3. Personas

- **Instance admin**: deploys and operates the service, creates users, groups and projects.
- **Developer**: points an app at the sandbox, reads captured mail, debugs templates.
- **QA / project manager**: reviews emails in the UI, shares a message with a colleague, receives redirected mail.
- **CI pipeline**: sends mail, waits for a specific email, extracts a link or code, asserts, cleans up.

## 4. Scope for v1

### 4.1 Core capture
- SMTP server (with AUTH PLAIN and LOGIN, STARTTLS optional, TLS configurable).
- Authentication is mandatory. Credentials map each connection to exactly one project. No unauthenticated mode.
- Multiple credentials per project (per environment or pipeline), individually revocable.
- Parse at ingest: headers, text, HTML, attachments, inline images.
- Per-message views: HTML, text, source, headers, attachments, link list.
- Search and filter by sender, recipient, subject, body, tag, date.
- Tags (manual and by rule or header), delete, mark read.
- Live updates in the UI.

### 4.2 Projects, users, access
- Built-in users only. Admin creates users (no email invites) and sets an initial password with a forced change on first login.
- Groups. A group is assigned to a project with a role.
- Roles: admin (instance), project owner, member (read and write), viewer (read only).
- API tokens per user and per project for automation.
- Audit log for admin actions (user, group, credential, policy changes).

### 4.3 Delivery policy (optional, off by default)
Each project may configure one outbound SMTP transport (for example AWS SES). Per project, a policy decides what happens to each recipient after capture:
- **Capture only** (default).
- **Allow**: deliver for real to recipients matching an allowlist (addresses or domains).
- **Redirect**: all other recipients are replaced by one redirect address (a project manager or developer), with the original recipients preserved in headers.

Rules:
- A copy is always stored in the sandbox.
- The envelope is rewritten, not only the headers. Bcc is stripped. Mixed recipient lists are delivered as separate messages.
- Fail closed: a misconfigured or unreachable policy never sends to unlisted addresses.
- Transport credentials are encrypted at rest.
- Manual "release" of a captured message to an allowed address.

### 4.4 API
- Mailpit-compatible REST API (subset used by common test libraries: list, search, get, raw, html, attachments, delete, tags, release), served on the same `/api/v1` path and scoped to one project by a project API token (Bearer, or Basic auth password).
- Native API (under `/_/api/v1`) for projects, users, groups, credentials, policy.
- OpenAPI specification.

### 4.5 CI helpers (differentiator)
- Long-poll endpoint: wait until an email matching a filter arrives, with timeout.
- Per-run isolation using a header or tag (for example `X-Run-Id`).
- Extract links and one-time codes from a message.
- Official GitHub Action and a small CLI. Language SDKs later.

### 4.6 Collaboration (differentiator)
- Message permalink to share with a colleague (requires login and project access). Public links are out of scope for v1.
- Comments on a message.
- Project activity view.

### 4.7 Deployment
- Single Go binary with the Svelte UI embedded.
- Docker image and docker-compose example.
- Helm chart. SMTP exposed as a TCP service.
- Postgres as the only external dependency.

## 5. Out of scope for v1
- Retention rules and quotas (users clean the table themselves for now). An optional global max message size is allowed.
- OIDC / SSO and LDAP.
- Unauthenticated SMTP and source-IP routing.
- Email invites and password reset by email.
- Spam scoring, HTML compatibility checks (Mailpit has some), chaos testing.
- IMAP and POP3.
- Per-environment or per-rule redirect targets.
- Blob storage outside Postgres.

## 6. Non-functional requirements
- Stateless application, horizontally scalable, with Postgres as shared state. Live events through `LISTEN/NOTIFY`.
- Isolation: a project's data is only reachable with its credentials or a user holding a role on it. Enforced in the data access layer and tested.
- Secure defaults: argon2id for login passwords, server-generated SMTP secrets and API tokens stored as keyed HMAC, secrets never logged.
- Performance target: ingest hundreds of messages per second on a modest instance. Message list responsive with 100k messages per project.
- Observability: structured logs, Prometheus metrics, health endpoints.

## 7. Risks and open questions
- **Accidental real delivery** through the policy feature. Mitigation: off by default, fail closed, tests, clear UI warnings.
- **Mailpit API drift.** Pin a target version and publish a compatibility matrix.
- **Database growth without retention.** Document a cleanup recipe, add retention after v1.
- **SMTP credential storage.** SMTP AUTH PLAIN needs the secret verified on each connection. Secrets are generated by the server with high entropy, so a keyed HMAC is enough and users can not choose weak ones.
- **Server key loss or rotation.** The HMAC key and the transport encryption key are both derived from `SECRET_KEY`. Document rotation (credentials would have to be reissued).
- Open: final name checks (domains, trademark), copyright holder, whether SQLite is ever supported.

## 8. Milestones (proposal)
1. Capture core: SMTP, Postgres, parse, projects, credentials, basic UI list and view.
2. Access: users, groups, roles, tokens, audit log.
3. Mailpit-compatible API and search.
4. Delivery policy and release.
5. CI helpers, Action, collaboration features.
6. Helm chart, docs, first public release.
