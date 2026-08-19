ALTER TABLE accounts ADD COLUMN cleanup_protected INTEGER NOT NULL DEFAULT 0
    CHECK (cleanup_protected IN (0, 1));

ALTER TABLE messages ADD COLUMN category TEXT NOT NULL DEFAULT 'normal'
    CHECK (category IN ('important', 'verification', 'marketing', 'spam', 'normal', 'uncertain'));
ALTER TABLE messages ADD COLUMN classification_reason TEXT NOT NULL DEFAULT '默认分类';
ALTER TABLE messages ADD COLUMN classification_source TEXT NOT NULL DEFAULT 'builtin'
    CHECK (classification_source IN ('builtin', 'rule', 'manual'));
ALTER TABLE messages ADD COLUMN verification_code TEXT;
ALTER TABLE messages ADD COLUMN cleanup_protected INTEGER NOT NULL DEFAULT 0
    CHECK (cleanup_protected IN (0, 1));
ALTER TABLE messages ADD COLUMN cleanup_protection_reason TEXT;
ALTER TABLE messages ADD COLUMN hidden_from_inbox INTEGER NOT NULL DEFAULT 0
    CHECK (hidden_from_inbox IN (0, 1));

CREATE INDEX messages_category_received_idx
    ON messages(category, hidden_from_inbox, remote_deleted, received_at_utc DESC, id DESC);

CREATE TABLE classification_rules (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    public_id TEXT NOT NULL UNIQUE,
    name TEXT NOT NULL,
    match_field TEXT NOT NULL
        CHECK (match_field IN ('sender', 'domain', 'subject', 'body')),
    match_operator TEXT NOT NULL
        CHECK (match_operator IN ('equals', 'contains')),
    match_value TEXT NOT NULL,
    target_category TEXT
        CHECK (target_category IS NULL OR target_category IN ('important', 'verification', 'marketing', 'spam', 'normal', 'uncertain')),
    protects_cleanup INTEGER NOT NULL DEFAULT 0
        CHECK (protects_cleanup IN (0, 1)),
    priority INTEGER NOT NULL DEFAULT 100 CHECK (priority BETWEEN 0 AND 1000),
    enabled INTEGER NOT NULL DEFAULT 1 CHECK (enabled IN (0, 1)),
    created_at_utc TEXT NOT NULL,
    updated_at_utc TEXT NOT NULL,
    CHECK (target_category IS NOT NULL OR protects_cleanup = 1)
);

CREATE INDEX classification_rules_enabled_priority_idx
    ON classification_rules(enabled, priority DESC, id ASC);

CREATE TABLE cleanup_folders (
    account_id INTEGER PRIMARY KEY,
    graph_folder_id TEXT NOT NULL,
    display_name TEXT NOT NULL,
    created_at_utc TEXT NOT NULL,
    updated_at_utc TEXT NOT NULL,
    FOREIGN KEY (account_id) REFERENCES accounts(id) ON DELETE CASCADE
);

CREATE TABLE cleanup_actions (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    public_id TEXT NOT NULL UNIQUE,
    message_id INTEGER NOT NULL UNIQUE,
    state TEXT NOT NULL DEFAULT 'candidate'
        CHECK (state IN ('candidate', 'dismissed', 'holding', 'restored', 'deleted', 'failed')),
    candidate_reason TEXT NOT NULL,
    original_folder_graph_id TEXT,
    holding_folder_graph_id TEXT,
    approved_at_utc TEXT,
    execute_after_utc TEXT,
    moved_at_utc TEXT,
    restored_at_utc TEXT,
    completed_at_utc TEXT,
    last_error TEXT,
    attempt_count INTEGER NOT NULL DEFAULT 0 CHECK (attempt_count >= 0),
    next_retry_at_utc TEXT,
    created_at_utc TEXT NOT NULL,
    updated_at_utc TEXT NOT NULL,
    FOREIGN KEY (message_id) REFERENCES messages(id) ON DELETE RESTRICT
);

CREATE INDEX cleanup_actions_state_due_idx
    ON cleanup_actions(state, execute_after_utc, updated_at_utc);

ALTER TABLE audit_events ADD COLUMN public_id TEXT;
ALTER TABLE audit_events ADD COLUMN entity_type TEXT;
ALTER TABLE audit_events ADD COLUMN entity_public_id TEXT;

CREATE UNIQUE INDEX audit_events_public_id_idx
    ON audit_events(public_id) WHERE public_id IS NOT NULL;

CREATE INDEX audit_events_entity_time_idx
    ON audit_events(entity_type, entity_public_id, created_at_utc DESC, id DESC);
