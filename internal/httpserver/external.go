package httpserver

import (
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"outlook-mail-manager/internal/apitoken"
	mailbox "outlook-mail-manager/internal/mail"
)

type externalAPI struct {
	db          *sql.DB
	tokens      apiTokenService
	mail        mailService
	maintenance maintenanceService
	logger      *slog.Logger
	limiter     *apiRateLimiter
}

type externalAccount struct {
	PublicID     string     `json:"public_id"`
	Name         string     `json:"name"`
	Address      string     `json:"address"`
	Status       string     `json:"status"`
	Groups       []string   `json:"groups"`
	Tags         []string   `json:"tags"`
	LastSyncedAt *time.Time `json:"last_synced_at,omitempty"`
	ReauthReason string     `json:"reauth_reason,omitempty"`
}

type messageCursor struct {
	ReceivedAt string `json:"received_at"`
	PublicID   string `json:"public_id"`
}

func newExternalAPI(db *sql.DB, tokens apiTokenService, mail mailService, maintenance maintenanceService, logger *slog.Logger) *externalAPI {
	return &externalAPI{db: db, tokens: tokens, mail: mail, maintenance: maintenance, logger: logger, limiter: newAPIRateLimiter()}
}

func (api *externalAPI) register(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/v1/accounts", api.accounts)
	mux.HandleFunc("GET /api/v1/messages", api.messages)
	mux.HandleFunc("GET /api/v1/messages/{public_id}", api.message)
	mux.HandleFunc("GET /api/v1/otp/latest", api.latestOTP)
	mux.HandleFunc("GET /api/v1/health", api.health)
}

func (api *externalAPI) accounts(w http.ResponseWriter, r *http.Request) {
	grant, ok := api.authorize(w, r, "accounts:read", 60)
	if !ok {
		return
	}
	allowed, err := api.allowedAccounts(r.Context(), grant)
	if err != nil {
		api.internalError(w, err)
		return
	}
	items := make([]externalAccount, 0, len(allowed))
	for _, publicID := range allowed {
		var item externalAccount
		var synced, reason sql.NullString
		err := api.db.QueryRowContext(r.Context(), `
			SELECT public_id,
				COALESCE(NULLIF(display_name, ''), NULLIF(primary_email, ''), imported_email),
				COALESCE(NULLIF(primary_email, ''), imported_email), status,
				last_sync_success_at_utc, reauth_reason
			FROM accounts WHERE public_id = ?
		`, publicID).Scan(&item.PublicID, &item.Name, &item.Address, &item.Status, &synced, &reason)
		if err != nil {
			api.internalError(w, err)
			return
		}
		item.Groups, _ = api.accountLabels(r.Context(), publicID, true)
		item.Tags, _ = api.accountLabels(r.Context(), publicID, false)
		item.LastSyncedAt, err = parseOptionalTime(synced)
		if err != nil {
			api.internalError(w, err)
			return
		}
		item.ReauthReason = reason.String
		items = append(items, item)
	}
	writeExternalJSON(w, http.StatusOK, map[string]any{"accounts": items})
}

