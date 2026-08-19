package mail

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"outlook-mail-manager/internal/accounts"
)

func TestMoveMessageToDeletedItemsAllowsProtectedVerificationMail(t *testing.T) {
	service, store, accountID, folders := newSyncTestService(t)
	defer store.Close()
	now := formatTime(service.now())
	result, err := store.DB.Exec(`
		INSERT INTO messages (
			public_id, account_id, folder_id, immutable_id, subject, body_text,
			received_at_utc, category, cleanup_protected, created_at_utc, updated_at_utc
		) VALUES ('msg_manual_delete', ?, ?, 'immutable-original', 'Verification code', 'secret body',
			?, 'verification', 1, ?, ?)
	`, accountID, folders[0].id, now, now, now)
	if err != nil {
		t.Fatal(err)
	}
	messageID, _ := result.LastInsertId()
	if _, err := store.DB.Exec(`
		INSERT INTO cleanup_actions (public_id, message_id, candidate_reason, created_at_utc, updated_at_utc)
		VALUES ('cleanup_manual_delete', ?, 'legacy candidate', ?, ?)
	`, messageID, now, now); err != nil {
		t.Fatal(err)
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/me/mailFolders/deleteditems":
			_ = json.NewEncoder(w).Encode(map[string]string{"id": "deleted-folder", "displayName": "Deleted Items"})
		case "/me/messages/immutable-original/move":
			var body map[string]string
			_ = json.NewDecoder(r.Body).Decode(&body)
			if body["destinationId"] != "deleted-folder" {
				t.Fatalf("move destination = %q", body["destinationId"])
			}
			_ = json.NewEncoder(w).Encode(map[string]string{"id": "immutable-moved"})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	provider := &fakeTokenProvider{leases: []accounts.AccessTokenLease{{AccessToken: "token", Version: 1}}}
	client, err := newGraphClient(server.URL, provider, server.Client(), service.now)
	if err != nil {
		t.Fatal(err)
	}
	service.graph = client

	if err := service.MoveMessageToDeletedItems(context.Background(), "msg_manual_delete"); err != nil {
		t.Fatalf("MoveMessageToDeletedItems() error = %v", err)
	}
	var immutableID string
	var hidden bool
	var body sql.NullString
	if err := store.DB.QueryRow(`SELECT immutable_id, hidden_from_inbox, body_text FROM messages WHERE id = ?`, messageID).Scan(&immutableID, &hidden, &body); err != nil {
		t.Fatal(err)
	}
	if immutableID != "immutable-moved" || !hidden || body.Valid {
		t.Fatalf("message state = immutable:%q hidden:%v body:%v", immutableID, hidden, body)
	}
	var cleanupState string
	if err := store.DB.QueryRow(`SELECT state FROM cleanup_actions WHERE message_id = ?`, messageID).Scan(&cleanupState); err != nil {
		t.Fatal(err)
	}
	if cleanupState != "deleted" {
		t.Fatalf("cleanup state = %q", cleanupState)
	}
	if _, err := service.GetMessage(context.Background(), "msg_manual_delete"); !errors.Is(err, ErrMessageNotFound) {
		t.Fatalf("GetMessage(hidden) error = %v", err)
	}
	var auditCount int
	if err := store.DB.QueryRow(`SELECT COUNT(*) FROM audit_events WHERE event_type = 'message.moved_to_deleted_items'`).Scan(&auditCount); err != nil || auditCount != 1 {
		t.Fatalf("audit count = %d, error = %v", auditCount, err)
	}
}

func TestMoveMessageToDeletedItemsLeavesLocalMailVisibleOnGraphFailure(t *testing.T) {
	service, store, accountID, folders := newSyncTestService(t)
	defer store.Close()
	now := formatTime(service.now())
	if _, err := store.DB.Exec(`
		INSERT INTO messages (public_id, account_id, folder_id, immutable_id, subject, body_text,
			received_at_utc, created_at_utc, updated_at_utc)
		VALUES ('msg_delete_failure', ?, ?, 'immutable-failure', 'Keep visible', 'body', ?, ?, ?)
	`, accountID, folders[0].id, now, now, now); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"error":{"code":"ErrorAccessDenied"}}`))
	}))
	defer server.Close()
	provider := &fakeTokenProvider{leases: []accounts.AccessTokenLease{{AccessToken: "token", Version: 1}}}
	client, _ := newGraphClient(server.URL, provider, server.Client(), func() time.Time { return service.now() })
	service.graph = client

	if err := service.MoveMessageToDeletedItems(context.Background(), "msg_delete_failure"); err == nil {
		t.Fatal("MoveMessageToDeletedItems() error = nil")
	}
	var hidden bool
	var body string
	if err := store.DB.QueryRow(`SELECT hidden_from_inbox, body_text FROM messages WHERE public_id = 'msg_delete_failure'`).Scan(&hidden, &body); err != nil {
		t.Fatal(err)
	}
	if hidden || body != "body" {
		t.Fatalf("failed Graph move changed local state: hidden=%v body=%q", hidden, body)
	}
}
