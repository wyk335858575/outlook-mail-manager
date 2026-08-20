-- Use the faster default when the legacy 10-minute value was never changed.
-- A later settings update is treated as an explicit administrator choice.
UPDATE app_settings
SET sync_interval_seconds = 5
WHERE id = 1
  AND sync_interval_seconds = 600
  AND julianday(updated_at_utc) <= (
      SELECT julianday(applied_at_utc)
      FROM schema_migrations
      WHERE version = 13
  );