func (api *externalAPI) messages(w http.ResponseWriter, r *http.Request) {
	grant, ok := api.authorize(w, r, "mail:read", 60)
	if !ok {
		return
	}
	allowed, err := api.allowedAccounts(r.Context(), grant)
	if err != nil {
		api.internalError(w, err)
		return
	}
	if len(allowed) == 0 {
		writeExternalJSON(w, http.StatusOK, map[string]any{"messages": []mailbox.MessageSummary{}, "next_cursor": ""})
		return
	}
	filter := mailbox.MessageFilter{
		Query: strings.TrimSpace(r.URL.Query().Get("q")), Group: strings.TrimSpace(r.URL.Query().Get("group")),
		Tag: strings.TrimSpace(r.URL.Query().Get("tag")), Category: mailbox.Category(strings.TrimSpace(r.URL.Query().Get("category"))),
		Folder: strings.TrimSpace(r.URL.Query().Get("folder")), Sender: strings.TrimSpace(r.URL.Query().Get("sender")),
		AccountScope: allowed,
	}
	if account := strings.TrimSpace(r.URL.Query().Get("account")); account != "" {
		var accountPublicID string
		err := api.db.QueryRowContext(r.Context(), `
			SELECT public_id FROM accounts
			WHERE public_id = ? OR imported_email = ? COLLATE NOCASE OR primary_email = ? COLLATE NOCASE
			LIMIT 1
		`, account, account, account).Scan(&accountPublicID)
		if errors.Is(err, sql.ErrNoRows) {
			writeExternalError(w, http.StatusNotFound, "not_found", "资源不存在")
			return
		}
		if err != nil {
			api.internalError(w, err)
			return
		}
		permitted, err := grant.AllowsAccount(r.Context(), api.db, accountPublicID)
		if err != nil {
			api.internalError(w, err)
			return
		}
		if !permitted {
			writeExternalError(w, http.StatusNotFound, "not_found", "资源不存在")
			return
		}
		filter.Account = accountPublicID
	}
	if value := strings.TrimSpace(r.URL.Query().Get("unread")); value != "" {
		parsed, err := strconv.ParseBool(value)
		if err != nil {
			writeExternalError(w, http.StatusBadRequest, "invalid_unread", "unread 必须是 true 或 false")
			return
		}
		filter.Unread = &parsed
	}
	limit := 50
	if value := strings.TrimSpace(r.URL.Query().Get("limit")); value != "" {
		parsed, err := strconv.Atoi(value)
		if err != nil || parsed < 1 || parsed > 100 {
			writeExternalError(w, http.StatusBadRequest, "invalid_limit", "limit 必须在 1 到 100 之间")
			return
		}
		limit = parsed
	}
	if cursor := strings.TrimSpace(r.URL.Query().Get("cursor")); cursor != "" {
		value, err := decodeMessageCursor(cursor)
		if err != nil {
			writeExternalError(w, http.StatusBadRequest, "invalid_cursor", "cursor 无效")
			return
		}
		filter.CursorReceivedAt = &value.ReceivedAt
		filter.CursorPublicID = value.PublicID
	}
	filter.Limit = limit + 1
	items, err := api.mail.SearchMessages(r.Context(), filter)
	if err != nil {
		api.internalError(w, err)
		return
	}
	nextCursor := ""
	if len(items) > limit {
		items = items[:limit]
		last := items[len(items)-1]
		nextCursor = encodeMessageCursor(last.ReceivedAt, last.PublicID)
	}
	writeExternalJSON(w, http.StatusOK, map[string]any{"messages": items, "next_cursor": nextCursor})
}

func (api *externalAPI) message(w http.ResponseWriter, r *http.Request) {
	grant, ok := api.authorize(w, r, "mail:read", 60)
	if !ok {
		return
	}
	item, err := api.mail.GetMessage(r.Context(), r.PathValue("public_id"))
	if err != nil {
		writeExternalError(w, http.StatusNotFound, "not_found", "资源不存在")
		return
	}
	allowed, err := grant.AllowsAccount(r.Context(), api.db, item.AccountPublicID)
	if err != nil {
		api.internalError(w, err)
		return
	}
	if !allowed {
		writeExternalError(w, http.StatusNotFound, "not_found", "资源不存在")
		return
	}
	writeExternalJSON(w, http.StatusOK, item)
}

