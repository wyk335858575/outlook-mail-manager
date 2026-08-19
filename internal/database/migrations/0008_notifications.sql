CREATE TABLE notification_channels (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    public_id TEXT NOT NULL UNIQUE,
    name TEXT NOT NULL,
    kind TEXT NOT NULL CHECK (kind IN ('telegram', 'pushplus', 'webhook')),
    config_ciphertext TEXT NOT NULL,
    enabled INTEGER NOT NULL DEFAULT 1 CHECK (enabled IN (0, 1)),
    system_enabled INTEGER NOT NULL DEFAULT 1 CHECK (system_enabled IN (0, 1)),
    created_at_utc TEXT NOT NULL,
    updated_at_utc TEXT NOT NULL
);

CREATE INDEX notification_channels_enabled_idx
    ON notification_channels(enabled, kind, id);

CREATE TABLE notification_rules (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    public_id TEXT NOT NULL UNIQUE,
    channel_id INTEGER NOT NULL,
    name TEXT NOT NULL,
    enabled INTEGER NOT NULL DEFAULT 1 CHECK (enabled IN (0, 1)),
    account_public_ids_json TEXT NOT NULL DEFAULT '[]',
    group_names_json TEXT NOT NULL DEFAULT '[]',
    tag_names_json TEXT NOT NULL DEFAULT '[]',
    categories_json TEXT NOT NULL DEFAULT '[]',
    sender_address TEXT NOT NULL DEFAULT '',
    sender_domain TEXT NOT NULL DEFAULT '',
    subject_keywords_json TEXT NOT NULL DEFAULT '[]',
    start_minute INTEGER NOT NULL DEFAULT -1 CHECK (start_minute BETWEEN -1 AND 1439),
    end_minute INTEGER NOT NULL DEFAULT -1 CHECK (end_minute BETWEEN -1 AND 1439),
    require_otp INTEGER NOT NULL DEFAULT 0 CHECK (require_otp IN (0, 1)),
    include_otp INTEGER NOT NULL DEFAULT 0 CHECK (include_otp IN (0, 1)),
    created_at_utc TEXT NOT NULL,
    updated_at_utc TEXT NOT NULL,
    FOREIGN KEY (channel_id) REFERENCES notification_channels(id) ON DELETE CASCADE
);

CREATE INDEX notification_rules_enabled_idx
    ON notification_rules(enabled, channel_id, id);

CREATE TABLE notification_deliveries (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    public_id TEXT NOT NULL UNIQUE,
    message_id INTEGER,
    rule_id INTEGER,
    channel_id INTEGER NOT NULL,
    event_type TEXT NOT NULL CHECK (event_type IN ('mail', 'system', 'test')),
    status TEXT NOT NULL DEFAULT 'queued'
        CHECK (status IN ('queued', 'sending', 'sent', 'failed')),
    dedupe_key TEXT NOT NULL UNIQUE,
    payload_json TEXT NOT NULL,
    attempt_count INTEGER NOT NULL DEFAULT 0 CHECK (attempt_count >= 0),
    next_retry_at_utc TEXT,
    last_error TEXT,
    sent_at_utc TEXT,
    created_at_utc TEXT NOT NULL,
    updated_at_utc TEXT NOT NULL,
    FOREIGN KEY (message_id) REFERENCES messages(id) ON DELETE RESTRICT,
    FOREIGN KEY (rule_id) REFERENCES notification_rules(id) ON DELETE SET NULL,
    FOREIGN KEY (channel_id) REFERENCES notification_channels(id) ON DELETE CASCADE
);

CREATE INDEX notification_deliveries_due_idx
    ON notification_deliveries(status, next_retry_at_utc, created_at_utc, id);
