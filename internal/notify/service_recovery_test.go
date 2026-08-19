package notify

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"outlook-mail-manager/internal/database"
	"outlook-mail-manager/internal/datakey"
)

func TestStartRecoversInterruptedDeliveries(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()
	store, err := database.Open(context.Background(), t.TempDir())
	if err != nil {
		t.Fatalf("database.Open() error = %v", err)
	}
	defer store.Close()
	keyring := datakey.New(nil)
	if err := keyring.Unlock(make([]byte, 32)); err != nil {
		t.Fatalf("unlock test data key: %v", err)
	}
	service, err := New(store.DB, keyring, Options{HTTPClient: server.Client(), Workers: 1})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	defer service.Close()
	channel, err := service.CreateChannel(context.Background(), ChannelInput{
		Name: "Webhook", Kind: "webhook", Enabled: true,
		WebhookURL: server.URL, WebhookSecret: "secret",
	})
	if err != nil {
		t.Fatalf("CreateChannel() error = %v", err)
	}
	var channelID int64
	if err := store.DB.QueryRow(`SELECT id FROM notification_channels WHERE public_id = ?`, channel.PublicID).Scan(&channelID); err != nil {
		t.Fatalf("load channel id: %v", err)
	}
	now := formatTime(time.Now().UTC())
	if _, err := store.DB.Exec(`
		INSERT INTO notification_deliveries (
			public_id, channel_id, event_type, status, dedupe_key, payload_json, created_at_utc, updated_at_utc
		) VALUES ('delivery_interrupted', ?, 'system', 'sending', 'interrupted',
			'{"event_type":"system","title":"Test","text":"Interrupted"}', ?, ?)
	`, channelID, now, now); err != nil {
		t.Fatalf("insert interrupted delivery: %v", err)
	}

	service.Start()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		var status string
		if err := store.DB.QueryRow(`SELECT status FROM notification_deliveries WHERE public_id = 'delivery_interrupted'`).Scan(&status); err != nil {
			t.Fatalf("load delivery status: %v", err)
		}
		if status == "sent" {
			if requests.Load() != 1 {
				t.Fatalf("webhook requests = %d, want 1", requests.Load())
			}
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("interrupted delivery was not recovered; webhook requests = %d", requests.Load())
}
