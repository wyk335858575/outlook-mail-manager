ALTER TABLE notification_rules ADD COLUMN personal_inbox INTEGER NOT NULL DEFAULT 0
    CHECK (personal_inbox IN (0, 1));

ALTER TABLE app_settings ADD COLUMN default_folder TEXT NOT NULL DEFAULT 'all'
    CHECK (default_folder IN ('all', 'inbox', 'junkemail'));
ALTER TABLE app_settings ADD COLUMN default_unread_only INTEGER NOT NULL DEFAULT 0
    CHECK (default_unread_only IN (0, 1));
ALTER TABLE app_settings ADD COLUMN auto_select_first_message INTEGER NOT NULL DEFAULT 1
    CHECK (auto_select_first_message IN (0, 1));
ALTER TABLE app_settings ADD COLUMN show_body_preview INTEGER NOT NULL DEFAULT 1
    CHECK (show_body_preview IN (0, 1));

CREATE INDEX notification_rules_personal_enabled_idx
    ON notification_rules(personal_inbox, enabled, id);