func (api *externalAPI) latestOTP(w http.ResponseWriter, r *http.Request) {
	requestStartedAt := time.Now().UTC()
	grant, ok := api.authorize(w, r, "otp:read", 20)
	if !ok {
		return
	}
	waitSeconds := 0
	var err error
	if value := strings.TrimSpace(r.URL.Query().Get("wait_seconds")); value != "" {
		waitSeconds, err = strconv.Atoi(value)
		if err != nil || waitSeconds < 0 || waitSeconds > 30 {
			writeExternalError(w, http.StatusBadRequest, "invalid_wait", "wait_seconds 必须在 0 到 30 之间")
			return
		}
	}
	accountLookup := strings.TrimSpace(r.URL.Query().Get("account"))
	if accountLookup == "" {
		writeExternalError(w, http.StatusBadRequest, "account_required", "account 为必填项")
		return
	}
	var accountPublicID, accountStatus string
	var synced sql.NullString
	err = api.db.QueryRowContext(r.Context(), `
		SELECT public_id, status, last_sync_success_at_utc FROM accounts
		WHERE public_id = ? OR imported_email = ? COLLATE NOCASE OR primary_email = ? COLLATE NOCASE
		LIMIT 1
	`, accountLookup, accountLookup, accountLookup).Scan(&accountPublicID, &accountStatus, &synced)
	if err != nil {
		writeExternalError(w, http.StatusNotFound, "not_found", "资源不存在")
		return
	}
	allowed, err := grant.AllowsAccount(r.Context(), api.db, accountPublicID)
	if err != nil {
		api.internalError(w, err)
		return
	}
	if !allowed {
		writeExternalError(w, http.StatusNotFound, "not_found", "资源不存在")
		return
	}
	syncSucceeded := false
	if waitSeconds > 0 {
		ctx, cancel := context.WithTimeout(r.Context(), time.Duration(waitSeconds)*time.Second)
		err = api.mail.SyncAccount(ctx, accountPublicID)
		cancel()
		syncSucceeded = err == nil
		_ = api.db.QueryRowContext(r.Context(), `SELECT last_sync_success_at_utc, status FROM accounts WHERE public_id = ?`,
			accountPublicID).Scan(&synced, &accountStatus)
	}
	query := `
		SELECT m.public_id, m.verification_code, m.received_at_utc
		FROM messages m JOIN accounts a ON a.id = m.account_id
		WHERE a.public_id = ? AND m.verification_code IS NOT NULL AND m.received_at_utc > ? AND m.received_at_utc <= ?
			AND m.remote_deleted = 0 AND m.hidden_from_inbox = 0
	`
	args := []any{accountPublicID, requestStartedAt.Add(-15 * time.Minute).Format(time.RFC3339Nano), requestStartedAt.Format(time.RFC3339Nano)}
	if sender := strings.TrimSpace(r.URL.Query().Get("sender")); sender != "" {
		query += " AND m.sender_address = ? COLLATE NOCASE"
		args = append(args, sender)
	}
	if subject := strings.TrimSpace(r.URL.Query().Get("subject")); subject != "" {
		query += " AND m.subject LIKE ? ESCAPE '\\'"
		args = append(args, "%"+escapeExternalLike(subject)+"%")
	}
	query += " ORDER BY m.received_at_utc DESC, m.public_id DESC LIMIT 1"
	var messagePublicID, code, received string
	err = api.db.QueryRowContext(r.Context(), query, args...).Scan(&messagePublicID, &code, &received)
	response := map[string]any{
		"code": nil, "message_public_id": nil, "received_at": nil, "synced_at": nil,
		"fresh": false, "account_status": accountStatus,
	}
	if synced.Valid {
		response["synced_at"] = synced.String
	}
	if err == nil {
		response["code"] = code
		response["message_public_id"] = messagePublicID
		response["received_at"] = received
		response["fresh"] = syncSucceeded
	} else if !errors.Is(err, sql.ErrNoRows) {
		api.internalError(w, err)
		return
	}
	if waitSeconds > 0 && !syncSucceeded {
		response["retry_after_seconds"] = 10
	}
	writeExternalJSON(w, http.StatusOK, response)
}

func (api *externalAPI) health(w http.ResponseWriter, r *http.Request) {
	_, ok := api.authorize(w, r, "system:read", 30)
	if !ok {
		return
	}
	maintenanceStatus, err := api.maintenance.Status(r.Context())
	if err != nil {
		api.internalError(w, err)
		return
	}
	mailStatus, err := api.mail.Status(r.Context())
	if err != nil {
		api.internalError(w, err)
		return
	}
	rows, err := api.db.QueryContext(r.Context(), `SELECT status, COUNT(*) FROM accounts GROUP BY status`)
	if err != nil {
		api.internalError(w, err)
		return
	}
	accountStates := map[string]int{}
	for rows.Next() {
		var status string
		var count int
		if err := rows.Scan(&status, &count); err != nil {
			rows.Close()
			api.internalError(w, err)
			return
		}
		accountStates[status] = count
	}
	rows.Close()
	writeExternalJSON(w, http.StatusOK, map[string]any{
		"database_ok": maintenanceStatus.DatabaseOK, "schema_version": maintenanceStatus.SchemaVersion,
		"backup_count": maintenanceStatus.BackupCount, "latest_backup": maintenanceStatus.LatestBackup,
		"failed_notifications": maintenanceStatus.FailedNotifications, "cleanup_failures": maintenanceStatus.CleanupFailures,
		"disk": mailStatus.Disk, "queues": map[string]int{"high_priority": mailStatus.HighPriority, "background": mailStatus.Background},
		"accounts": accountStates, "checked_at": maintenanceStatus.CheckedAt,
	})
}

func (api *externalAPI) authorize(w http.ResponseWriter, r *http.Request, scope string, limit int) (apitoken.Grant, bool) {
	header := strings.TrimSpace(r.Header.Get("Authorization"))
	secret := ""
	if header != "" {
		if !strings.HasPrefix(header, "Bearer ") {
			writeExternalError(w, http.StatusUnauthorized, "unauthorized", "访问凭据无效")
			return apitoken.Grant{}, false
		}
		secret = strings.TrimSpace(strings.TrimPrefix(header, "Bearer "))
	} else if r.Method == http.MethodGet {
		secret = strings.TrimSpace(r.URL.Query().Get("access_token"))
	}
	if secret == "" {
		writeExternalError(w, http.StatusUnauthorized, "unauthorized", "访问凭据无效")
		return apitoken.Grant{}, false
	}
	grant, err := api.tokens.Verify(r.Context(), secret, clientIP(r), scope)
	if err != nil {
		writeExternalError(w, http.StatusUnauthorized, "unauthorized", "访问凭据无效")
		return apitoken.Grant{}, false
	}
	if !api.limiter.Allow(grant.TokenPublicID+":"+clientIP(r), limit, time.Minute) {
		w.Header().Set("Retry-After", "60")
		writeExternalError(w, http.StatusTooManyRequests, "rate_limited", "请求过于频繁")
		return apitoken.Grant{}, false
	}
	return grant, true
}

