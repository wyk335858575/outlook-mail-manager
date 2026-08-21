package notify

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"outlook-mail-manager/internal/database"
	"outlook-mail-manager/internal/datakey"
)

func TestEnqueueMessageMatchesPersonalRulesAndDedupesPerChannel(t *testing.T) {
	ctx := context.Background()
	store, err := database.Open(ctx, t.TempDir())
	if err != nil {
		t.Fatalf("database.Open() error = %v", err)
	}
	defer store.Close()
	keyring := datakey.New(nil)
	if err := keyring.Unlock(make([]byte, 32)); err != nil {
		t.Fatalf("unlock test data key: %v", err)
	}
	service, err := New(store.DB, keyring, Options{Now: func() time.Time {
		return time.Date(2026, time.August, 21, 10, 0, 0, 0, time.UTC)
	}})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	defer service.Close()
	now := formatTime(time.Date(2026, time.August, 21, 10, 0, 0, 0, time.UTC))
	result, err := store.DB.Exec(`
		INSERT INTO accounts (public_id, imported_email, status, created_at_utc, updated_at_utc)
		VALUES ('account_notify', 'notify@example.com', 'active', ?, ?)
	`, now, now)
	if err != nil {
		t.Fatalf("insert account: %v", err)
	}
	accountID, _ := result.LastInsertId()
	result, err = store.DB.Exec(`
		INSERT INTO folders (account_id, graph_id, well_known_name, display_name, sync_status, created_at_utc, updated_at_utc)
		VALUES (?, 'folder-inbox', 'inbox', 'Inbox', 'active', ?, ?)
	`, accountID, now, now)
	if err != nil {
		t.Fatalf("insert folder: %v", err)
	}
	folderID, _ := result.LastInsertId()
	if _, err := store.DB.Exec(`
		INSERT INTO messages (
			public_id, account_id, folder_id, immutable_id, subject, sender_address,
			received_at_utc, category, verification_code, body_text, created_at_utc, updated_at_utc
		) VALUES ('message_personal', ?, ?, 'immutable-personal', 'Login code', 'security@example.com',
			?, 'important', '123456', 'Message body', ?, ?)
	`, accountID, folderID, now, now, now); err != nil {
		t.Fatalf("insert message: %v", err)
	}
	for _, channel := range []string{"channel_one", "channel_two"} {
		if _, err := store.DB.Exec(`
			INSERT INTO notification_channels (
				public_id, name, kind, config_ciphertext, enabled, system_enabled, created_at_utc, updated_at_utc
			) VALUES (?, ?, 'wxpush', 'sealed', 1, 0, ?, ?)
		`, channel, channel, now, now); err != nil {
			t.Fatalf("insert channel %s: %v", channel, err)
		}
	}
	if _, err := store.DB.Exec(`
		INSERT INTO personal_inbox_rules (
			public_id, name, enabled, categories_json, created_at_utc, updated_at_utc
		) VALUES ('personal_rule', 'Important mail', 1, '["important"]', ?, ?)
	`, now, now); err != nil {
		t.Fatalf("insert personal rule: %v", err)
	}
	if _, err := store.DB.Exec(`
		INSERT INTO notification_rules (
			public_id, channel_id, name, enabled, personal_only, categories_json, include_otp,
			created_at_utc, updated_at_utc
		) VALUES
			('rule_personal', (SELECT id FROM notification_channels WHERE public_id = 'channel_one'), 'A personal', 1, 1, '[]', 0, ?, ?),
			('rule_category', (SELECT id FROM notification_channels WHERE public_id = 'channel_one'), 'B category', 1, 0, '["important"]', 1, ?, ?),
			('rule_other_channel', (SELECT id FROM notification_channels WHERE public_id = 'channel_two'), 'C other channel', 1, 0, '["important"]', 0, ?, ?)
	`, now, now, now, now, now, now); err != nil {
		t.Fatalf("insert notification rules: %v", err)
	}

	if err := service.EnqueueMessage(ctx, "message_personal"); err != nil {
		t.Fatalf("EnqueueMessage() error = %v", err)
	}
	rows, err := store.DB.Query(`
		SELECT c.public_id, d.dedupe_key, d.rule_id, d.payload_json
		FROM notification_deliveries d JOIN notification_channels c ON c.id = d.channel_id
		ORDER BY c.public_id
	`)
	if err != nil {
		t.Fatalf("query deliveries: %v", err)
	}
	defer rows.Close()
	var deliveries int
	for rows.Next() {
		var channelID, dedupeKey string
		var ruleID int64
		var payloadJSON string
		if err := rows.Scan(&channelID, &dedupeKey, &ruleID, &payloadJSON); err != nil {
			t.Fatalf("scan delivery: %v", err)
		}
		if dedupeKey != "mail:message_personal:"+channelID || ruleID == 0 {
			t.Fatalf("delivery identity = channel:%q dedupe:%q rule:%d", channelID, dedupeKey, ruleID)
		}
		var payload deliveryPayload
		if err := json.Unmarshal([]byte(payloadJSON), &payload); err != nil {
			t.Fatalf("decode payload: %v", err)
		}
		if channelID == "channel_one" && payload.VerificationCode != "123456" {
			t.Fatalf("merged payload verification code = %q", payload.VerificationCode)
		}
		deliveries++
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate deliveries: %v", err)
	}
	if deliveries != 2 {
		t.Fatalf("delivery count = %d, want one per channel", deliveries)
	}

	if _, err := store.DB.Exec(`
		INSERT INTO messages (
			public_id, account_id, folder_id, immutable_id, subject, sender_address,
			received_at_utc, category, created_at_utc, updated_at_utc
		) VALUES ('message_not_personal', ?, ?, 'immutable-not-personal', 'Newsletter', 'news@example.com',
			?, 'normal', ?, ?)
	`, accountID, folderID, now, now, now); err != nil {
		t.Fatalf("insert non-personal message: %v", err)
	}
	if err := service.EnqueueMessage(ctx, "message_not_personal"); err != nil {
		t.Fatalf("EnqueueMessage(non-personal) error = %v", err)
	}
	var total int
	if err := store.DB.QueryRow(`SELECT COUNT(*) FROM notification_deliveries`).Scan(&total); err != nil {
		t.Fatalf("count deliveries: %v", err)
	}
	if total != 2 {
		t.Fatalf("delivery count after non-personal message = %d, want 2", total)
	}
}

