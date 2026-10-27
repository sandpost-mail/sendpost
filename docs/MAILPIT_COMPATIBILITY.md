# Mailpit compatibility

Sandpost serves a subset of the [Mailpit](https://github.com/axllent/mailpit) REST API so that test suites and tools written for a per-project Mailpit work after changing a base URL and a token. See [MAILPIT_MIGRATION.md](./MAILPIT_MIGRATION.md) for the steps.

- Target: **Mailpit v1.31.6** (tag `v1.31.6`, published 2026-10-15).
- Contract: [mailpit/swagger-v1.31.6.json](./mailpit/swagger-v1.31.6.json), the OpenAPI 2.0 document of that release, copied unmodified (see [mailpit/README.md](./mailpit/README.md) for the source, checksum and license). `internal/api/compat_contract_test.go` fails when a route is missing from, or not in, that contract, or when this page disagrees with the route table.
- This API is separate from the native API (`/_/api/v1`, [openapi.yaml](./openapi.yaml)). The native OpenAPI check does not cover it.

## Addressing and authentication

| Form | Who | Project |
|---|---|---|
| `https://host/api/v1/...` | project token (`spp_...`) | the project of the token |
| `https://host/p/{slug}/api/v1/...` | project token of that project, or a user token with a role on it | `{slug}` |

- The token goes in `Authorization: Bearer <token>`, or as the **password** of HTTP Basic auth with any username (what Mailpit clients already send). The session cookie is never read on these routes, so they need no CSRF header and a browser can not be made to use them.
- A user token (`spu_...`) on the bare `/api/v1` answers 403 (it names no project), use the `/p/{slug}` form.
- Permissions follow the native API. Reads (list, search, get, headers, raw, part, view, tags, info) and marking messages read need the `read` scope (viewer). Delete, tag changes and release need `write` (member). A project token holds at most member.
- A project the caller has no role on, a token of another project and an unknown slug all answer the same `404 page not found`. Message ids of another project behave like unknown ids: reads answer 404, writes by id succeed and change nothing.
- Without credentials: 401, with a `WWW-Authenticate: Basic` challenge when no `Authorization` header was sent at all. Wrong credentials: 401 without challenge.
- Errors are plain text like Mailpit (`404 page not found`, 400 with a message). Mutations answer `ok`. Unexpected failures are logged and answer a generic 500.
- Any other path under `/api/v1/` answers `404 page not found`, never the web UI.

## Endpoint matrix

Status: `implemented` (same behaviour apart from the notes), `partial` (works with the stated differences), `unsupported` (answers 501 once authenticated).

| Method | Path | Status | Notes |
|---|---|---|---|
| GET | `/api/v1/info` | partial | Counts, per tag counts and version describe the project mailbox. `Database`, `DatabaseSize`, `LatestVersion` and `RuntimeStats` are present but empty or zero. |
| GET | `/api/v1/webui` | unsupported | Configuration of Mailpit's own UI. |
| GET | `/api/v1/chaos` | unsupported | Chaos testing is not offered. |
| PUT | `/api/v1/chaos` | unsupported | Chaos testing is not offered. |
| POST | `/api/v1/send` | unsupported | Mail enters through authenticated SMTP only. |
| GET | `/api/v1/messages` | partial | `before=` is ignored. `limit=0` returns everything up to 1000 messages, larger limits are capped at 1000. `Username` is always empty. |
| PUT | `/api/v1/messages` | implemented | By `IDs`, by `Search`, or all messages. A `Search` without any usable term, or with an invalid date, answers 400 instead of widening to everything. |
| DELETE | `/api/v1/messages` | implemented | Deletes the given `IDs`, or every message of the project when there is none (empty body or empty list), as Mailpit does. A body that is not valid JSON answers 400, Mailpit deletes everything in that case. |
| GET | `/api/v1/search` | partial | Common subset of the syntax, see below. |
| DELETE | `/api/v1/search` | implemented | Same syntax as search. A query without any usable term answers 400. |
| GET | `/api/v1/message/{ID}` | partial | Marks the message read, like Mailpit. `Username` is always empty. `To` and `Cc` carry display names when the header parses, `Bcc` is the envelope recipients that are in neither. `Date` is the Date header, else the time of receipt. |
| GET | `/api/v1/message/{ID}/headers` | implemented | Headers as parsed at capture, keys in canonical form. |
| GET | `/api/v1/message/{ID}/raw` | implemented | `?dl=1` sends it as a download. |
| GET | `/api/v1/message/{ID}/part/{PartID}` | implemented | `PartID` is the attachment ordinal (attachments first, then inline parts). Types a browser would execute (HTML, XML, SVG) are served as downloads. |
| GET | `/api/v1/message/{ID}/part/{PartID}/thumb` | unsupported | Needs image decoding of untrusted data, left out for now. |
| GET | `/api/v1/message/{ID}/html-check` | unsupported | HTML compatibility check is not offered. |
| GET | `/api/v1/message/{ID}/link-check` | unsupported | Link checking would make the server fetch URLs from captured mail. |
| GET | `/api/v1/message/{ID}/sa-check` | unsupported | SpamAssassin is not integrated. |
| POST | `/api/v1/message/{ID}/release` | partial | Body `{"To": [...]}`. Sends through the project's transport (set up in the UI, see the delivery policy), and only to addresses on the project allowlist. Answers `ok` once the release is queued (a job sends it, the delivery status is in the UI and `GET /_/api/v1/projects/{pid}/messages/{mid}/deliveries`). 400 with the reason when a target is not allowlisted, the transport is missing or disabled, or `To` is empty. Needs `write` (member). `latest` works. Mailpit relays to any address through its single global relay, Sandpost refuses everything off the allowlist. |
| GET | `/api/v1/tags` | implemented | Tags in use in the project, sorted. |
| PUT | `/api/v1/tags` | implemented | Replaces the tags of the given messages. Invalid names are dropped, case duplicates collapse (the first spelling wins), as in Mailpit. Tag names are case insensitive. |
| PUT | `/api/v1/tags/{Tag}` | implemented | Rename, also a change of case only. The tag is found without regard to case. 404 when the tag does not exist (even when the new name is the same), 400 when the new name is invalid or used by another tag. Mailpit answers 400 for a missing tag. |
| DELETE | `/api/v1/tags/{Tag}` | implemented | Removes the tag from its messages. 404 when it does not exist (Mailpit: 400). |
| GET | `/view/{ID}.html` | partial | Served at `/view/...` and `/p/{slug}/view/...`, outside `/api/v1`. `embed=1` is ignored. Inline images point at the part endpoint of this API, which needs the token, so a browser only shows them when it sends credentials. Carries a sandbox CSP. Marks the message read. |
| GET | `/view/{ID}.txt` | implemented | Marks the message read. |

Everywhere a message id is expected, `latest` is the newest message of the project, optionally narrowed with `?query=` (same syntax as search).

## Data shapes

The JSON keys and types are the ones of the Mailpit swagger, and the test suite validates responses against it: PascalCase message objects, lower case list envelope (`total`, `unread`, `count`, `messages_count`, `messages_unread`, `start`, `tags`, `messages`), lists that are always arrays.

Differences in the values:

- **IDs** are decimal strings (`"1042"`), not Mailpit's 22 character ids. Code must treat them as opaque strings. Ids are unique across projects but only valid inside one.
- **PartID** is an ordinal (`"0"`, `"1"`), not a MIME part path.
- **Display names**: `From`, `To`, `Cc` and `Reply-To` carry names parsed from the headers. `Bcc` addresses carry no name (they are not in the headers).
- **Snippet** is the first 250 characters of the text body, or of the HTML with markup removed when there is no text.
- `Attachments` in a summary counts attachments, not inline parts. In a message, parts without a file name and without a content id are not listed, as in Mailpit.
- Checksums (`MD5`, `SHA1`, `SHA256`) are computed on each message fetch (a re-parse of the stored message), not stored.

## Search syntax

A search can have at most 20 terms, more answers 400. Terms are separated by spaces, double quotes group a phrase, `-` or `!` in front negates a term, every term must match. Matching is case insensitive substring unless noted. Dates without a zone are read in UTC, or in the zone of the `tz` parameter.

| Term | Supported | Notes |
|---|---|---|
| `from:` `to:` `cc:` `bcc:` `reply-to:` `addressed:` | yes | Matched against the address (and, for `from:`, the display name). `to:` and `cc:` use the header addresses, `bcc:` the envelope recipients missing from To and Cc, `addressed:` any of From, To, Cc, Bcc and Reply-To. Display names of recipients are not searched. |
| `subject:` | yes | |
| `message-id:` | yes | |
| `tag:` | yes | Exact tag name, case insensitive. |
| `is:read` `is:unread` `is:tagged` | yes | |
| `has:attachment(s)` `has:inline(s)` | yes | |
| `after:` `before:` | yes | Both inclusive, as in Mailpit. Formats: `2026-10-14`, `2026-10-14 13:30`, `2026-10-14 13:30:05`, RFC 3339. Mailpit accepts many more formats, and ignores an invalid date. Sandpost answers 400 for an invalid date, because dropping the term would widen a delete. |
| `larger:` `smaller:` | yes | Sizes such as `500`, `2kb`, `1.5m`, `10mb`. A size that does not fit in a signed 64 bit byte count (after the kb or mb multiplication) answers 400. |
| plain words | partial | Substring of the subject, the text body (generated from the HTML for HTML only mail), the sender and the recipient addresses. Mailpit's search text is From, Subject, To, Cc, Bcc, Reply-To and Return-Path, the HTML body stripped of markup (or the text body when there is no HTML) and the attachment file names. So in Sandpost attachment names, Reply-To and Return-Path are not searched by plain words (use `reply-to:`). |
| anything else with a colon | as text | Treated as a plain word, like Mailpit. |

Not part of this syntax: the native search keys `body:` and `date:`, they are only for `/_/api/v1`.

## Other deviations

- Mailpit's websocket (`/api/events`), its SMTP relay, the UI's own Basic auth, CORS configuration and tag filters (`--tags`) are not provided. Sandpost has its own tag rules (project settings), which also cover an `X-Tags` style header.
- Responses to deletions do not push events to the Sandpost UI stream yet, the UI shows them on the next load.
- Compat requests are not written to the audit log (they are not administrative actions).

## Verification status

- Done offline: `internal/api/compat_*_test.go` replays Mailpit shaped requests (Basic and Bearer auth, query parameters, JSON bodies) and validates every JSON response against the vendored swagger. Cross tenant tests try every route with the ids and slug of another project.
- **Not done: running real Mailpit client libraries and Mailpit's own example tests against Sandpost.** It needs the network and those projects' toolchains, so it is a follow-up (backlog item 3.4). Until then the exit criterion "an existing test suite using Mailpit works by changing the base URL and token" is only proven at the level of the contract, not with third party code. Candidates: the Mailpit API examples in its docs, and the Playwright and Node helper packages that wrap its API.
