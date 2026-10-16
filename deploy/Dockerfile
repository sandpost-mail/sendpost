# syntax=docker/dockerfile:1
# Build from the repository root: docker build -f deploy/Dockerfile .
FROM node:22-alpine AS web
WORKDIR /web
COPY web/package.json web/package-lock.json ./
RUN npm ci
COPY web/ ./
RUN npm run build

FROM golang:1.26 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
# The embedded UI: replaces the placeholder in internal/webui/dist.
COPY --from=web /web/build/ internal/webui/dist/
RUN CGO_ENABLED=0 go build -trimpath -ldflags "-s -w" \
    -o /out/sandpost ./cmd/sandpost

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/sandpost /sandpost
USER nonroot:nonroot
EXPOSE 8080 1025
ENTRYPOINT ["/sandpost"]
CMD ["serve"]
