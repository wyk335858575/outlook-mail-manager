DELETE FROM sessions;
DELETE FROM recovery_codes;
DELETE FROM admins;
DELETE FROM account_tokens;

UPDATE accounts
SET status = CASE WHEN status = 'disabled' THEN status ELSE 'reauth_required' END,
    reauth_reason = CASE
        WHEN status = 'disabled' THEN reauth_reason
        ELSE '管理员安全机制已升级，请重新授权 Microsoft 账号'
    END,
    last_oauth_error = NULL,
    consecutive_failures = 0,
    next_retry_at_utc = NULL,
    updated_at_utc = strftime('%Y-%m-%dT%H:%M:%fZ', 'now');

DROP TABLE recovery_codes;

ALTER TABLE admins ADD COLUMN username TEXT COLLATE NOCASE;
ALTER TABLE admins ADD COLUMN key_salt BLOB;
ALTER TABLE admins ADD COLUMN wrapped_data_key TEXT;

UPDATE app_metadata
SET value = 'administrator_setup_required',
    updated_at_utc = strftime('%Y-%m-%dT%H:%M:%fZ', 'now')
WHERE key = 'installation_state';

INSERT INTO audit_events (event_type, actor_type, details_json, created_at_utc)
VALUES (
    'administrator_security_migrated',
    'system',
    '{"credentials_reset":true,"mail_data_preserved":true}',
    strftime('%Y-%m-%dT%H:%M:%fZ', 'now')
);
