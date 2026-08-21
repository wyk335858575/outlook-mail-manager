ALTER TABLE api_tokens ADD COLUMN all_accounts INTEGER NOT NULL DEFAULT 0 CHECK (all_accounts IN (0, 1));
