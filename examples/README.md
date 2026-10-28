# CI examples

Three small, self-contained projects that test an email flow ("sign up, get a code or a link by mail") against a Sandpost project, using the `sandpost` CLI and the GitHub Action from the repository root.

| Directory | Runner | What it shows |
|---|---|---|
| [jest](jest) | Jest | an API level test: mail in over SMTP, `sandpost extract` for the code |
| [pytest](pytest) | pytest | the same in Python, with a fixture that gives every test its own run id |
| [playwright](playwright) | Playwright | a browser test: fill a sign up form, read the code from the mail, finish the flow |

How they work, in all three:

1. The application under test sends its mail through Sandpost's SMTP port with the project's SMTP credential, and adds the header `X-Run-Id: <id>` to every message (here a stand-in sender plays the application, replace it with your own).
2. The test calls `sandpost extract` (or `sandpost wait`) with the same run id. Only mail of that run matches, so parallel jobs and re-runs of one project do not see each other's messages. Mail sent before the call counts.
3. `sandpost` exits 3 on a timeout and 4 when the message has no such code or link, so a missing mail fails the test with a clear message instead of a hang.

Configuration (all three read the same variables):

| Variable | Meaning |
|---|---|
| `SANDPOST_URL` | address of the Sandpost server, for example `https://sandpost.example.com` |
| `SANDPOST_PROJECT` | numeric project id |
| `SANDPOST_TOKEN` | project API token, scope `read` (Settings, API tokens). Keep it in a secret |
| `SANDPOST_SMTP_HOST`, `SANDPOST_SMTP_PORT` | SMTP address (port 1025 by default) |
| `SANDPOST_SMTP_USER`, `SANDPOST_SMTP_PASSWORD` | the project's SMTP credential (Settings, Create credential) |
| `SANDPOST_RUN_ID` | the run id. In GitHub Actions use `${{ github.run_id }}-${{ github.run_attempt }}`: `GITHUB_RUN_ID` alone is the same for a re-run and would match the first attempt's mail |

Each directory has a `ci.example.yml`: copy it to `.github/workflows/` of your own repository (it is kept out of this repository's workflows on purpose). The workflows expect a reachable Sandpost server and build the CLI with the action. A published container image for Sandpost itself is not available yet (backlog 6.1).

Status: the CLI and the action script are tested in this repository. The example projects themselves were syntax checked, not run against a live server in CI, see the notes in each README.
