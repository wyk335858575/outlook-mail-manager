CREATE TABLE microsoft_oauth_config (
    id INTEGER PRIMARY KEY CHECK (id = 1),
    client_id TEXT NOT NULL DEFAULT '',
    updated_at_utc TEXT
);

INSERT INTO microsoft_oauth_config (id, client_id, updated_at_utc)
VALUES (1, '', NULL);

ALTER TABLE account_tokens
ADD COLUMN oauth_client_id TEXT NOT NULL DEFAULT '';
