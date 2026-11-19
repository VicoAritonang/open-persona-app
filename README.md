# OpenPersona Platform

The hosted OpenPersona service: personas served to customers who hold an API key, on AWS Lambda and one EC2 instance, with packs, keys, jobs and usage in Postgres (Neon).

It is built on the open-source library [`go.vicoworks.com/open-persona`](../README.md), which provides the engines, the HTTP/gRPC/MCP server and the API contract. This module adds only what the hosted service needs:

| Path | |
|---|---|
| `registry/` | Postgres (Neon) store: pack versions, API keys, async jobs, usage events; a `server.Catalog` that pulls packs on demand |
| `cmd/openpersona-lambda/` | The API on AWS Lambda (HTTP, Connect RPC, and the async job worker) |
| `cmd/openpersona-cloud/` | The always-on server for EC2 (streaming, native gRPC), and the operator CLI: registry, push/pull, API keys |
| `deploy/aws/` | SAM template for Lambda; systemd unit, install script and Caddyfile for EC2 |
| `docs/` | [HOSTING.md](docs/HOSTING.md) (architecture, deployment), [REGISTRY.md](docs/REGISTRY.md) (schema, commands) |

## Develop

```sh
cd platform
go test ./...
go run ./cmd/openpersona-cloud registry status     # needs NEON_DB_URL in .env (see .env.example)
```

The registry tests run against Postgres only when `OPENPERSONA_TEST_REGISTRY_URL` is set; otherwise they are skipped.

## Relation to the library

Until this directory moves to its own repository, `go.mod` builds against the library in the parent directory (`replace go.vicoworks.com/open-persona => ../`), so a change to the library and the platform can land in one commit. To split it out:

1. Copy `platform/` into the new repository (with `git subtree split --prefix=platform` to keep its history).
2. Delete the `replace` line and `go get go.vicoworks.com/open-persona@<released version>`.
3. Move `.github/workflows/platform.yml` there and drop its `platform/` path prefixes.

The platform depends only on the library's public API and its stable wire contract (`api/`), never on its internals.

## Versioning

Tagged `platform/vX.Y.Z` while it lives here (Go's convention for a module in a subdirectory), `vX.Y.Z` after the split. Changes are listed in [CHANGELOG.md](CHANGELOG.md).
