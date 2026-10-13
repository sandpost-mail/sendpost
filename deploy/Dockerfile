# syntax=docker/dockerfile:1
# Build from the repository root: docker build -f deploy/Dockerfile .
FROM golang:1.26 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags "-s -w" \
    -o /out/sandpost ./cmd/sandpost

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/sandpost /sandpost
USER nonroot:nonroot
EXPOSE 8080 1025
ENTRYPOINT ["/sandpost"]
CMD ["serve"]
