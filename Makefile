SHELL := /bin/sh
BIN := bin/sandpost
COMPOSE := docker compose -f deploy/docker-compose.yml --env-file deploy/.env

.PHONY: help build test lint migrate dev sqlc seed web web-dev web-check docker-build clean

help:
	@echo "targets: build test lint migrate dev sqlc seed web web-dev web-check docker-build clean"

# Build the UI and embed it (internal/webui/dist) in the binary.
build: web
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

web/node_modules: web/package-lock.json
	cd web && npm ci
	@touch web/node_modules

# Build the SvelteKit app and copy it where go:embed picks it up.
web: web/node_modules
	cd web && npm run build
	find internal/webui/dist -mindepth 1 ! -name .gitkeep -delete
	cp -R web/build/. internal/webui/dist/

# UI with hot reload on :5173, proxying the API of `make dev` on :8080.
web-dev: web/node_modules
	cd web && npm run dev

web-check:
	cd web && npm ci && npm run check

deploy/.env:
	@printf 'SECRET_KEY=%s\nPOSTGRES_PASSWORD=sandpost\n' "$$(openssl rand -hex 32)" > $@
	@echo "created $@ with a random SECRET_KEY"

# Start Postgres in Docker, migrate, and run the server locally.
dev: deploy/.env web
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
	find internal/webui/dist -mindepth 1 ! -name .gitkeep -delete
