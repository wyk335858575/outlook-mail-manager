CREATE TABLE accounts (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    public_id TEXT NOT NULL UNIQUE,
    imported_email TEXT NOT NULL COLLATE NOCASE UNIQUE,
    microsoft_user_id TEXT UNIQUE,
    primary_email TEXT,
    display_name TEXT,
    notes TEXT NOT NULL DEFAULT '',
    status TEXT NOT NULL DEFAULT 'pending'
        CHECK (status IN ('pending', 'active', 'degraded', 'reauth_required', 'disabled')),
    reauth_reason TEXT,
    last_oauth_error TEXT,
    consecutive_failures INTEGER NOT NULL DEFAULT 0 CHECK (consecutive_failures >= 0),
    next_retry_at_utc TEXT,
    last_graph_success_at_utc TEXT,
    last_sync_success_at_utc TEXT,
    sync_backlog INTEGER NOT NULL DEFAULT 0 CHECK (sync_backlog >= 0),
    disabled_at_utc TEXT,
    created_at_utc TEXT NOT NULL,
    updated_at_utc TEXT NOT NULL
);

CREATE INDEX accounts_status_idx ON accounts(status, created_at_utc);

CREATE TABLE account_tokens (
    account_id INTEGER PRIMARY KEY,
    access_token_ciphertext TEXT NOT NULL,
    access_expires_at_utc TEXT NOT NULL,
    refresh_token_ciphertext TEXT NOT NULL,
    token_type TEXT NOT NULL,
    granted_scopes TEXT NOT NULL,
    token_version INTEGER NOT NULL DEFAULT 1 CHECK (token_version >= 1),
    last_refresh_at_utc TEXT,
    last_refresh_success_at_utc TEXT,
    created_at_utc TEXT NOT NULL,
    updated_at_utc TEXT NOT NULL,
    FOREIGN KEY (account_id) REFERENCES accounts(id) ON DELETE RESTRICT
);

CREATE TABLE account_groups (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    name TEXT NOT NULL COLLATE NOCASE UNIQUE,
    created_at_utc TEXT NOT NULL
);

CREATE TABLE account_tags (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    name TEXT NOT NULL COLLATE NOCASE UNIQUE,
    created_at_utc TEXT NOT NULL
);

CREATE TABLE account_group_members (
    account_id INTEGER NOT NULL,
    group_id INTEGER NOT NULL,
    PRIMARY KEY (account_id, group_id),
    FOREIGN KEY (account_id) REFERENCES accounts(id) ON DELETE CASCADE,
    FOREIGN KEY (group_id) REFERENCES account_groups(id) ON DELETE CASCADE
);

CREATE TABLE account_tag_members (
    account_id INTEGER NOT NULL,
    tag_id INTEGER NOT NULL,
    PRIMARY KEY (account_id, tag_id),
    FOREIGN KEY (account_id) REFERENCES accounts(id) ON DELETE CASCADE,
    FOREIGN KEY (tag_id) REFERENCES account_tags(id) ON DELETE CASCADE
);

