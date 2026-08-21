ALTER TABLE notification_rules ADD COLUMN personal_only INTEGER NOT NULL DEFAULT 0
    CHECK (personal_only IN (0, 1));

CREATE INDEX notification_rules_personal_only_idx
    ON notification_rules(personal_only, enabled, channel_id, id);

