# Vendored Mailpit contract

`swagger-v1.31.6.json` is the OpenAPI 2.0 (Swagger) document of [Mailpit](https://github.com/axllent/mailpit) v1.31.6, the version Sandpost's compatible API targets. It is the contract the compatibility tests check response shapes against, so tests never need the network.

- Source: `server/ui/api/v1/swagger.json` at tag `v1.31.6` (published 2026-10-15, the latest stable release when this was pinned), fetched from `https://raw.githubusercontent.com/axllent/mailpit/v1.31.6/server/ui/api/v1/swagger.json`.
- Tag object: `b035f5287ed6fcffdbead7bb91ae158cba54d277`.
- SHA-256 of the file: `77cd5441b66fabfadaee3fafc84e63c927edc0eaed88590d82745c94b663f3ca`.
- Copied unmodified. Mailpit is MIT licensed, Copyright (c) 2022-Now() Ralph Slooten.

To move to a newer Mailpit: replace the file (and rename it), update the version in `docs/MAILPIT_COMPATIBILITY.md`, then run `go test ./internal/api/ -run Compat`. The contract test fails for every path the new swagger adds or drops until the matrix and the route table are updated.
