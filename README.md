# Sandpost

Self-hosted email capture and safe delivery for development, staging and CI. One Go binary, a Svelte UI, Postgres. See [docs/PRD.md](docs/PRD.md), [docs/ARCHITECTURE.md](docs/ARCHITECTURE.md) and [docs/BACKLOG.md](docs/BACKLOG.md).

Status: early development (milestone M0, project setup). Nothing is usable yet beyond the health endpoints.

## Quick start

Requirements: Go (see `go.mod`), Docker, Node 22 or later for the web app.

```
make dev        # Postgres in Docker, migrations, server on :8080
make test       # unit and integration tests (integration tests need Docker)
make lint
```

Run the full stack in containers:

```
cp deploy/.env.example deploy/.env   # then set SECRET_KEY (openssl rand -hex 32)
docker compose -f deploy/docker-compose.yml --env-file deploy/.env up --build
```

## Configuration

Environment variables, optionally preloaded from a `KEY=VALUE` file named by `SANDPOST_CONFIG_FILE` (environment wins over the file).

| Variable | Required | Default | Notes |
|---|---|---|---|
| `DATABASE_URL` | yes | | Postgres connection string |
| `SECRET_KEY` | yes | | at least 32 bytes |
| `SMTP_LISTEN` | no | `:1025` | |
| `HTTP_LISTEN` | no | `:8080` | |
| `TLS_CERT_FILE`, `TLS_KEY_FILE` | no | | set both or neither |
| `ADMIN_EMAIL`, `ADMIN_PASSWORD` | no | | first boot bootstrap, set both or neither |
| `MAX_MESSAGE_BYTES` | no | 26214400 | |
| `LOG_LEVEL`, `LOG_FORMAT` | no | `info`, `text` | format is `text` or `json` |

## Commands

`sandpost serve`, `sandpost migrate [up|down|status]`, `sandpost admin`.

## License

MIT, see [LICENSE](LICENSE).
