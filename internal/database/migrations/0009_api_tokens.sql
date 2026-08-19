CREATE TABLE api_tokens (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    public_id TEXT NOT NULL UNIQUE,
    name TEXT NOT NULL,
    token_prefix TEXT NOT NULL,
    token_hash TEXT NOT NULL UNIQUE,
    scopes_json TEXT NOT NULL,
    account_public_ids_json TEXT NOT NULL DEFAULT '[]',
    group_names_json TEXT NOT NULL DEFAULT '[]',
    ip_cidrs_json TEXT NOT NULL DEFAULT '[]',
    expires_at_utc TEXT,
    last_used_at_utc TEXT,
    revoked_at_utc TEXT,
    created_at_utc TEXT NOT NULL
);

CREATE INDEX api_tokens_active_prefix_idx
    ON api_tokens(token_prefix, revoked_at_utc, expires_at_utc);
