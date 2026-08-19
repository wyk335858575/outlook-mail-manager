package mail

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"outlook-mail-manager/internal/accounts"
)

func TestSearchMessagesEscapesQueryAndAppliesFilters(t *testing.T) {
	service, store, accountID, folders := newSyncTestService(t)
	defer store.Close()
	now := formatTime(service.now())
	for _, message := range []struct {
		publicID, immutableID, subject, body string
		folderID                             int64
		read                                 bool
	}{
		{"msg_invoice", "immutable-invoice", `Invoice "Alpha"`, "payment due today", folders[0].id, false},
		{"msg_read", "immutable-read", "Invoice archive", "already handled", folders[0].id, true},
		{"msg_junk", "immutable-junk", "Promotion", "invoice alpha offer", folders[1].id, false},
	} {
		if _, err := store.DB.Exec(`
			INSERT INTO messages (
				public_id, account_id, folder_id, immutable_id, subject, sender_name, sender_address,
				received_at_utc, is_read, body_text, created_at_utc, updated_at_utc
			) VALUES (?, ?, ?, ?, ?, 'Billing', 'billing@example.com', ?, ?, ?, ?, ?)
		`, message.publicID, accountID, message.folderID, message.immutableID, message.subject,
			now, message.read, message.body, now, now); err != nil {
			t.Fatalf("insert message: %v", err)
		}
	}

	unread := true
	items, err := service.SearchMessages(context.Background(), MessageFilter{
		Query: "invoice alpha", Account: "acc_mail_test", Folder: "inbox", Unread: &unread, Limit: 500,
	})
	if err != nil {
		t.Fatalf("SearchMessages() error = %v", err)
	}
	if len(items) != 1 || items[0].PublicID != "msg_invoice" || !items[0].Unread {
		t.Fatalf("SearchMessages() = %#v", items)
	}
	if _, err := service.SearchMessages(context.Background(), MessageFilter{Query: `" OR *`}); err != nil {
		t.Fatalf("unsafe-looking query returned error: %v", err)
	}
}

func TestSearchMessagesPersonalInboxUsesIndependentRules(t *testing.T) {
	service, store, accountID, folders := newSyncTestService(t)
	defer store.Close()
	now := formatTime(service.now())
	for _, message := range []struct {
		publicID, immutableID, subject, sender, category string
	}{
		{"msg_payment", "immutable-payment", "PayPal project payment received", "notice@paypal.com", "important"},
		{"msg_other", "immutable-other", "Weekly account summary", "notice@paypal.com", "normal"},
		{"msg_commission", "immutable-commission", "RebatesMe commission paid", "payments@rebatesme.com", "important"},
		{"msg_cashback", "immutable-cashback", "Cashback available", "payments@rebatesme.com", "important"},
		{"msg_wrong_domain", "immutable-wrong-domain", "PayPal project payment received", "notice@example.com", "important"},
	} {
		if _, err := store.DB.Exec(`
			INSERT INTO messages (
				public_id, account_id, folder_id, immutable_id, subject, sender_address,
				received_at_utc, category, created_at_utc, updated_at_utc
			) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		`, message.publicID, accountID, folders[0].id, message.immutableID, message.subject,
			message.sender, now, message.category, now, now); err != nil {
			t.Fatalf("insert message: %v", err)
		}
	}
	if _, err := store.DB.Exec(`
		INSERT INTO notification_channels (
			public_id, name, kind, config_ciphertext, enabled, system_enabled, created_at_utc, updated_at_utc
		) VALUES ('channel_personal', '付款通知', 'webhook', 'sealed', 1, 0, ?, ?)
	`, now, now); err != nil {
		t.Fatalf("insert notification channel: %v", err)
	}
	if _, err := store.DB.Exec(`
		INSERT INTO personal_inbox_rules (
			public_id, name, enabled, account_public_ids_json,
			categories_json, sender_domain, subject_keywords_json, created_at_utc, updated_at_utc
		) VALUES (
			'rule_personal', 'PayPal 付款', 1, '["acc_mail_test"]',
			'["important"]', 'paypal.com', '["payment"]', ?, ?
		)
	`, now, now); err != nil {
		t.Fatalf("insert personal inbox rule: %v", err)
	}
	if _, err := store.DB.Exec(`
		INSERT INTO personal_inbox_rules (
			public_id, name, enabled, categories_json, sender_domain,
			subject_keywords_json, created_at_utc, updated_at_utc
		) VALUES (
			'rule_rebates', '返利付款', 1, '["important"]', 'rebatesme.com',
			'["commission","cashback"]', ?, ?
		)
	`, now, now); err != nil {
		t.Fatalf("insert second personal inbox rule: %v", err)
	}
	if _, err := store.DB.Exec(`
		INSERT INTO notification_rules (
			public_id, channel_id, name, enabled, personal_inbox, created_at_utc, updated_at_utc
		) VALUES (
			'rule_regular', (SELECT id FROM notification_channels WHERE public_id = 'channel_personal'),
			'旧个性化标记', 1, 1, ?, ?
		)
	`, now, now); err != nil {
		t.Fatalf("insert unrelated notification rule: %v", err)
	}

	items, err := service.SearchMessages(context.Background(), MessageFilter{PersonalOnly: true})
	if err != nil {
		t.Fatalf("SearchMessages(personal) error = %v", err)
	}
	matched := make(map[string]bool, len(items))
	for _, item := range items {
		matched[item.PublicID] = true
	}
	if len(items) != 3 || !matched["msg_payment"] || !matched["msg_commission"] || !matched["msg_cashback"] || matched["msg_wrong_domain"] {
		t.Fatalf("SearchMessages(personal) = %#v", items)
	}

	if _, err := store.DB.Exec(`UPDATE personal_inbox_rules SET enabled = 0`); err != nil {
		t.Fatalf("disable personal inbox rule: %v", err)
	}
	items, err = service.SearchMessages(context.Background(), MessageFilter{PersonalOnly: true})
	if err != nil {
		t.Fatalf("SearchMessages(disabled personal) error = %v", err)
	}
	if len(items) != 0 {
		t.Fatalf("SearchMessages(disabled personal) = %#v", items)
	}
}

