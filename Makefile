SHELL := /bin/sh
BIN := bin/sandpost
COMPOSE := docker compose -f deploy/docker-compose.yml --env-file deploy/.env

.PHONY: help build test lint migrate dev sqlc seed web-check docker-build clean

help:
	@echo "targets: build test lint migrate dev sqlc seed web-check docker-build clean"

build:
	CGO_ENABLED=0 go build -trimpath -ldflags "-s -w" -o $(BIN) ./cmd/sandpost

test:
	go test -race -cover ./...

lint:
	golangci-lint run ./...

# Apply migrations to $$DATABASE_URL (see deploy/.env.example, or run `make dev`).
migrate:
	go run ./cmd/sandpost migrate up

sqlc:
	sqlc generate

web-check:
	cd web && npm ci && npm run check

deploy/.env:
	@printf 'SECRET_KEY=%s\nPOSTGRES_PASSWORD=sandpost\n' "$$(openssl rand -hex 32)" > $@
	@echo "created $@ with a random SECRET_KEY"

# Start Postgres in Docker, migrate, and run the server locally.
dev: deploy/.env
	$(COMPOSE) up -d --wait db
	set -a; . deploy/.env; set +a; \
	export DATABASE_URL="postgres://sandpost:$$POSTGRES_PASSWORD@127.0.0.1:$${POSTGRES_PORT:-5432}/sandpost?sslmode=disable"; \
	go run ./cmd/sandpost migrate up && go run ./cmd/sandpost serve

seed:
	./scripts/seed.sh

docker-build:
	docker build -f deploy/Dockerfile -t sandpost:dev .

clean:
	rm -rf bin web/build web/.svelte-kit
