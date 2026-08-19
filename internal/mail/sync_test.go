package mail

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"outlook-mail-manager/internal/accounts"
	"outlook-mail-manager/internal/database"
)

func TestSyncFolderDoesNotAdvanceCursorWhenLaterPageFails(t *testing.T) {
	ctx := context.Background()
	service, store, accountID, folders := newSyncTestService(t)
	defer store.Close()

	requests := 0
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		switch r.URL.Path {
		case "/page-one":
			fmt.Fprintf(w, `{"value":[{"id":"immutable-1","subject":"First page","receivedDateTime":"2026-08-17T10:00:00Z","body":{"contentType":"text","content":"saved body"}}],"@odata.nextLink":%q}`, serverURL(r)+"page-two")
		case "/page-two":
			w.WriteHeader(http.StatusInternalServerError)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	provider := &fakeTokenProvider{leases: []accounts.AccessTokenLease{{AccessToken: "token", Version: 1}}}
	client, err := newGraphClient(server.URL, provider, server.Client(), service.now)
	if err != nil {
		t.Fatalf("newGraphClient() error = %v", err)
	}
	client.sleep = func(context.Context, time.Duration) error { return nil }
	service.graph = client
	folder := folders[0]
	folder.deltaLink = server.URL + "/page-one"
	if _, err := store.DB.Exec("UPDATE sync_cursors SET delta_link = ? WHERE folder_id = ?", folder.deltaLink, folder.id); err != nil {
		t.Fatalf("set cursor: %v", err)
	}

	if err := service.syncFolder(ctx, accountID, folder); err == nil {
		t.Fatal("syncFolder() error = nil")
	}
	var cursor string
	if err := store.DB.QueryRow("SELECT delta_link FROM sync_cursors WHERE folder_id = ?", folder.id).Scan(&cursor); err != nil {
		t.Fatalf("load cursor: %v", err)
	}
	if cursor != folder.deltaLink {
		t.Fatalf("cursor = %q, want %q", cursor, folder.deltaLink)
	}
	var count int
	if err := store.DB.QueryRow("SELECT COUNT(*) FROM messages WHERE immutable_id = 'immutable-1'").Scan(&count); err != nil || count != 1 {
		t.Fatalf("saved message count = %d, error = %v", count, err)
	}
	if requests != 4 {
		t.Fatalf("requests = %d, want 4 including bounded 5xx retries", requests)
	}
}

func TestExpiredCursorRebuildsOnlyAffectedFolder(t *testing.T) {
	ctx := context.Background()
	service, store, accountID, folders := newSyncTestService(t)
	defer store.Close()

	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/expired":
			w.WriteHeader(http.StatusGone)
		case "/me/mailFolders/inbox/messages/delta":
			fmt.Fprintf(w, `{"value":[],"@odata.deltaLink":%q}`, serverURL(r)+"inbox-final")
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	provider := &fakeTokenProvider{leases: []accounts.AccessTokenLease{{AccessToken: "token", Version: 1}}}
	client, err := newGraphClient(server.URL, provider, server.Client(), service.now)
	if err != nil {
		t.Fatalf("newGraphClient() error = %v", err)
	}
	service.graph = client
	inbox, junk := folders[0], folders[1]
	inbox.deltaLink = server.URL + "/expired"
	junk.deltaLink = server.URL + "/junk-stays"
	if _, err := store.DB.Exec("UPDATE sync_cursors SET delta_link = ? WHERE folder_id = ?", inbox.deltaLink, inbox.id); err != nil {
		t.Fatalf("set inbox cursor: %v", err)
	}
	if _, err := store.DB.Exec("UPDATE sync_cursors SET delta_link = ? WHERE folder_id = ?", junk.deltaLink, junk.id); err != nil {
		t.Fatalf("set junk cursor: %v", err)
	}

	if err := service.syncFolder(ctx, accountID, inbox); err != nil {
		t.Fatalf("syncFolder() error = %v", err)
	}
	var inboxCursor, junkCursor string
	if err := store.DB.QueryRow("SELECT delta_link FROM sync_cursors WHERE folder_id = ?", inbox.id).Scan(&inboxCursor); err != nil {
		t.Fatalf("load inbox cursor: %v", err)
	}
	if err := store.DB.QueryRow("SELECT delta_link FROM sync_cursors WHERE folder_id = ?", junk.id).Scan(&junkCursor); err != nil {
		t.Fatalf("load junk cursor: %v", err)
	}
	if inboxCursor != server.URL+"/inbox-final" {
		t.Fatalf("inbox cursor = %q", inboxCursor)
	}
	if junkCursor != junk.deltaLink {
		t.Fatalf("junk cursor = %q, want unchanged", junkCursor)
	}
}

func TestSyncFolderHydratesPartialDeltaMessage(t *testing.T) {
	ctx := context.Background()
	service, store, accountID, folders := newSyncTestService(t)
	defer store.Close()

	message := graphMessage{
		ID: "immutable-partial", Subject: "PayPal payment received",
		ReceivedDateTime: "2026-08-17T10:00:00Z",
	}
	message.Body.ContentType = "text"
	message.Body.Content = "Original body"
	if err := service.applyPage(ctx, accountID, folders[0], []graphMessage{message}, false, ""); err != nil {
		t.Fatalf("seed message: %v", err)
	}

	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/partial":
			fmt.Fprintf(w, `{"value":[{"id":"immutable-partial","isRead":true}],"@odata.deltaLink":%q}`, serverURL(r)+"done")
		case "/me/messages/immutable-partial":
			_, _ = w.Write([]byte(`{"id":"immutable-partial","subject":"PayPal payment received","receivedDateTime":"2026-08-17T10:00:00Z","isRead":true,"body":{"contentType":"text","content":"Updated body"}}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	provider := &fakeTokenProvider{leases: []accounts.AccessTokenLease{{AccessToken: "token", Version: 1}}}
	client, err := newGraphClient(server.URL, provider, server.Client(), service.now)
	if err != nil {
		t.Fatalf("newGraphClient() error = %v", err)
	}
	service.graph = client
	folder := folders[0]
	folder.deltaLink = server.URL + "/partial"
	if _, err := store.DB.Exec("UPDATE sync_cursors SET delta_link = ? WHERE folder_id = ?", folder.deltaLink, folder.id); err != nil {
		t.Fatalf("set cursor: %v", err)
	}

	if err := service.syncFolder(ctx, accountID, folder); err != nil {
		t.Fatalf("syncFolder() error = %v", err)
	}
	var subject, body, cursor string
	var isRead bool
	if err := store.DB.QueryRow(`SELECT subject, body_text, is_read FROM messages WHERE immutable_id = 'immutable-partial'`).Scan(&subject, &body, &isRead); err != nil {
		t.Fatalf("load hydrated message: %v", err)
	}
	if subject != "PayPal payment received" || body != "Updated body" || !isRead {
		t.Fatalf("hydrated message = subject:%q body:%q read:%v", subject, body, isRead)
	}
	if err := store.DB.QueryRow("SELECT delta_link FROM sync_cursors WHERE folder_id = ?", folder.id).Scan(&cursor); err != nil {
		t.Fatalf("load cursor: %v", err)
	}
	if cursor != server.URL+"/done" {
		t.Fatalf("cursor = %q", cursor)
	}
}

func TestApplyPageMetadataOnlyAndFolderMove(t *testing.T) {
	ctx := context.Background()
	service, store, accountID, folders := newSyncTestService(t)
	defer store.Close()

	message := graphMessage{ID: "immutable-move", Subject: "Verification code", ReceivedDateTime: "2026-08-17T10:00:00Z"}
	message.Body.ContentType = "text"
	message.Body.Content = "secret body 123456"
	if err := service.applyPage(ctx, accountID, folders[0], []graphMessage{message}, true, ""); err != nil {
		t.Fatalf("apply metadata page: %v", err)
	}
	var publicID string
	var body sql.NullString
	if err := store.DB.QueryRow("SELECT public_id, body_text FROM messages WHERE immutable_id = ?", message.ID).Scan(&publicID, &body); err != nil {
		t.Fatalf("load metadata message: %v", err)
	}
	if body.Valid {
		t.Fatalf("metadata-only body = %q, want NULL", body.String)
	}

	removed := graphMessage{ID: message.ID, Removed: &struct {
		Reason string `json:"reason"`
	}{Reason: "changed"}}
	if err := service.applyPage(ctx, accountID, folders[0], []graphMessage{removed}, false, ""); err != nil {
		t.Fatalf("apply tombstone: %v", err)
	}
	message.Body.Content = "body restored in junk"
	if err := service.applyPage(ctx, accountID, folders[1], []graphMessage{message}, false, ""); err != nil {
		t.Fatalf("apply moved message: %v", err)
	}
	var movedPublicID, movedBody string
	var folderID int64
	var deleted bool
	if err := store.DB.QueryRow(`
		SELECT public_id, folder_id, body_text, remote_deleted FROM messages WHERE immutable_id = ?
	`, message.ID).Scan(&movedPublicID, &folderID, &movedBody, &deleted); err != nil {
		t.Fatalf("load moved message: %v", err)
	}
	if movedPublicID != publicID || folderID != folders[1].id || movedBody != message.Body.Content || deleted {
		t.Fatalf("moved message = public:%q folder:%d body:%q deleted:%v", movedPublicID, folderID, movedBody, deleted)
	}
}

func TestRescueVerificationMessageMovesJunkMailToInbox(t *testing.T) {
	service, store, accountID, folders := newSyncTestService(t)
	defer store.Close()
	now := formatTime(service.now())
	if _, err := store.DB.Exec(`
		INSERT INTO messages (
			public_id, account_id, folder_id, immutable_id, subject, received_at_utc,
			category, classification_reason, created_at_utc, updated_at_utc
		) VALUES ('msg_verification', ?, ?, 'immutable-code', 'RebatesMe verification code', ?,
			'verification', '检测到验证码主题', ?, ?)
	`, accountID, folders[1].id, now, now, now); err != nil {
		t.Fatalf("insert verification message: %v", err)
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/me/messages/immutable-code/move" || r.Method != http.MethodPost {
			http.NotFound(w, r)
			return
		}
		var body map[string]string
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, "invalid request", http.StatusBadRequest)
			return
		}
		if body["destinationId"] != "inbox" {
			http.Error(w, "unexpected destination", http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"immutable-code-moved"}`))
	}))
	defer server.Close()
	provider := &fakeTokenProvider{leases: []accounts.AccessTokenLease{{AccessToken: "token", Version: 1}}}
	client, err := newGraphClient(server.URL, provider, server.Client(), service.now)
	if err != nil {
		t.Fatalf("newGraphClient() error = %v", err)
	}
	service.graph = client

	if err := service.rescueVerificationMessages(context.Background(), accountID); err != nil {
		t.Fatalf("rescueVerificationMessages() error = %v", err)
	}
	var immutableID string
	var folderID int64
	if err := store.DB.QueryRow(`
		SELECT immutable_id, folder_id FROM messages WHERE public_id = 'msg_verification'
	`).Scan(&immutableID, &folderID); err != nil {
		t.Fatalf("load rescued message: %v", err)
	}
	if immutableID != "immutable-code-moved" || folderID != folders[0].id {
		t.Fatalf("rescued message = immutable:%q folder:%d", immutableID, folderID)
	}
	var auditCount int
	if err := store.DB.QueryRow(`
		SELECT COUNT(*) FROM audit_events
		WHERE event_type = 'verification.moved_to_inbox' AND entity_public_id = 'msg_verification'
	`).Scan(&auditCount); err != nil || auditCount != 1 {
		t.Fatalf("audit count = %d, error = %v", auditCount, err)
	}
}