func TestGetMessageReturnsPlainTextAndHidesTombstones(t *testing.T) {
	service, store, accountID, folders := newSyncTestService(t)
	defer store.Close()
	now := formatTime(service.now())
	if _, err := store.DB.Exec(`
		INSERT INTO messages (
			public_id, account_id, folder_id, immutable_id, subject, received_at_utc,
			body_text, body_cached_at_utc, created_at_utc, updated_at_utc
		) VALUES ('msg_detail', ?, ?, 'immutable-detail', 'Detail', ?, 'plain body', ?, ?, ?)
	`, accountID, folders[0].id, now, now, now, now); err != nil {
		t.Fatalf("insert message: %v", err)
	}
	detail, err := service.GetMessage(context.Background(), "msg_detail")
	if err != nil {
		t.Fatalf("GetMessage() error = %v", err)
	}
	if detail.BodyText != "plain body" || detail.BodyCachedAt == nil || detail.AccountPublicID != "acc_mail_test" {
		t.Fatalf("GetMessage() = %#v", detail)
	}
	if _, err := store.DB.Exec("UPDATE messages SET remote_deleted = 1 WHERE public_id = 'msg_detail'"); err != nil {
		t.Fatalf("mark tombstone: %v", err)
	}
	if _, err := service.GetMessage(context.Background(), "msg_detail"); !errors.Is(err, ErrMessageNotFound) {
		t.Fatalf("GetMessage() error = %v, want ErrMessageNotFound", err)
	}
}

