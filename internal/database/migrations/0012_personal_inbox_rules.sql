CREATE TABLE personal_inbox_rules (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    public_id TEXT NOT NULL UNIQUE,
    name TEXT NOT NULL,
    enabled INTEGER NOT NULL DEFAULT 1 CHECK (enabled IN (0, 1)),
    account_public_ids_json TEXT NOT NULL DEFAULT '[]',
    group_names_json TEXT NOT NULL DEFAULT '[]',
    tag_names_json TEXT NOT NULL DEFAULT '[]',
    categories_json TEXT NOT NULL DEFAULT '[]',
    sender_address TEXT NOT NULL DEFAULT '',
    sender_domain TEXT NOT NULL DEFAULT '',
    subject_keywords_json TEXT NOT NULL DEFAULT '[]',
    require_otp INTEGER NOT NULL DEFAULT 0 CHECK (require_otp IN (0, 1)),
    created_at_utc TEXT NOT NULL,
    updated_at_utc TEXT NOT NULL
);

CREATE INDEX personal_inbox_rules_enabled_idx
    ON personal_inbox_rules(enabled, id);

INSERT INTO personal_inbox_rules (
    public_id, name, enabled, account_public_ids_json, group_names_json,
    tag_names_json, categories_json, sender_address, sender_domain,
    subject_keywords_json, require_otp, created_at_utc, updated_at_utc
)
SELECT
    public_id, name, enabled, account_public_ids_json, group_names_json,
    tag_names_json, categories_json, sender_address, sender_domain,
    subject_keywords_json, require_otp, created_at_utc, updated_at_utc
FROM notification_rules
WHERE personal_inbox = 1;

UPDATE notification_rules SET personal_inbox = 0 WHERE personal_inbox = 1;
