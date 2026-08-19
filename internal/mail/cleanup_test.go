package mail

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"outlook-mail-manager/internal/accounts"
)

func TestProcessDueCleanupRechecksProtectionBeforeDeletedItems(t *testing.T) {
	service, store, accountID, folders := newSyncTestService(t)
	defer store.Close()
	now := service.now().UTC()
	result, err := store.DB.Exec(`
		INSERT INTO messages (
			public_id, account_id, folder_id, immutable_id, subject, sender_name, sender_address,
			received_at_utc, is_flagged, body_text, category, created_at_utc, updated_at_utc
		) VALUES ('msg_protected', ?, ?, 'immutable-protected', 'Protected message', 'Sender',
			'sender@example.com', ?, 1, 'body', 'marketing', ?, ?)
	`, accountID, folders[0].id, formatTime(now.Add(-time.Hour)), formatTime(now), formatTime(now))
	if err != nil {
		t.Fatalf("insert protected message: %v", err)
	}
	messageID, _ := result.LastInsertId()
	if _, err := store.DB.Exec(`
		INSERT INTO cleanup_actions (
			public_id, message_id, state, candidate_reason, original_folder_graph_id,
			holding_folder_graph_id, approved_at_utc, execute_after_utc, moved_at_utc,
			created_at_utc, updated_at_utc
		) VALUES ('clean_protected', ?, 'holding', 'marketing', ?, 'holding-folder', ?, ?, ?, ?, ?)
	`, messageID, folders[0].graphID, formatTime(now.Add(-15*24*time.Hour)),
		formatTime(now.Add(-time.Minute)), formatTime(now.Add(-15*24*time.Hour)), formatTime(now), formatTime(now)); err != nil {
		t.Fatalf("insert cleanup action: %v", err)
	}

	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		http.Error(w, "cleanup must not call Graph for protected mail", http.StatusInternalServerError)
	}))
	defer server.Close()
	provider := &fakeTokenProvider{leases: []accounts.AccessTokenLease{{AccessToken: "token", Version: 1}}}
	service.graph, err = newGraphClient(server.URL, provider, server.Client(), service.now)
	if err != nil {
		t.Fatalf("newGraphClient() error = %v", err)
	}

	if err := service.ProcessDueCleanup(context.Background()); err != nil {
		t.Fatalf("ProcessDueCleanup() error = %v", err)
	}
	if requests != 0 {
		t.Fatalf("Graph requests = %d, want 0", requests)
	}
	var state, lastError string
	if err := store.DB.QueryRow(`SELECT state, last_error FROM cleanup_actions WHERE public_id = 'clean_protected'`).Scan(&state, &lastError); err != nil {
		t.Fatalf("load cleanup action: %v", err)
	}
	if state != "failed" || lastError != "cleanup_protected" {
		t.Fatalf("cleanup state = %q, last_error = %q", state, lastError)
	}
	var audits int
	if err := store.DB.QueryRow(`
		SELECT COUNT(*) FROM audit_events
		WHERE event_type = 'cleanup.blocked_by_protection' AND entity_public_id = 'clean_protected'
	`).Scan(&audits); err != nil {
		t.Fatalf("count protection audits: %v", err)
	}
	if audits != 1 {
		t.Fatalf("protection audit count = %d, want 1", audits)
	}
}

func TestGetCleanupItemIsNotLimitedByListPageSize(t *testing.T) {
	service, store, accountID, folders := newSyncTestService(t)
	defer store.Close()
	now := formatTime(service.now().UTC())
	tx, err := store.DB.Begin()
	if err != nil {
		t.Fatalf("begin seed transaction: %v", err)
	}
	defer tx.Rollback()
	for index := 0; index < 501; index++ {
		result, err := tx.Exec(`
			INSERT INTO messages (
				public_id, account_id, folder_id, immutable_id, subject, sender_address,
				received_at_utc, category, created_at_utc, updated_at_utc
			) VALUES (?, ?, ?, ?, 'Candidate', 'sender@example.com', ?, 'marketing', ?, ?)
		`, fmt.Sprintf("msg_candidate_%03d", index), accountID, folders[0].id,
			fmt.Sprintf("immutable-candidate-%03d", index), now, now, now)
		if err != nil {
			t.Fatalf("insert candidate message %d: %v", index, err)
		}
		messageID, _ := result.LastInsertId()
		if _, err := tx.Exec(`
			INSERT INTO cleanup_actions (public_id, message_id, state, candidate_reason, created_at_utc, updated_at_utc)
			VALUES (?, ?, 'candidate', 'marketing', ?, ?)
		`, fmt.Sprintf("clean_candidate_%03d", index), messageID, now, now); err != nil {
			t.Fatalf("insert candidate cleanup %d: %v", index, err)
		}
	}
	result, err := tx.Exec(`
		INSERT INTO messages (
			public_id, account_id, folder_id, immutable_id, subject, sender_address,
			received_at_utc, category, created_at_utc, updated_at_utc
		) VALUES ('msg_target', ?, ?, 'immutable-target', 'Target', 'sender@example.com', ?, 'marketing', ?, ?)
	`, accountID, folders[0].id, now, now, now)
	if err != nil {
		t.Fatalf("insert target message: %v", err)
	}
	targetMessageID, _ := result.LastInsertId()
	if _, err := tx.Exec(`
		INSERT INTO cleanup_actions (public_id, message_id, state, candidate_reason, restored_at_utc, created_at_utc, updated_at_utc)
		VALUES ('clean_target', ?, 'restored', 'marketing', ?, ?, ?)
	`, targetMessageID, now, now, now); err != nil {
		t.Fatalf("insert target cleanup: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit seed transaction: %v", err)
	}

	item, err := service.getCleanupItem(context.Background(), "clean_target")
	if err != nil {
		t.Fatalf("getCleanupItem() error = %v", err)
	}
	if item.PublicID != "clean_target" || item.MessagePublicID != "msg_target" {
		t.Fatalf("cleanup item = %+v", item)
	}
}

