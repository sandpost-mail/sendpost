## What and why

<!-- The change and the reason for it. Link the issue: Fixes #123 -->

## How it was tested

<!-- Commands run, new or changed tests, manual checks. -->

## Checklist

- [ ] Title follows `<type>: <description>` (feat, fix, refactor, docs, test, chore, perf, ci)
- [ ] `make lint` and `make test` pass, new code has tests (80% coverage per package)
- [ ] Schema change: new goose migration, `make sqlc` run and generated code committed
- [ ] API change: `docs/openapi.yaml` updated (`make openapi`)
- [ ] README or docs updated when behaviour or configuration changes