func TestRescueVerificationMessageKeepsJunkStateWhenGraphFails(t *testing.T) {
	service, store, accountID, folders := newSyncTestService(t)
	defer store.Close()
	now := formatTime(service.now())
	if _, err := store.DB.Exec(`
		INSERT INTO messages (
			public_id, account_id, folder_id, immutable_id, subject, received_at_utc,
			category, classification_reason, created_at_utc, updated_at_utc
		) VALUES ('msg_verification_failed', ?, ?, 'immutable-code-failed', 'Verification code', ?,
			'verification', '检测到验证码主题', ?, ?)
	`, accountID, folders[1].id, now, now, now); err != nil {
		t.Fatalf("insert verification message: %v", err)
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()
	provider := &fakeTokenProvider{leases: []accounts.AccessTokenLease{{AccessToken: "token", Version: 1}}}
	client, err := newGraphClient(server.URL, provider, server.Client(), service.now)
	if err != nil {
		t.Fatalf("newGraphClient() error = %v", err)
	}
	client.sleep = func(context.Context, time.Duration) error { return nil }
	service.graph = client

	if err := service.rescueVerificationMessages(context.Background(), accountID); err == nil {
		t.Fatal("rescueVerificationMessages() error = nil")
	}
	var immutableID string
	var folderID int64
	if err := store.DB.QueryRow(`
		SELECT immutable_id, folder_id FROM messages WHERE public_id = 'msg_verification_failed'
	`).Scan(&immutableID, &folderID); err != nil {
		t.Fatalf("load failed rescue message: %v", err)
	}
	if immutableID != "immutable-code-failed" || folderID != folders[1].id {
		t.Fatalf("failed rescue message = immutable:%q folder:%d", immutableID, folderID)
	}
	var auditCount int
	if err := store.DB.QueryRow(`
		SELECT COUNT(*) FROM audit_events WHERE entity_public_id = 'msg_verification_failed'
	`).Scan(&auditCount); err != nil || auditCount != 0 {
		t.Fatalf("audit count = %d, error = %v", auditCount, err)
	}
}

func TestPlainTextTruncationAndDiskThresholds(t *testing.T) {
	plain, err := plainTextBody("html", `<html><head><style>hide</style></head><body><p>Hello <strong>world</strong></p><script>bad()</script><div>第二行</div></body></html>`)
	if err != nil {
		t.Fatalf("plainTextBody() error = %v", err)
	}
	if strings.Contains(plain, "hide") || strings.Contains(plain, "bad") || !strings.Contains(plain, "Hello world") || !strings.Contains(plain, "第二行") {
		t.Fatalf("plain text = %q", plain)
	}
	value := strings.Repeat("界", maxBodyBytes)
	truncated, wasTruncated := truncateUTF8(value, maxBodyBytes)
	if !wasTruncated || len(truncated) > maxBodyBytes || !utf8.ValidString(truncated) {
		t.Fatalf("truncateUTF8() bytes=%d truncated=%v valid=%v", len(truncated), wasTruncated, utf8.ValidString(truncated))
	}
	now := time.Date(2026, 8, 17, 12, 0, 0, 0, time.UTC)
	for _, test := range []struct {
		used         int
		level        string
		metadataOnly bool
	}{
		{69, "normal", false}, {70, "warning", false}, {85, "critical", false}, {90, "metadata_only", true},
	} {
		state := classifyDisk(test.used, now)
		if state.Level != test.level || state.MetadataOnly != test.metadataOnly {
			t.Fatalf("classifyDisk(%d) = %#v", test.used, state)
		}
	}
}

func TestCanceledSyncDoesNotDegradeAccount(t *testing.T) {
	service, store, accountID, folders := newSyncTestService(t)
	defer store.Close()

	service.recordSyncFailure(context.Background(), accountID, folders[0].id, context.Canceled)
	var status string
	var failures int
	if err := store.DB.QueryRow(
		"SELECT status, sync_failures FROM accounts WHERE id = ?", accountID,
	).Scan(&status, &failures); err != nil {
		t.Fatalf("load account status: %v", err)
	}
	if status != "active" || failures != 0 {
		t.Fatalf("status = %q, failures = %d", status, failures)
	}
}

func TestSuccessfulSyncRestoresDegradedAccount(t *testing.T) {
	ctx := context.Background()
	service, store, accountID, folders := newSyncTestService(t)
	defer store.Close()

	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/inbox", "/junk":
			fmt.Fprintf(w, `{"value":[],"@odata.deltaLink":%q}`, serverURL(r)+strings.TrimPrefix(r.URL.Path, "/")+"-done")
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	provider := &fakeTokenProvider{leases: []accounts.AccessTokenLease{{AccessToken: "token", Version: 1}}}
	client, err := newGraphClient(server.URL, provider, server.Client(), service.now)
	if err != nil {
		t.Fatalf("newGraphClient() error = %v", err)
	}
	service.graph = client

	if _, err := store.DB.Exec(`
		UPDATE accounts SET status = 'degraded', last_sync_error = 'graph_message_invalid',
			sync_failures = 12, sync_next_retry_at_utc = ?, sync_backlog = 1
		WHERE id = ?
	`, formatTime(service.now().Add(time.Hour)), accountID); err != nil {
		t.Fatalf("degrade account: %v", err)
	}
	for index, folder := range folders {
		path := "/inbox"
		if index == 1 {
			path = "/junk"
		}
		if _, err := store.DB.Exec(`
			UPDATE sync_cursors SET delta_link = ? WHERE folder_id = ?
		`, server.URL+path, folder.id); err != nil {
			t.Fatalf("set folder cursor: %v", err)
		}
		if _, err := store.DB.Exec(`
			UPDATE folders SET sync_status = 'error', last_error = 'graph_message_invalid' WHERE id = ?
		`, folder.id); err != nil {
			t.Fatalf("set folder failure: %v", err)
		}
	}

	if err := service.syncAccount(ctx, accountID); err != nil {
		t.Fatalf("syncAccount() error = %v", err)
	}
	var status string
	var lastError, retry sql.NullString
	var failures, backlog int
	if err := store.DB.QueryRow(`
		SELECT status, last_sync_error, sync_failures, sync_next_retry_at_utc, sync_backlog
		FROM accounts WHERE id = ?
	`, accountID).Scan(&status, &lastError, &failures, &retry, &backlog); err != nil {
		t.Fatalf("load recovered account: %v", err)
	}
	if status != "active" || lastError.Valid || failures != 0 || retry.Valid || backlog != 0 {
		t.Fatalf("recovered account = status:%q error:%v failures:%d retry:%v backlog:%d", status, lastError, failures, retry, backlog)
	}
	var inactiveFolders int
	if err := store.DB.QueryRow(`
		SELECT COUNT(*) FROM folders WHERE account_id = ? AND (sync_status != 'active' OR last_error IS NOT NULL)
	`, accountID).Scan(&inactiveFolders); err != nil {
		t.Fatalf("load recovered folders: %v", err)
	}
	if inactiveFolders != 0 {
		t.Fatalf("inactive folders after successful sync = %d", inactiveFolders)
	}
}

func TestSyncErrorCodeKeepsAuthorizationAndSyncFailuresDistinct(t *testing.T) {
	for _, test := range []struct {
		err  error
		want string
	}{
		{context.DeadlineExceeded, "sync_timeout"},
		{fmt.Errorf("decode Microsoft Graph response: unexpected EOF"), "graph_response_invalid"},
		{fmt.Errorf("request Microsoft Graph: connection reset"), "graph_network"},
		{fmt.Errorf("save message: SQLITE_BUSY: database is locked"), "database_busy"},
	} {
		if got := syncErrorCode(test.err); got != test.want {
			t.Fatalf("syncErrorCode(%v) = %q, want %q", test.err, got, test.want)
		}
	}
}

func newSyncTestService(t *testing.T) (*Service, *database.Store, int64, []folderRecord) {
	t.Helper()
	store, err := database.Open(context.Background(), t.TempDir())
	if err != nil {
		t.Fatalf("database.Open() error = %v", err)
	}
	now := time.Date(2026, 8, 17, 12, 0, 0, 0, time.UTC)
	result, err := store.DB.Exec(`
		INSERT INTO accounts (public_id, imported_email, status, created_at_utc, updated_at_utc)
		VALUES ('acc_mail_test', 'mail-test@outlook.com', 'active', ?, ?)
	`, formatTime(now), formatTime(now))
	if err != nil {
		store.Close()
		t.Fatalf("insert account: %v", err)
	}
	accountID, _ := result.LastInsertId()
	service := &Service{
		db: store.DB, dataDir: t.TempDir(), now: func() time.Time { return now }, random: rand.Reader,
		diskUsage: func(string) (int, error) { return 50, nil },
	}
	folders, err := service.ensureFolders(context.Background(), accountID)
	if err != nil {
		store.Close()
		t.Fatalf("ensureFolders() error = %v", err)
	}
	return service, store, accountID, folders
}

func serverURL(r *http.Request) string {
	return "http://" + r.Host + "/"
}
