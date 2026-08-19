CREATE TABLE admins (
    id INTEGER PRIMARY KEY CHECK (id = 1),
    password_hash TEXT NOT NULL,
    password_algorithm TEXT NOT NULL,
    totp_secret_ciphertext TEXT NOT NULL,
    last_totp_step INTEGER NOT NULL DEFAULT 0,
    created_at_utc TEXT NOT NULL,
    password_updated_at_utc TEXT NOT NULL
);

CREATE TABLE recovery_codes (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    admin_id INTEGER NOT NULL CHECK (admin_id = 1),
    code_hash BLOB NOT NULL UNIQUE CHECK (length(code_hash) = 32),
    created_at_utc TEXT NOT NULL,
    used_at_utc TEXT,
    FOREIGN KEY (admin_id) REFERENCES admins(id) ON DELETE RESTRICT
);

CREATE INDEX recovery_codes_available_idx
ON recovery_codes(admin_id, used_at_utc);

CREATE TABLE sessions (
    token_hash BLOB PRIMARY KEY CHECK (length(token_hash) = 32),
    admin_id INTEGER NOT NULL CHECK (admin_id = 1),
    created_at_utc TEXT NOT NULL,
    expires_at_utc TEXT NOT NULL,
    last_seen_at_utc TEXT NOT NULL,
    revoked_at_utc TEXT,
    FOREIGN KEY (admin_id) REFERENCES admins(id) ON DELETE RESTRICT
);

CREATE INDEX sessions_active_idx
ON sessions(admin_id, expires_at_utc, revoked_at_utc);

CREATE TABLE audit_events (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    event_type TEXT NOT NULL,
    actor_type TEXT NOT NULL,
    details_json TEXT NOT NULL DEFAULT '{}',
    created_at_utc TEXT NOT NULL
);

CREATE TRIGGER audit_events_no_update
BEFORE UPDATE ON audit_events
BEGIN
    SELECT RAISE(ABORT, 'audit events are immutable');
END;

CREATE TRIGGER audit_events_no_delete
BEFORE DELETE ON audit_events
BEGIN
    SELECT RAISE(ABORT, 'audit events are immutable');
END;
