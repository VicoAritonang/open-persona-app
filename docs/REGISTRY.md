# The persona registry (Postgres / Neon)

The platform stores every version of every pack in Postgres (Neon), so a pack can be distributed from a database as well as by copying the `.persona` file. The file format itself is described in the library's [docs/PACKS.md](../../docs/PACKS.md).

### Setup

```sh
# .env
NEON_DB_URL=postgresql://user:pass@ep-....-pooler.<region>.aws.neon.tech/neondb?sslmode=require&channel_binding=require

openpersona-cloud registry migrate     # creates schema "openpersona"; safe to re-run
openpersona-cloud registry status
```

The URL is read from `OPENPERSONA_REGISTRY_URL`, `NEON_DB_URL` or `DATABASE_URL` (first set wins). `OPENPERSONA_REGISTRY_SCHEMA` overrides the schema name. Pooled Neon endpoints (`-pooler` in the host) are supported: the client then avoids named prepared statements.

### Commands

```sh
openpersona-cloud push -pack fina.persona [-status draft] [-visibility private|unlisted|public] [-owner "..."] [-notes "..."]
openpersona-cloud pull -id fina [-version 1.0.0] [-out fina.persona]     # default: latest published
openpersona-cloud registry list
openpersona-cloud registry versions -id fina
openpersona-cloud registry set-status -id fina -version 1.0.0 -status deprecated|yanked|published|draft
```

- `push` of identical bytes is a no-op; different bytes under an existing version fail.
- `pull` verifies the SHA-256 and opens the pack before replacing the target file, and skips the download when the file on disk already has that hash.
- **Latest** is the highest published version by semver. Drafts and deprecated versions are never latest; yanked versions are refused on pull.

### Schema

All tables live in the `openpersona` schema. The DDL is in [`registry/migrations/001_init.sql`](../registry/migrations/001_init.sql) (packs) and [`002_cloud.sql`](../registry/migrations/002_cloud.sql) (API keys, jobs, usage); applied migrations are recorded in `schema_migrations`.

| Table | One row per | Key columns |
|---|---|---|
| `pack_blobs` | distinct pack file | `sha256` (PK), `size_bytes`, `data bytea` |
| `personas` | persona | `id` (PK), `name`, `role`, `description`, `language`, `owner`, `visibility`, `latest_version` |
| `persona_versions` | trained build | (`persona_id`, `version`) PK, `sha256` → `pack_blobs`, `status`, `format`, `spec jsonb`, `core`, `report jsonb`, `eval jsonb`, `embed_model`, `train_model`, `trained_at`, `published_at`, `published_by`, `notes` |
| `persona_units` | knowledge unit of a version | (`persona_id`, `version`, `unit_id`) PK, `kind`, `title`, `body`, `tags text[]`; full-text index |

`persona_units` is a copy for people, not for the runtime: review what a version knows, search it, or diff two versions in SQL.

```sql
-- Units added in 1.1.0 compared with 1.0.0
SELECT kind, title FROM openpersona.persona_units WHERE persona_id = 'fina' AND version = '1.1.0'
EXCEPT
SELECT kind, title FROM openpersona.persona_units WHERE persona_id = 'fina' AND version = '1.0.0';

-- Which versions mention a topic
SELECT version, title FROM openpersona.persona_units
WHERE persona_id = 'fina' AND to_tsvector('simple', title || ' ' || body) @@ plainto_tsquery('simple', 'kas kecil');
```

### As a library

```go
reg, err := registry.Open(ctx, registry.URLFromEnv(), registry.Options{})
defer reg.Close()
v, created, err := reg.Push(ctx, "fina.persona", registry.PushOptions{Notes: "..."})
v, downloaded, err := reg.Pull(ctx, "fina", "", "packs/fina.persona")
p, err := persona.Open("packs/fina.persona")
```

### Limits and next steps

- Pack bytes sit in `bytea`. That is fine for packs of a few MB to a few hundred MB (Fina 1.0.0 is 3.2 MB and Kora 1.0.0 is 14.4 MB, about two thirds of each is vectors). For much larger packs, move `pack_blobs.data` to object storage (S3/R2) and keep the hash and URL in the table.
- `visibility` and `owner` are recorded but not enforced: the registry trusts whoever holds the database URL. Distributing packs to third parties needs an HTTP endpoint with per-client keys in front of it, and pack signing so a client can verify the publisher.
- Memory (`memory.Engine`) still uses SQLite; only personas are in Postgres.
