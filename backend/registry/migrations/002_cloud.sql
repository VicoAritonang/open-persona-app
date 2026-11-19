-- OpenPersona cloud: API keys for clients, async jobs, and usage records.
-- {{schema}} is replaced with the configured schema name.

-- A client's API key. Only the SHA-256 of the key is stored; the key itself
-- is shown once, when it is created. personas lists the persona ids the key
-- may use ('*' = all); scopes lists what it may do: knowledge (retrieve and
-- prompt, no LLM), chat (answers, streaming, async jobs, MCP ask), download
-- (fetch the pack file to run it locally).
CREATE TABLE {{schema}}.api_keys (
  id           text PRIMARY KEY,
  key_sha256   text NOT NULL UNIQUE CHECK (key_sha256 ~ '^[0-9a-f]{64}$'),
  prefix       text NOT NULL,
  name         text NOT NULL DEFAULT '',
  owner        text NOT NULL,
  personas     text[] NOT NULL DEFAULT '{*}',
  scopes       text[] NOT NULL DEFAULT '{knowledge,chat}',
  created_at   timestamptz NOT NULL DEFAULT now(),
  last_used_at timestamptz,
  revoked_at   timestamptz
);

-- An async job: one persona chat run outside the request that created it
-- (by a Lambda invoked asynchronously, or a worker goroutine).
CREATE TABLE {{schema}}.jobs (
  id          text PRIMARY KEY,
  key_id      text NOT NULL, -- an api_keys id, or "static" for a single-key server
  persona_id  text NOT NULL,
  kind        text NOT NULL DEFAULT 'chat',
  status      text NOT NULL DEFAULT 'queued' CHECK (status IN ('queued', 'running', 'succeeded', 'failed')),
  request     jsonb NOT NULL,
  result      jsonb,
  error       text,
  attempts    integer NOT NULL DEFAULT 0,
  created_at  timestamptz NOT NULL DEFAULT now(),
  started_at  timestamptz,
  finished_at timestamptz
);
CREATE INDEX jobs_key_created ON {{schema}}.jobs (key_id, created_at DESC);

-- One row per billable operation.
CREATE TABLE {{schema}}.usage_events (
  id                bigserial PRIMARY KEY,
  key_id            text NOT NULL,
  persona_id        text NOT NULL,
  op                text NOT NULL,
  prompt_tokens     bigint NOT NULL DEFAULT 0,
  completion_tokens bigint NOT NULL DEFAULT 0,
  calls             integer NOT NULL DEFAULT 0,
  duration_ms       integer NOT NULL DEFAULT 0,
  at                timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX usage_events_key_at ON {{schema}}.usage_events (key_id, at);
