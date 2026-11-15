# syntax=docker/dockerfile:1
# Build from the repository root: docker build -f deploy/Dockerfile .
FROM node:22-alpine@sha256:0e1754758c2df4cfa3e68d59e2e1b7374bb7fd9ea6328eaaacf15cc934af8202 AS web
WORKDIR /web
COPY web/package.json web/package-lock.json ./
RUN npm ci
COPY web/ ./
RUN npm run build

# Keep the tag equal to the go directive in go.mod.
FROM golang:1.26.11@sha256:7af425da11f4fc1a9308eb704369e3f6d1c44e35bac0f874a59ade3f20fdc07e AS build
ARG VERSION=dev
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
# The embedded UI: replaces the placeholder in internal/webui/dist.
COPY --from=web /web/build/ internal/webui/dist/
RUN CGO_ENABLED=0 go build -trimpath -ldflags "-s -w -X main.version=${VERSION}" \
    -o /out/sandpost ./cmd/sandpost

FROM gcr.io/distroless/static-debian12:nonroot@sha256:afa5c872c891853ca7fcf1f12c3edb23f7eeef36189728842dd51042ff57f7ab
COPY --from=build /out/sandpost /sandpost
USER nonroot:nonroot
EXPOSE 8080 1025
ENTRYPOINT ["/sandpost"]
CMD ["serve"]
