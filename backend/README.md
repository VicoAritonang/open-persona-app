# OpenPersona App: backend

The backend of the hosted OpenPersona platform: personas served to customers who hold an API key, on AWS Lambda and one EC2 instance, with packs, keys, jobs and usage in Postgres (Neon).

It is built on the open-source library [`go.vicoworks.com/open-persona`](https://github.com/VicoAritonang/open-persona), which provides the engines, the HTTP/gRPC/MCP server and the API contract. This module adds only what the hosted service needs:

| Path | |
|---|---|
| `registry/` | Postgres (Neon) store: pack versions, API keys, async jobs, usage events; a `server.Catalog` that pulls packs on demand |
| `cmd/openpersona-lambda/` | The API on AWS Lambda (HTTP, Connect RPC, and the async job worker) |
| `cmd/openpersona-cloud/` | The always-on server for EC2 (streaming, native gRPC), and the operator CLI: registry, push/pull, API keys |
| `deploy/aws/` | SAM template for Lambda; systemd unit, install script and Caddyfile for EC2 |
| `docs/` | [HOSTING.md](docs/HOSTING.md) (architecture, deployment), [REGISTRY.md](docs/REGISTRY.md) (schema, commands) |

The web app's "studio" API (training, evaluation, publishing from the browser) will live here too; see [../web/README.md](../web/README.md).

## Develop

```sh
cd backend
go test ./...
go run ./cmd/openpersona-cloud registry status     # needs NEON_DB_URL in .env (see .env.example)
```

The registry tests run against Postgres only when `OPENPERSONA_TEST_REGISTRY_URL` is set; otherwise they are skipped.

## The library dependency

`go.mod` requires a published version of the library, so CI and releases are reproducible. To work on both at once, use a Go workspace in the parent folder (see [../README.md](../README.md#development)); to move to a new library version:

```sh
go get go.vicoworks.com/open-persona@v0.2.0 && go mod tidy
```

The backend depends only on the library's public API and its stable wire contract (`api/`), never on its internals.

## Versioning

Tagged `backend/vX.Y.Z`. Changes are listed in [CHANGELOG.md](CHANGELOG.md).
