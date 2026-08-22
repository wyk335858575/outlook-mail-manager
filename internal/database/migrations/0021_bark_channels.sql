DROP INDEX notification_channels_enabled_idx;
DROP INDEX notification_rules_enabled_idx;
DROP INDEX notification_rules_personal_enabled_idx;
DROP INDEX notification_rules_personal_only_idx;
DROP INDEX notification_deliveries_due_idx;

ALTER TABLE notification_channels RENAME TO notification_channels_v20;
CREATE TABLE notification_channels (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    public_id TEXT NOT NULL UNIQUE,
    name TEXT NOT NULL,
    kind TEXT NOT NULL CHECK (kind IN ('telegram', 'pushplus', 'wxpush', 'bark')),
    config_ciphertext TEXT NOT NULL,
    enabled INTEGER NOT NULL DEFAULT 1 CHECK (enabled IN (0, 1)),
    system_enabled INTEGER NOT NULL DEFAULT 1 CHECK (system_enabled IN (0, 1)),
    created_at_utc TEXT NOT NULL,
    updated_at_utc TEXT NOT NULL
);
INSERT INTO notification_channels (
    id, public_id, name, kind, config_ciphertext, enabled, system_enabled, created_at_utc, updated_at_utc
)
SELECT id, public_id, name, kind, config_ciphertext, enabled, system_enabled, created_at_utc, updated_at_utc
FROM notification_channels_v20;

ALTER TABLE notification_rules RENAME TO notification_rules_v20;
CREATE TABLE notification_rules (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    public_id TEXT NOT NULL UNIQUE,
    channel_id INTEGER NOT NULL,
    name TEXT NOT NULL,
    enabled INTEGER NOT NULL DEFAULT 1 CHECK (enabled IN (0, 1)),
    personal_inbox INTEGER NOT NULL DEFAULT 0 CHECK (personal_inbox IN (0, 1)),
    personal_only INTEGER NOT NULL DEFAULT 0 CHECK (personal_only IN (0, 1)),
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
INSERT INTO notification_rules (
    id, public_id, channel_id, name, enabled, personal_inbox, personal_only, account_public_ids_json,
    group_names_json, tag_names_json, categories_json, sender_address, sender_domain,
    subject_keywords_json, start_minute, end_minute, require_otp, include_otp,
    created_at_utc, updated_at_utc
)
SELECT id, public_id, channel_id, name, enabled, personal_inbox, personal_only, account_public_ids_json,
    group_names_json, tag_names_json, categories_json, sender_address, sender_domain,
    subject_keywords_json, start_minute, end_minute, require_otp, include_otp,
    created_at_utc, updated_at_utc
FROM notification_rules_v20;

ALTER TABLE notification_deliveries RENAME TO notification_deliveries_v20;
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
INSERT INTO notification_deliveries (
    id, public_id, message_id, rule_id, channel_id, event_type, status, dedupe_key,
    payload_json, attempt_count, next_retry_at_utc, last_error, sent_at_utc,
    created_at_utc, updated_at_utc
)
SELECT id, public_id, message_id, rule_id, channel_id, event_type, status, dedupe_key,
    payload_json, attempt_count, next_retry_at_utc, last_error, sent_at_utc,
    created_at_utc, updated_at_utc
FROM notification_deliveries_v20;

DROP TABLE notification_deliveries_v20;
DROP TABLE notification_rules_v20;
DROP TABLE notification_channels_v20;

CREATE INDEX notification_channels_enabled_idx
    ON notification_channels(enabled, kind, id);

CREATE INDEX notification_rules_enabled_idx
    ON notification_rules(enabled, channel_id, id);

CREATE INDEX notification_rules_personal_enabled_idx
    ON notification_rules(personal_inbox, enabled, id);

CREATE INDEX notification_rules_personal_only_idx
    ON notification_rules(personal_only, enabled, channel_id, id);

CREATE INDEX notification_deliveries_due_idx
    ON notification_deliveries(status, next_retry_at_utc, created_at_utc, id);
