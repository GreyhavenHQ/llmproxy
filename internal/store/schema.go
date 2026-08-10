package store

// Schema is portable across SQLite and Postgres: TEXT ids and timestamps,
// INTEGER 0/1 booleans, DOUBLE PRECISION floats.
//
// Deliberate property: no table has a column that can hold request or
// response content. Usage is recorded as (unit, quantity) pairs only; a test
// rejects content-shaped column names in this DDL.
const Schema = `
CREATE TABLE IF NOT EXISTS principal (
    id TEXT PRIMARY KEY,
    name TEXT NOT NULL UNIQUE,
    kind TEXT NOT NULL DEFAULT 'user',
    role TEXT NOT NULL DEFAULT 'member',
    external_sub TEXT,
    email TEXT,
    owning_group TEXT,
    created_at TEXT NOT NULL,
    sessions_revoked_before TEXT
);
-- A browser session is a server-side row. The cookie holds an opaque token
-- stored here only as a keyed hash. Logout and revocation delete rows, same
-- philosophy as api_key: delete, don't flag. (No semicolons in comments:
-- Init splits this DDL on them.)
CREATE TABLE IF NOT EXISTS session (
    id TEXT PRIMARY KEY,
    principal_id TEXT NOT NULL REFERENCES principal(id),
    token_hash TEXT NOT NULL UNIQUE,
    created_at TEXT NOT NULL,
    expires_at TEXT NOT NULL,
    last_used_at TEXT
);
CREATE INDEX IF NOT EXISTS idx_session_principal ON session(principal_id);
CREATE TABLE IF NOT EXISTS api_key (
    id TEXT PRIMARY KEY,
    principal_id TEXT NOT NULL REFERENCES principal(id),
    key_hash TEXT NOT NULL UNIQUE,
    key_suffix TEXT NOT NULL DEFAULT '',
    label TEXT NOT NULL DEFAULT '',
    created_at TEXT NOT NULL,
    last_used_at TEXT
);
CREATE INDEX IF NOT EXISTS idx_api_key_principal ON api_key(principal_id);
-- A relay token identifies a principal on the transparent relay only. It is
-- deliberately not an api_key row: it grants no access to the proxy's own
-- API surface, and API keys are not accepted on the relay.
CREATE TABLE IF NOT EXISTS relay_token (
    id TEXT PRIMARY KEY,
    principal_id TEXT NOT NULL REFERENCES principal(id),
    token_hash TEXT NOT NULL UNIQUE,
    token_suffix TEXT NOT NULL DEFAULT '',
    label TEXT NOT NULL DEFAULT '',
    created_at TEXT NOT NULL,
    last_used_at TEXT
);
CREATE INDEX IF NOT EXISTS idx_relay_token_principal ON relay_token(principal_id);
CREATE TABLE IF NOT EXISTS provider (
    id TEXT PRIMARY KEY,
    name TEXT NOT NULL UNIQUE,
    wire_format TEXT NOT NULL DEFAULT 'openai',
    base_url TEXT NOT NULL,
    credential_ciphertext TEXT,
    verify_tls INTEGER NOT NULL DEFAULT 1,
    ca_pem TEXT,
    timeout_connect DOUBLE PRECISION NOT NULL DEFAULT 10,
    timeout_read DOUBLE PRECISION NOT NULL DEFAULT 300,
    timeout_write DOUBLE PRECISION NOT NULL DEFAULT 30,
    max_concurrency INTEGER,
    enabled INTEGER NOT NULL DEFAULT 1,
    created_at TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS provider_endpoint (
    id TEXT PRIMARY KEY,
    provider_id TEXT NOT NULL REFERENCES provider(id),
    endpoint TEXT NOT NULL,
    url_override TEXT,
    enabled INTEGER NOT NULL DEFAULT 1,
    UNIQUE (provider_id, endpoint)
);
-- A binding is either direct (provider_id + upstream_name) or an alias for
-- another binding (target_id). One hop only: a target must itself be direct,
-- which is enforced at write time. An alias row inherits its target's
-- provider, upstream name and capabilities, so its own upstream_name and
-- capability_set are empty and provider_id only satisfies the reference.
CREATE TABLE IF NOT EXISTS model_binding (
    id TEXT PRIMARY KEY,
    alias TEXT NOT NULL UNIQUE,
    provider_id TEXT NOT NULL REFERENCES provider(id),
    upstream_name TEXT NOT NULL,
    capability_set TEXT NOT NULL DEFAULT 'chat,chat_stream',
    origin TEXT NOT NULL DEFAULT 'declared',
    discovered_at TEXT,
    created_at TEXT NOT NULL,
    target_id TEXT REFERENCES model_binding(id)
);
-- client is the caller's User-Agent header, truncated. tags is the caller's
-- x-llmproxy-tags header, normalised to a canonical comma-separated
-- "key:value" list. Both are header metadata only, never request or response
-- content.
CREATE TABLE IF NOT EXISTS usage_event (
    id TEXT PRIMARY KEY,
    ts TEXT NOT NULL,
    principal_id TEXT NOT NULL,
    api_key_id TEXT NOT NULL,
    provider_id TEXT NOT NULL,
    alias TEXT NOT NULL,
    upstream_name TEXT NOT NULL,
    endpoint TEXT NOT NULL,
    client TEXT NOT NULL DEFAULT '',
    tags TEXT NOT NULL DEFAULT '',
    status_code INTEGER,
    outcome TEXT NOT NULL,
    cancelled INTEGER NOT NULL DEFAULT 0,
    streamed INTEGER NOT NULL DEFAULT 0,
    cost DOUBLE PRECISION,
    unpriced INTEGER NOT NULL DEFAULT 0,
    duration_ms INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX IF NOT EXISTS idx_usage_event_ts ON usage_event(ts);
CREATE INDEX IF NOT EXISTS idx_usage_event_principal ON usage_event(principal_id);
CREATE INDEX IF NOT EXISTS idx_usage_event_key ON usage_event(api_key_id);
CREATE TABLE IF NOT EXISTS usage_quantity (
    id TEXT PRIMARY KEY,
    usage_event_id TEXT NOT NULL REFERENCES usage_event(id),
    unit TEXT NOT NULL,
    quantity DOUBLE PRECISION NOT NULL,
    unit_price DOUBLE PRECISION,
    priced INTEGER NOT NULL DEFAULT 0,
    measurement TEXT NOT NULL DEFAULT 'upstream_reported'
);
CREATE INDEX IF NOT EXISTS idx_usage_quantity_event ON usage_quantity(usage_event_id);
CREATE TABLE IF NOT EXISTS pricing_feed (
    id TEXT PRIMARY KEY,
    version TEXT NOT NULL,
    origin TEXT NOT NULL DEFAULT '',
    loaded_at TEXT NOT NULL,
    active INTEGER NOT NULL DEFAULT 1
);
CREATE TABLE IF NOT EXISTS pricing_entry (
    id TEXT PRIMARY KEY,
    feed_id TEXT NOT NULL REFERENCES pricing_feed(id),
    model TEXT NOT NULL,
    unit TEXT NOT NULL,
    price_per_unit DOUBLE PRECISION NOT NULL,
    UNIQUE (feed_id, model, unit)
);
CREATE TABLE IF NOT EXISTS admin_event (
    id TEXT PRIMARY KEY,
    ts TEXT NOT NULL,
    actor_principal_id TEXT NOT NULL,
    action TEXT NOT NULL,
    target_kind TEXT NOT NULL,
    target_ref TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_admin_event_ts ON admin_event(ts)
`
