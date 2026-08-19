CREATE TABLE app_settings_v13 (
    id INTEGER PRIMARY KEY CHECK (id = 1),
    sync_interval_seconds INTEGER NOT NULL
        CHECK (sync_interval_seconds IN (5, 60, 300, 600, 900, 1800, 3600)),
    initial_sync_days INTEGER NOT NULL CHECK (initial_sync_days IN (7, 14, 30, 60, 90)),
    body_cache_kib INTEGER NOT NULL CHECK (body_cache_kib IN (64, 128, 256, 512, 1024)),
    message_page_size INTEGER NOT NULL CHECK (message_page_size IN (25, 50, 100, 200)),
    timezone TEXT NOT NULL,
    reader_mode TEXT NOT NULL CHECK (reader_mode IN ('text', 'html')),
    mark_read_on_open INTEGER NOT NULL CHECK (mark_read_on_open IN (0, 1)),
    updated_at_utc TEXT NOT NULL,
    default_folder TEXT NOT NULL DEFAULT 'all'
        CHECK (default_folder IN ('all', 'inbox', 'junkemail')),
    default_unread_only INTEGER NOT NULL DEFAULT 0 CHECK (default_unread_only IN (0, 1)),
    auto_select_first_message INTEGER NOT NULL DEFAULT 1 CHECK (auto_select_first_message IN (0, 1)),
    show_body_preview INTEGER NOT NULL DEFAULT 1 CHECK (show_body_preview IN (0, 1))
);

INSERT INTO app_settings_v13 (
    id, sync_interval_seconds, initial_sync_days, body_cache_kib,
    message_page_size, timezone, reader_mode, mark_read_on_open, updated_at_utc,
    default_folder, default_unread_only, auto_select_first_message, show_body_preview
)
SELECT
    id, sync_interval_minutes * 60, initial_sync_days, body_cache_kib,
    message_page_size, timezone, reader_mode, mark_read_on_open, updated_at_utc,
    default_folder, default_unread_only, auto_select_first_message, show_body_preview
FROM app_settings;

DROP TABLE app_settings;
ALTER TABLE app_settings_v13 RENAME TO app_settings;