func TestMarkMessageReadPersistsOnlyAfterGraphSuccess(t *testing.T) {
	service, store, accountID, folders := newSyncTestService(t)
	defer store.Close()
	now := formatTime(service.now())
	for _, message := range []struct {
		publicID    string
		immutableID string
	}{
		{publicID: "msg_read_success", immutableID: "immutable-success"},
		{publicID: "msg_read_failure", immutableID: "immutable-failure"},
	} {
		if _, err := store.DB.Exec(`
			INSERT INTO messages (
				public_id, account_id, folder_id, immutable_id, subject,
				received_at_utc, is_read, created_at_utc, updated_at_utc
			) VALUES (?, ?, ?, ?, 'Read state', ?, 0, ?, ?)
		`, message.publicID, accountID, folders[0].id, message.immutableID, now, now, now); err != nil {
			t.Fatalf("insert message: %v", err)
		}
	}

	requests := map[string]int{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests[r.URL.Path]++
		if r.URL.Path == "/me/messages/immutable-failure" {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"isRead":true}`))
	}))
	defer server.Close()
	provider := &fakeTokenProvider{leases: []accounts.AccessTokenLease{{AccessToken: "token", Version: 1}}}
	client, err := newGraphClient(server.URL, provider, server.Client(), func() time.Time { return service.now() })
	if err != nil {
		t.Fatalf("newGraphClient() error = %v", err)
	}
	client.sleep = func(context.Context, time.Duration) error { return nil }
	service.graph = client

	if err := service.MarkMessageRead(context.Background(), "msg_read_success"); err != nil {
		t.Fatalf("MarkMessageRead(success) error = %v", err)
	}
	if err := service.MarkMessageRead(context.Background(), "msg_read_success"); err != nil {
		t.Fatalf("MarkMessageRead(already read) error = %v", err)
	}
	if err := service.MarkMessageRead(context.Background(), "msg_read_failure"); err == nil {
		t.Fatal("MarkMessageRead(failure) error = nil")
	}

	var successRead, failureRead bool
	if err := store.DB.QueryRow("SELECT is_read FROM messages WHERE public_id = 'msg_read_success'").Scan(&successRead); err != nil {
		t.Fatalf("load successful read state: %v", err)
	}
	if err := store.DB.QueryRow("SELECT is_read FROM messages WHERE public_id = 'msg_read_failure'").Scan(&failureRead); err != nil {
		t.Fatalf("load failed read state: %v", err)
	}
	if !successRead || failureRead {
		t.Fatalf("read states = success:%v failure:%v", successRead, failureRead)
	}
	if requests["/me/messages/immutable-success"] != 1 || requests["/me/messages/immutable-failure"] != 3 {
		t.Fatalf("Graph requests = %#v", requests)
	}
}

func TestMarkMessagesReadUsesGraphBatches(t *testing.T) {
	service, store, accountID, folders := newSyncTestService(t)
	defer store.Close()
	now := formatTime(service.now())
	publicIDs := make([]string, 0, 45)
	for index := 0; index < 45; index++ {
		publicID := fmt.Sprintf("msg_batch_read_%02d", index)
		publicIDs = append(publicIDs, publicID)
		if _, err := store.DB.Exec(`
			INSERT INTO messages (
				public_id, account_id, folder_id, immutable_id, subject,
				received_at_utc, is_read, created_at_utc, updated_at_utc
			) VALUES (?, ?, ?, ?, 'Batch read', ?, 0, ?, ?)
		`, publicID, accountID, folders[0].id, "immutable-"+publicID, now, now, now); err != nil {
			t.Fatalf("insert message %d: %v", index, err)
		}
	}

	var batchSizes []int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/$batch" {
			t.Fatalf("request = %s %s", r.Method, r.URL.Path)
		}
		var body struct {
			Requests []graphBatchRequest `json:"requests"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decode batch request: %v", err)
		}
		batchSizes = append(batchSizes, len(body.Requests))
		responses := make([]map[string]any, 0, len(body.Requests))
		for _, request := range body.Requests {
			if strings.HasSuffix(request.URL, "/immutable-msg_batch_read_03") {
				responses = append(responses, map[string]any{
					"id": request.ID, "status": 429, "headers": map[string]string{"Retry-After": "5"},
					"body": map[string]any{"error": map[string]string{"code": "TooManyRequests"}},
				})
				continue
			}
			responses = append(responses, map[string]any{"id": request.ID, "status": 200, "headers": map[string]string{}, "body": map[string]bool{"isRead": true}})
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"responses": responses})
	}))
	defer server.Close()
	provider := &fakeTokenProvider{leases: []accounts.AccessTokenLease{{AccessToken: "token", Version: 1}}}
	client, err := newGraphClient(server.URL, provider, server.Client(), service.now)
	if err != nil {
		t.Fatalf("newGraphClient() error = %v", err)
	}
	service.graph = client

	results := service.MarkMessagesRead(context.Background(), publicIDs)
	if len(results) != 45 {
		t.Fatalf("results = %d, want 45", len(results))
	}
	for index, result := range results {
		if index == 3 {
			if result.Err == nil || result.Read {
				t.Fatalf("throttled read result = %+v", result)
			}
			continue
		}
		if result.Err != nil || !result.Read {
			t.Fatalf("read result = %+v", result)
		}
	}
	if fmt.Sprint(batchSizes) != "[20 20 5]" {
		t.Fatalf("batch sizes = %v, want [20 20 5]", batchSizes)
	}
	var unread int
	if err := store.DB.QueryRow(`SELECT COUNT(*) FROM messages WHERE public_id LIKE 'msg_batch_read_%' AND is_read = 0`).Scan(&unread); err != nil {
		t.Fatalf("count unread messages: %v", err)
	}
	if unread != 1 {
		t.Fatalf("unread messages = %d, want 1", unread)
	}
}
