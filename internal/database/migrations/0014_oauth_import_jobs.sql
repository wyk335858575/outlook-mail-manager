CREATE TABLE oauth_import_jobs (
    public_id TEXT PRIMARY KEY,
    overwrite_existing INTEGER NOT NULL CHECK (overwrite_existing IN (0, 1)),
    state TEXT NOT NULL CHECK (state IN ('queued', 'running', 'completed', 'failed')),
    total_count INTEGER NOT NULL CHECK (total_count BETWEEN 1 AND 1000),
    processed_count INTEGER NOT NULL DEFAULT 0 CHECK (processed_count >= 0),
    created_count INTEGER NOT NULL DEFAULT 0 CHECK (created_count >= 0),
    updated_count INTEGER NOT NULL DEFAULT 0 CHECK (updated_count >= 0),
    skipped_count INTEGER NOT NULL DEFAULT 0 CHECK (skipped_count >= 0),
    failed_count INTEGER NOT NULL DEFAULT 0 CHECK (failed_count >= 0),
    created_at_utc TEXT NOT NULL,
    updated_at_utc TEXT NOT NULL,
    completed_at_utc TEXT
);

CREATE TABLE oauth_import_items (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    job_public_id TEXT NOT NULL,
    row_number INTEGER NOT NULL,
    email TEXT NOT NULL COLLATE NOCASE,
    client_id TEXT NOT NULL,
    refresh_token_ciphertext TEXT,
    state TEXT NOT NULL CHECK (state IN ('queued', 'running', 'created', 'updated', 'skipped', 'failed')),
    error_code TEXT,
    message TEXT,
    created_at_utc TEXT NOT NULL,
    updated_at_utc TEXT NOT NULL,
    UNIQUE (job_public_id, row_number),
    FOREIGN KEY (job_public_id) REFERENCES oauth_import_jobs(public_id) ON DELETE CASCADE
);

CREATE INDEX oauth_import_items_job_state_idx ON oauth_import_items(job_public_id, state, row_number);
