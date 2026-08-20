ALTER TABLE accounts ADD COLUMN auth_method TEXT NOT NULL DEFAULT 'web'
    CHECK (auth_method IN ('web', 'oauth'));

CREATE INDEX accounts_auth_method_idx ON accounts(auth_method, created_at_utc);
