# OpenPersona App

The hosted OpenPersona platform: a web app where people train and build persona knowledge graphs, and the backend that trains them, stores them, and serves them to customers' products through API keys.

It is built on the open-source library [OpenPersona](https://github.com/VicoAritonang/open-persona) (`go.vicoworks.com/open-persona`): the training and retrieval engines, the `.persona` pack format, and the HTTP/gRPC/MCP API with its versioned contract all come from there. This repository holds only what the hosted service adds.

```
app/
├── web/        the web app (frontend): build, train, evaluate and publish personas
└── backend/    the backend (Go): registry, API keys, jobs, usage; runs on AWS Lambda + EC2 with Neon Postgres
```

| Part | State | Docs |
|---|---|---|
| [`backend/`](backend) | Running: serves personas (knowledge, chat, streaming, async jobs, pack download, MCP, gRPC), the Neon registry, API keys, usage | [backend/README.md](backend/README.md), [HOSTING](backend/docs/HOSTING.md), [REGISTRY](backend/docs/REGISTRY.md) |
| [`web/`](web) | Planned | [web/README.md](web/README.md) |

## How the parts fit

```
 browser ──► web/ ──► backend/ "studio" API (planned): projects, sources, training jobs, eval, publish
                         │
                         ├── trains with the library's persona.Trainer, writes a .persona pack
                         ├── stores every version in the registry (Neon)
                         ▼
 customers' products ──► backend/ serving API (running): HTTP, gRPC, MCP, with API keys
```

The serving API is the library's contract (`/v1`, `openpersona.v1`), so customers can also run a downloaded pack locally with the library, unchanged.

## Development

Each part builds on its own:

```sh
cd backend && go test ./...
```

To change the library and the backend together, check both out side by side and add a Go workspace in the parent folder (not committed to either repository):

```
workspace/
├── go.work        # go work init ./library ./app/backend
├── library/       # github.com/VicoAritonang/open-persona
└── app/           # this repository
```

With `go.work` present, the backend builds against the local library; CI and releases build against the version in `backend/go.mod`. Before releasing the backend, release the library and `go get go.vicoworks.com/open-persona@<version>` in `backend/`.

## Versioning

The parts are versioned independently: `backend/vX.Y.Z` tags (Go's convention for a module in a subdirectory) and the web app's own version once it exists. Each part keeps its own CHANGELOG.md.
