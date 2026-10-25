# Moving from a per-project Mailpit to Sandpost

If your tests or tools talk to a Mailpit REST API, you change two things: the base URL and the credential. The SMTP side changes too, since Sandpost needs authentication. Details of what is and is not supported are in [MAILPIT_COMPATIBILITY.md](./MAILPIT_COMPATIBILITY.md) (Mailpit v1.31.6 API).

## 1. Create a project token

In the project settings (Tokens) create a **project token**:

- scope `read` for suites that only read mail (list, search, get, attachments). Marking messages read also works.
- scope `write` if they delete messages or change tags (the usual `DELETE /api/v1/messages` between tests needs it).

The token (`spp_...`) is shown once. A project token belongs to one project, so use one per project, not per test run.

## 2. Change the base URL and the credential

| | Mailpit | Sandpost |
|---|---|---|
| API base URL | `http://mailpit:8025` | `https://sandpost.example.com` (the `/api/v1/...` paths stay as they are) |
| Credential | none, or Basic auth of the instance | the project token, as Bearer, or as the Basic password |
| Alternative base URL | | `https://sandpost.example.com/p/{project-slug}` with a project token or a user token that has a role on the project |

Mailpit clients that already use Basic auth keep working: put the token in the password field, the username is ignored.

```sh
# before
curl http://mailpit:8025/api/v1/messages
# after
curl -u x:$SANDPOST_TOKEN https://sandpost.example.com/api/v1/messages
# or
curl -H "Authorization: Bearer $SANDPOST_TOKEN" https://sandpost.example.com/api/v1/messages
```

Node, Python and similar:

```ts
// before: fetch(`${process.env.MAILPIT_URL}/api/v1/messages`)
const res = await fetch(`${process.env.SANDPOST_URL}/api/v1/messages`, {
  headers: { Authorization: `Bearer ${process.env.SANDPOST_TOKEN}` },
});
```

## 3. Point your application's SMTP at Sandpost

Create an SMTP credential in the project settings and send with it (the settings page shows a ready made snippet). Mailpit usually accepts anything without authentication, Sandpost requires AUTH (PLAIN or LOGIN) and, depending on the instance, TLS or STARTTLS.

## 4. Check the differences that can break a suite

- **Message ids are decimal strings** (`"1042"`). Do not parse or validate them as Mailpit ids, and do not hardcode them across runs.
- **Clearing the mailbox between tests**: `DELETE /api/v1/messages` empties the whole project, which is shared with everyone who uses it. Give parallel suites their own project, or use tags (`PUT /api/v1/tags`) and `DELETE /api/v1/search?query=tag:run-42` to clean only your run.
- `GET /api/v1/message/{id}` marks the message read, as in Mailpit.
- Search is a subset: addresses, subject, tags, `is:`, `has:`, dates and sizes work, see the syntax table. Display names of recipients are not searched.
- A search with an invalid date answers 400 (Mailpit ignores the term).
- Not available (answers 501): HTTP send API, chaos, HTML and link checks, SpamAssassin, attachment thumbnails.
- Release works differently: it needs a transport and a delivery policy on the project (project settings, owner role), and only sends to addresses on the allowlist.
- `/view/{id}.html` and `.txt` are served at `/view/...` of the Sandpost host (or `/p/{slug}/view/...`) and need the same credentials.
- No unauthenticated access: a missing or wrong token answers 401.

## 5. Verify

Run your suite. A quick manual check:

```sh
curl -u x:$SANDPOST_TOKEN "https://sandpost.example.com/api/v1/search?query=is:unread&limit=5"
```

If you see 404 where a project should exist, check the token belongs to that project (or the slug in `/p/{slug}`). Sandpost answers 404 for a project the token can not see, on purpose.