func TestApproveCleanupBatchUsesGraphBatches(t *testing.T) {
	service, store, accountID, folders := newSyncTestService(t)
	defer store.Close()
	now := formatTime(service.now().UTC())
	if _, err := store.DB.Exec(`
		INSERT INTO cleanup_folders (account_id, graph_folder_id, display_name, created_at_utc, updated_at_utc)
		VALUES (?, 'holding-folder', ?, ?, ?)
	`, accountID, cleanupFolderName, now, now); err != nil {
		t.Fatalf("insert cleanup folder: %v", err)
	}
	publicIDs := make([]string, 0, 45)
	for index := 0; index < 45; index++ {
		messageResult, err := store.DB.Exec(`
			INSERT INTO messages (
				public_id, account_id, folder_id, immutable_id, subject, sender_address,
				received_at_utc, category, created_at_utc, updated_at_utc
			) VALUES (?, ?, ?, ?, 'Batch cleanup', 'sender@example.com', ?, 'marketing', ?, ?)
		`, fmt.Sprintf("msg_batch_cleanup_%02d", index), accountID, folders[0].id,
			fmt.Sprintf("immutable-cleanup-%02d", index), now, now, now)
		if err != nil {
			t.Fatalf("insert message %d: %v", index, err)
		}
		messageID, _ := messageResult.LastInsertId()
		publicID := fmt.Sprintf("clean_batch_%02d", index)
		publicIDs = append(publicIDs, publicID)
		if _, err := store.DB.Exec(`
			INSERT INTO cleanup_actions (public_id, message_id, state, candidate_reason, created_at_utc, updated_at_utc)
			VALUES (?, ?, 'candidate', 'marketing', ?, ?)
		`, publicID, messageID, now, now); err != nil {
			t.Fatalf("insert cleanup action %d: %v", index, err)
		}
	}

	var batchSizes []int
	batchIndex := 0
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
			if strings.HasSuffix(request.URL, "/immutable-cleanup-03/move") {
				responses = append(responses, map[string]any{
					"id": request.ID, "status": 429, "headers": map[string]string{"Retry-After": "5"},
					"body": map[string]any{"error": map[string]string{"code": "TooManyRequests"}},
				})
				continue
			}
			responses = append(responses, map[string]any{
				"id": request.ID, "status": 201, "headers": map[string]string{},
				"body": map[string]string{"id": fmt.Sprintf("moved-%d-%s", batchIndex, request.ID)},
			})
		}
		batchIndex++
		_ = json.NewEncoder(w).Encode(map[string]any{"responses": responses})
	}))
	defer server.Close()
	provider := &fakeTokenProvider{leases: []accounts.AccessTokenLease{{AccessToken: "token", Version: 1}}}
	client, err := newGraphClient(server.URL, provider, server.Client(), service.now)
	if err != nil {
		t.Fatalf("newGraphClient() error = %v", err)
	}
	service.graph = client

	results := service.ApproveCleanupBatch(context.Background(), publicIDs)
	if len(results) != 45 {
		t.Fatalf("results = %d, want 45", len(results))
	}
	for index, result := range results {
		if index == 3 {
			if result.Err == nil || result.Item != nil {
				t.Fatalf("throttled cleanup result = %+v", result)
			}
			continue
		}
		if result.Err != nil || result.Item == nil || result.Item.State != "holding" {
			t.Fatalf("cleanup result = %+v", result)
		}
	}
	if fmt.Sprint(batchSizes) != "[20 20 5]" {
		t.Fatalf("batch sizes = %v, want [20 20 5]", batchSizes)
	}
	var holding int
	if err := store.DB.QueryRow(`SELECT COUNT(*) FROM cleanup_actions WHERE public_id LIKE 'clean_batch_%' AND state = 'holding'`).Scan(&holding); err != nil {
		t.Fatalf("count holding actions: %v", err)
	}
	if holding != 44 {
		t.Fatalf("holding actions = %d, want 44", holding)
	}
	var candidates int
	if err := store.DB.QueryRow(`SELECT COUNT(*) FROM cleanup_actions WHERE public_id LIKE 'clean_batch_%' AND state = 'candidate'`).Scan(&candidates); err != nil {
		t.Fatalf("count candidate actions: %v", err)
	}
	if candidates != 1 {
		t.Fatalf("candidate actions = %d, want 1", candidates)
	}
}
