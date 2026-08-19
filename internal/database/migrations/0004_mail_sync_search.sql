ALTER TABLE accounts ADD COLUMN last_sync_error TEXT;
ALTER TABLE accounts ADD COLUMN sync_failures INTEGER NOT NULL DEFAULT 0 CHECK (sync_failures >= 0);
ALTER TABLE accounts ADD COLUMN sync_next_retry_at_utc TEXT;

CREATE TABLE folders (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    account_id INTEGER NOT NULL,
    graph_id TEXT NOT NULL,
    well_known_name TEXT NOT NULL CHECK (well_known_name IN ('inbox', 'junkemail')),
    display_name TEXT NOT NULL,
    sync_status TEXT NOT NULL DEFAULT 'pending'
        CHECK (sync_status IN ('pending', 'active', 'error', 'resync_required')),
    last_synced_at_utc TEXT,
    last_error TEXT,
    created_at_utc TEXT NOT NULL,
    updated_at_utc TEXT NOT NULL,
    UNIQUE (account_id, well_known_name),
    UNIQUE (account_id, graph_id),
    FOREIGN KEY (account_id) REFERENCES accounts(id) ON DELETE CASCADE
);

CREATE INDEX folders_account_status_idx ON folders(account_id, sync_status);

CREATE TABLE sync_cursors (
    folder_id INTEGER PRIMARY KEY,
    delta_link TEXT,
    initial_window_start_utc TEXT NOT NULL,
    last_success_at_utc TEXT,
    updated_at_utc TEXT NOT NULL,
    FOREIGN KEY (folder_id) REFERENCES folders(id) ON DELETE CASCADE
);

CREATE TABLE messages (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    public_id TEXT NOT NULL UNIQUE,
    account_id INTEGER NOT NULL,
    folder_id INTEGER NOT NULL,
    immutable_id TEXT NOT NULL,
    internet_message_id TEXT,
    subject TEXT NOT NULL DEFAULT '',
    sender_name TEXT NOT NULL DEFAULT '',
    sender_address TEXT NOT NULL DEFAULT '',
    received_at_utc TEXT NOT NULL,
    is_read INTEGER NOT NULL DEFAULT 0 CHECK (is_read IN (0, 1)),
    is_flagged INTEGER NOT NULL DEFAULT 0 CHECK (is_flagged IN (0, 1)),
    body_text TEXT,
    body_cached_at_utc TEXT,
    body_truncated INTEGER NOT NULL DEFAULT 0 CHECK (body_truncated IN (0, 1)),
    remote_deleted INTEGER NOT NULL DEFAULT 0 CHECK (remote_deleted IN (0, 1)),
    created_at_utc TEXT NOT NULL,
    updated_at_utc TEXT NOT NULL,
    UNIQUE (account_id, immutable_id),
    FOREIGN KEY (account_id) REFERENCES accounts(id) ON DELETE CASCADE,
    FOREIGN KEY (folder_id) REFERENCES folders(id) ON DELETE RESTRICT
);

CREATE INDEX messages_received_idx ON messages(remote_deleted, received_at_utc DESC, id DESC);
CREATE INDEX messages_account_received_idx ON messages(account_id, remote_deleted, received_at_utc DESC, id DESC);
CREATE INDEX messages_folder_received_idx ON messages(folder_id, remote_deleted, received_at_utc DESC, id DESC);

CREATE VIRTUAL TABLE message_fts USING fts5(
    subject,
    sender_name,
    sender_address,
    body_text,
    content='messages',
    content_rowid='id',
    tokenize='unicode61 remove_diacritics 2'
);

CREATE TRIGGER messages_fts_insert AFTER INSERT ON messages BEGIN
    INSERT INTO message_fts(rowid, subject, sender_name, sender_address, body_text)
    VALUES (new.id, new.subject, new.sender_name, new.sender_address, COALESCE(new.body_text, ''));
END;

CREATE TRIGGER messages_fts_delete AFTER DELETE ON messages BEGIN
    INSERT INTO message_fts(message_fts, rowid, subject, sender_name, sender_address, body_text)
    VALUES ('delete', old.id, old.subject, old.sender_name, old.sender_address, COALESCE(old.body_text, ''));
END;

CREATE TRIGGER messages_fts_update AFTER UPDATE OF subject, sender_name, sender_address, body_text ON messages BEGIN
    INSERT INTO message_fts(message_fts, rowid, subject, sender_name, sender_address, body_text)
    VALUES ('delete', old.id, old.subject, old.sender_name, old.sender_address, COALESCE(old.body_text, ''));
    INSERT INTO message_fts(rowid, subject, sender_name, sender_address, body_text)
    VALUES (new.id, new.subject, new.sender_name, new.sender_address, COALESCE(new.body_text, ''));
END;
