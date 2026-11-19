-- OpenPersona registry: persona packs stored in Postgres (Neon).
--
-- A pack (.persona file) is the unit of distribution. The registry keeps
-- the file itself, content-addressed, plus enough metadata to list, choose
-- and audit versions without downloading them. {{schema}} is replaced with
-- the configured schema name (default "openpersona").

-- The pack files, keyed by the SHA-256 of their bytes. Two versions with
-- identical bytes share one row.
CREATE TABLE {{schema}}.pack_blobs (
  sha256     text PRIMARY KEY CHECK (sha256 ~ '^[0-9a-f]{64}$'),
  size_bytes bigint NOT NULL CHECK (size_bytes > 0),
  data       bytea NOT NULL,
  created_at timestamptz NOT NULL DEFAULT now()
);

-- One row per persona; descriptive columns follow its latest published version.
CREATE TABLE {{schema}}.personas (
  id             text PRIMARY KEY CHECK (id ~ '^[a-z0-9][a-z0-9-]{0,62}$'),
  name           text NOT NULL,
  role           text NOT NULL,
  description    text NOT NULL DEFAULT '',
  language       text NOT NULL DEFAULT '',
  owner          text NOT NULL DEFAULT '',
  visibility     text NOT NULL DEFAULT 'private' CHECK (visibility IN ('private', 'unlisted', 'public')),
  latest_version text,
  created_at     timestamptz NOT NULL DEFAULT now(),
  updated_at     timestamptz NOT NULL DEFAULT now()
);

-- Every trained build. id@version is immutable: same version, same bytes.
CREATE TABLE {{schema}}.persona_versions (
  persona_id   text NOT NULL REFERENCES {{schema}}.personas(id) ON DELETE CASCADE,
  version      text NOT NULL CHECK (version ~ '^[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.-]+)?$'),
  sha256       text NOT NULL REFERENCES {{schema}}.pack_blobs(sha256),
  size_bytes   bigint NOT NULL,
  format       integer NOT NULL,
  status       text NOT NULL DEFAULT 'published' CHECK (status IN ('draft', 'published', 'deprecated', 'yanked')),
  spec         jsonb NOT NULL,
  core         text NOT NULL,
  report       jsonb,
  eval         jsonb,
  embed_model  text NOT NULL DEFAULT '',
  train_model  text NOT NULL DEFAULT '',
  trained_at   timestamptz,
  published_at timestamptz NOT NULL DEFAULT now(),
  published_by text NOT NULL DEFAULT '',
  notes        text NOT NULL DEFAULT '',
  PRIMARY KEY (persona_id, version)
);
CREATE INDEX persona_versions_sha256 ON {{schema}}.persona_versions (sha256);

ALTER TABLE {{schema}}.personas
  ADD CONSTRAINT personas_latest_fk FOREIGN KEY (id, latest_version)
  REFERENCES {{schema}}.persona_versions (persona_id, version) DEFERRABLE INITIALLY DEFERRED;

-- The compiled knowledge units of each version, copied out of the pack so
-- they can be reviewed, searched and diffed between versions in SQL. The
-- pack stays the source of truth; runtime never reads this table.
CREATE TABLE {{schema}}.persona_units (
  persona_id text NOT NULL,
  version    text NOT NULL,
  unit_id    bigint NOT NULL,
  kind       text NOT NULL,
  title      text NOT NULL,
  body       text NOT NULL,
  tags       text[] NOT NULL DEFAULT '{}',
  PRIMARY KEY (persona_id, version, unit_id),
  FOREIGN KEY (persona_id, version) REFERENCES {{schema}}.persona_versions (persona_id, version) ON DELETE CASCADE
);
CREATE INDEX persona_units_search ON {{schema}}.persona_units
  USING gin (to_tsvector('simple', title || ' ' || body));
CREATE INDEX persona_units_title ON {{schema}}.persona_units (persona_id, lower(title));
