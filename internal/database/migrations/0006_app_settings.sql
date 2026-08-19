CREATE TABLE app_settings (
    id INTEGER PRIMARY KEY CHECK (id = 1),
    sync_interval_minutes INTEGER NOT NULL CHECK (sync_interval_minutes IN (5, 10, 15, 30, 60)),
    initial_sync_days INTEGER NOT NULL CHECK (initial_sync_days IN (7, 14, 30, 60, 90)),
    body_cache_kib INTEGER NOT NULL CHECK (body_cache_kib IN (64, 128, 256, 512, 1024)),
    message_page_size INTEGER NOT NULL CHECK (message_page_size IN (25, 50, 100, 200)),
    timezone TEXT NOT NULL,
    reader_mode TEXT NOT NULL CHECK (reader_mode IN ('text', 'html')),
    mark_read_on_open INTEGER NOT NULL CHECK (mark_read_on_open IN (0, 1)),
    updated_at_utc TEXT NOT NULL
);

INSERT INTO app_settings (
    id, sync_interval_minutes, initial_sync_days, body_cache_kib,
    message_page_size, timezone, reader_mode, mark_read_on_open, updated_at_utc
) VALUES (
    1, 10, 30, 256, 100, 'Asia/Shanghai', 'text', 1,
    strftime('%Y-%m-%dT%H:%M:%fZ', 'now')
);
