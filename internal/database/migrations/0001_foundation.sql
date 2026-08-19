CREATE TABLE app_metadata (
    key TEXT PRIMARY KEY,
    value TEXT NOT NULL,
    updated_at_utc TEXT NOT NULL
);

INSERT INTO app_metadata (key, value, updated_at_utc)
VALUES ('installation_state', 'foundation_ready', strftime('%Y-%m-%dT%H:%M:%fZ', 'now'));
