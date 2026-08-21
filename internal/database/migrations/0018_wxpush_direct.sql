UPDATE notification_deliveries
SET status = 'failed',
    next_retry_at_utc = NULL,
    last_error = 'wxpush_reconfiguration_required'
WHERE status IN ('queued', 'sending')
  AND channel_id IN (
      SELECT id FROM notification_channels WHERE kind = 'wxpush'
  );

UPDATE notification_rules
SET enabled = 0
WHERE channel_id IN (
    SELECT id FROM notification_channels WHERE kind = 'wxpush'
);

UPDATE notification_channels
SET enabled = 0,
    system_enabled = 0
WHERE kind = 'wxpush';

