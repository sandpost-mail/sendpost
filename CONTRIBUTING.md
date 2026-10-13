# Contributing

Thanks for helping. Work is tracked in [docs/BACKLOG.md](docs/BACKLOG.md), design lives in [docs/ARCHITECTURE.md](docs/ARCHITECTURE.md).

## Setup

```
make dev       # local Postgres and server
make test
make lint
make web-check
```

Tools: Go, Docker (integration tests use Testcontainers, or set `TEST_DATABASE_URL` to a Postgres where the user can create databases), golangci-lint v2, sqlc, Node 22+.

## Rules

- Tests first. New Go code needs tests with at least 80% coverage. Prefer table-driven tests.
- Keep files small and focused, handle every error explicitly, wrap errors with context.
- No hardcoded secrets. Configuration comes from the environment.
- Immutable style: return new values rather than mutating shared state.
- Database changes are new goose migrations in `internal/store/migrations`. Queries live in `internal/store/queries`, then run `make sqlc` and commit the generated code.
- Run `gofmt`, `make lint` and `make test` before opening a pull request.

## Commits and pull requests

Use `<type>: <description>` with type one of feat, fix, refactor, docs, test, chore, perf, ci. Keep pull requests focused, describe the change and how you tested it.

## License

By contributing you agree that your contributions are licensed under the MIT License.