func (api *externalAPI) allowedAccounts(ctx context.Context, grant apitoken.Grant) ([]string, error) {
	rows, err := api.db.QueryContext(ctx, `SELECT public_id FROM accounts ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]string, 0)
	for rows.Next() {
		var publicID string
		if err := rows.Scan(&publicID); err != nil {
			return nil, err
		}
		allowed, err := grant.AllowsAccount(ctx, api.db, publicID)
		if err != nil {
			return nil, err
		}
		if allowed {
			items = append(items, publicID)
		}
	}
	return items, rows.Err()
}

func (api *externalAPI) accountLabels(ctx context.Context, publicID string, groups bool) ([]string, error) {
	table, members, foreign := "account_tags", "account_tag_members", "tag_id"
	if groups {
		table, members, foreign = "account_groups", "account_group_members", "group_id"
	}
	rows, err := api.db.QueryContext(ctx, `SELECT n.name FROM `+table+` n JOIN `+members+` m ON m.`+foreign+` = n.id
		JOIN accounts a ON a.id = m.account_id WHERE a.public_id = ? ORDER BY n.name COLLATE NOCASE`, publicID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	values := make([]string, 0)
	for rows.Next() {
		var value string
		if err := rows.Scan(&value); err != nil {
			return nil, err
		}
		values = append(values, value)
	}
	return values, rows.Err()
}

func (api *externalAPI) internalError(w http.ResponseWriter, err error) {
	api.logger.Error("external API request failed", "event", "external_api_request_failed", "error", err)
	writeExternalError(w, http.StatusInternalServerError, "internal_error", "服务暂时无法完成请求")
}

func writeExternalJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func writeExternalError(w http.ResponseWriter, status int, code, message string) {
	writeExternalJSON(w, status, map[string]any{"error": map[string]string{"code": code, "message": message}})
}

func encodeMessageCursor(receivedAt time.Time, publicID string) string {
	encoded, _ := json.Marshal(messageCursor{ReceivedAt: receivedAt.UTC().Format(time.RFC3339Nano), PublicID: publicID})
	return base64.RawURLEncoding.EncodeToString(encoded)
}

func decodeMessageCursor(value string) (struct {
	ReceivedAt time.Time
	PublicID   string
}, error) {
	var result struct {
		ReceivedAt time.Time
		PublicID   string
	}
	decoded, err := base64.RawURLEncoding.DecodeString(value)
	if err != nil {
		return result, err
	}
	var cursor messageCursor
	if err := json.Unmarshal(decoded, &cursor); err != nil || cursor.PublicID == "" {
		return result, errors.New("invalid cursor")
	}
	result.ReceivedAt, err = time.Parse(time.RFC3339Nano, cursor.ReceivedAt)
	result.PublicID = cursor.PublicID
	return result, err
}

func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	ip := net.ParseIP(host)
	if ip != nil && ip.IsLoopback() {
		if forwarded := strings.TrimSpace(strings.Split(r.Header.Get("X-Forwarded-For"), ",")[0]); net.ParseIP(forwarded) != nil {
			return forwarded
		}
	}
	return host
}

func parseOptionalTime(value sql.NullString) (*time.Time, error) {
	if !value.Valid || value.String == "" {
		return nil, nil
	}
	parsed, err := time.Parse(time.RFC3339Nano, value.String)
	if err != nil {
		return nil, err
	}
	return &parsed, nil
}

func escapeExternalLike(value string) string {
	value = strings.ReplaceAll(value, "\\", "\\\\")
	value = strings.ReplaceAll(value, "%", "\\%")
	return strings.ReplaceAll(value, "_", "\\_")
}

type apiRateLimiter struct {
	mu      sync.Mutex
	windows map[string]rateWindow
}

type rateWindow struct {
	started time.Time
	count   int
}

func newAPIRateLimiter() *apiRateLimiter { return &apiRateLimiter{windows: map[string]rateWindow{}} }

func (l *apiRateLimiter) Allow(key string, maximum int, duration time.Duration) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := time.Now()
	window := l.windows[key]
	if window.started.IsZero() || now.Sub(window.started) >= duration {
		l.windows[key] = rateWindow{started: now, count: 1}
		return true
	}
	if window.count >= maximum {
		return false
	}
	window.count++
	l.windows[key] = window
	return true
}
