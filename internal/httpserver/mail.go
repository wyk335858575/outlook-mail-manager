package httpserver

import (
	"context"
	"crypto/subtle"
	"errors"
	"io"
	"log/slog"
	"math"
	"mime"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"outlook-mail-manager/internal/accounts"
	"outlook-mail-manager/internal/auth"
	mailbox "outlook-mail-manager/internal/mail"
)

type mailService interface {
	SearchMessages(context.Context, mailbox.MessageFilter) ([]mailbox.MessageSummary, error)
	GetMessage(context.Context, string) (mailbox.MessageDetail, error)
	MarkMessageRead(context.Context, string) error
	MarkMessagesRead(context.Context, []string) []mailbox.MessageReadResult
	MoveMessageToDeletedItems(context.Context, string) error
	GetMessageHTML(context.Context, string, bool) (string, error)
	ListAttachments(context.Context, string) ([]mailbox.Attachment, error)
	OpenAttachment(context.Context, string, string) (mailbox.AttachmentDownload, error)
	Status(context.Context) (mailbox.Status, error)
	GetSettings(context.Context) (mailbox.Settings, error)
	UpdateSettings(context.Context, mailbox.Settings) (mailbox.Settings, error)
	SyncAccount(context.Context, string) error
	EnqueueAll(context.Context) (int, error)
	ListClassificationRules(context.Context) ([]mailbox.ClassificationRule, error)
	CreateClassificationRule(context.Context, mailbox.ClassificationRule) (mailbox.ClassificationRule, error)
	DeleteClassificationRule(context.Context, string) error
	CorrectMessageCategory(context.Context, string, mailbox.Category) error
	ReclassifyAll(context.Context) error
	SetMessageFlagged(context.Context, string, bool) error
	SetAccountCleanupProtected(context.Context, string, bool) error
	ListCleanupActions(context.Context, string, int) ([]mailbox.CleanupItem, error)
	ApproveCleanup(context.Context, string) (mailbox.CleanupItem, error)
	ApproveCleanupBatch(context.Context, []string) []mailbox.CleanupApprovalResult
	RestoreCleanup(context.Context, string) (mailbox.CleanupItem, error)
	RetryCleanup(context.Context, string) error
	ListAuditEvents(context.Context, int) ([]mailbox.AuditEvent, error)
	ListPersonalInboxRules(context.Context) ([]mailbox.PersonalInboxRule, error)
	CreatePersonalInboxRule(context.Context, mailbox.PersonalInboxRule) (mailbox.PersonalInboxRule, error)
	UpdatePersonalInboxRule(context.Context, string, mailbox.PersonalInboxRule) (mailbox.PersonalInboxRule, error)
	DeletePersonalInboxRule(context.Context, string) error
}

type mailAPI struct {
	service     mailService
	authService *auth.Service
	logger      *slog.Logger
}

func newMailAPI(service mailService, authService *auth.Service, logger *slog.Logger) *mailAPI {
	return &mailAPI{service: service, authService: authService, logger: logger}
}

func (api *mailAPI) register(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/mail/messages", api.messages)
	mux.HandleFunc("GET /api/mail/messages/{public_id}/html", api.messageHTML)
	mux.HandleFunc("GET /api/mail/messages/{public_id}/attachments", api.attachments)
	mux.HandleFunc("GET /api/mail/messages/{public_id}/attachments/{attachment_id}", api.downloadAttachment)
	mux.HandleFunc("POST /api/mail/messages/read", api.markMessagesRead)
	mux.HandleFunc("POST /api/mail/messages/{public_id}/read", api.markMessageRead)
	mux.HandleFunc("POST /api/mail/messages/{public_id}/delete", api.deleteMessage)
	mux.HandleFunc("GET /api/mail/messages/{public_id}", api.message)
	mux.HandleFunc("GET /api/mail/status", api.status)
	mux.HandleFunc("POST /api/mail/sync", api.sync)
	mux.HandleFunc("GET /api/settings", api.settings)
	mux.HandleFunc("PUT /api/settings", api.updateSettings)
	mux.HandleFunc("GET /api/personal-inbox/rules", api.personalInboxRules)
	mux.HandleFunc("POST /api/personal-inbox/rules", api.createPersonalInboxRule)
	mux.HandleFunc("PUT /api/personal-inbox/rules/{public_id}", api.updatePersonalInboxRule)
	mux.HandleFunc("DELETE /api/personal-inbox/rules/{public_id}", api.deletePersonalInboxRule)
	mux.HandleFunc("GET /api/classification/rules", api.classificationRules)
	mux.HandleFunc("POST /api/classification/rules", api.createClassificationRule)
	mux.HandleFunc("DELETE /api/classification/rules/{public_id}", api.deleteClassificationRule)
	mux.HandleFunc("POST /api/classification/reclassify", api.reclassify)
	mux.HandleFunc("POST /api/mail/messages/{public_id}/category", api.correctCategory)
	mux.HandleFunc("POST /api/mail/messages/{public_id}/flag", api.setFlagged)
	mux.HandleFunc("POST /api/accounts/{public_id}/cleanup-protection", api.setAccountCleanupProtection)
	mux.HandleFunc("GET /api/cleanup", api.cleanupActions)
	mux.HandleFunc("POST /api/cleanup/approve", api.approveCleanup)
	mux.HandleFunc("POST /api/cleanup/{public_id}/restore", api.restoreCleanup)
	mux.HandleFunc("POST /api/cleanup/{public_id}/retry", api.retryCleanup)
	mux.HandleFunc("GET /api/audit", api.auditEvents)
}

func (api *mailAPI) messages(w http.ResponseWriter, r *http.Request) {
	if !api.authorize(w, r, false) {
		return
	}
	filter := mailbox.MessageFilter{
		Query: strings.TrimSpace(r.URL.Query().Get("q")), Account: strings.TrimSpace(r.URL.Query().Get("account")),
		Group: strings.TrimSpace(r.URL.Query().Get("group")), Tag: strings.TrimSpace(r.URL.Query().Get("tag")),
		Category: mailbox.Category(strings.TrimSpace(r.URL.Query().Get("category"))),
		Folder:   strings.TrimSpace(r.URL.Query().Get("folder")), Sender: strings.TrimSpace(r.URL.Query().Get("sender")),
	}
	if !api.parseMessageTimes(w, r, &filter) {
		return
	}
	if value := strings.TrimSpace(r.URL.Query().Get("limit")); value != "" {
		limit, err := strconv.Atoi(value)
		if err != nil || limit < 1 {
			writeAPIError(w, http.StatusBadRequest, "invalid_limit", "邮件数量必须是正整数")
			return
		}
		filter.Limit = limit
	}
	if value := strings.TrimSpace(r.URL.Query().Get("unread")); value != "" {
		unread, err := strconv.ParseBool(value)
		if err != nil {
			writeAPIError(w, http.StatusBadRequest, "invalid_unread", "未读筛选必须是 true 或 false")
			return
		}
		filter.Unread = &unread
	}
	if value := strings.TrimSpace(r.URL.Query().Get("personal")); value != "" {
		personal, err := strconv.ParseBool(value)
		if err != nil {
			writeAPIError(w, http.StatusBadRequest, "invalid_personal", "个性化收件箱筛选必须是 true 或 false")
			return
		}
		filter.PersonalOnly = personal
	}
	items, err := api.service.SearchMessages(r.Context(), filter)
	if err != nil {
		api.writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"messages": items})
}

func (api *mailAPI) deleteMessage(w http.ResponseWriter, r *http.Request) {
	if !api.authorize(w, r, true) {
		return
	}
	var request struct {
		Confirm string `json:"confirm"`
	}
	if err := decodeJSONLimit(w, r, &request, 4<<10); err != nil {
		writeAPIError(w, http.StatusBadRequest, "invalid_request", "删除邮件请求格式无效")
		return
	}
	if request.Confirm != "MOVE_TO_DELETED_ITEMS" {
		writeAPIError(w, http.StatusBadRequest, "invalid_delete_confirmation", "必须明确确认将邮件移入 Outlook 已删除邮件")
		return
	}
	if err := api.service.MoveMessageToDeletedItems(r.Context(), r.PathValue("public_id")); err != nil {
		api.writeError(w, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusNoContent)
}

func (api *mailAPI) parseMessageTimes(w http.ResponseWriter, r *http.Request, filter *mailbox.MessageFilter) bool {
	for key, destination := range map[string]**time.Time{"since": &filter.Since, "until": &filter.Until} {
		value := strings.TrimSpace(r.URL.Query().Get(key))
		if value == "" {
			continue
		}
		parsed, err := time.Parse(time.RFC3339, value)
		if err != nil {
			writeAPIError(w, http.StatusBadRequest, "invalid_"+key, "时间筛选必须使用 RFC 3339 格式")
			return false
		}
		*destination = &parsed
	}
	return true
}

func (api *mailAPI) classificationRules(w http.ResponseWriter, r *http.Request) {
	if !api.authorize(w, r, false) {
		return
	}
	items, err := api.service.ListClassificationRules(r.Context())
	if err != nil {
		api.writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"rules": items})
}

func (api *mailAPI) createClassificationRule(w http.ResponseWriter, r *http.Request) {
	if !api.authorize(w, r, true) {
		return
	}
	var request mailbox.ClassificationRule
	if err := decodeJSON(w, r, &request); err != nil {
		writeAPIError(w, http.StatusBadRequest, "invalid_request", "请求格式无效")
		return
	}
	item, err := api.service.CreateClassificationRule(r.Context(), request)
	if err != nil {
		api.writeError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, item)
}

func (api *mailAPI) deleteClassificationRule(w http.ResponseWriter, r *http.Request) {
	if !api.authorize(w, r, true) {
		return
	}
	if err := api.service.DeleteClassificationRule(r.Context(), r.PathValue("public_id")); err != nil {
		api.writeError(w, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusNoContent)
}

func (api *mailAPI) reclassify(w http.ResponseWriter, r *http.Request) {
	if !api.authorize(w, r, true) {
		return
	}
	if err := api.service.ReclassifyAll(r.Context()); err != nil {
		api.writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"reclassified": true})
}

func (api *mailAPI) correctCategory(w http.ResponseWriter, r *http.Request) {
	if !api.authorize(w, r, true) {
		return
	}
	var request struct {
		Category mailbox.Category `json:"category"`
	}
	if err := decodeJSON(w, r, &request); err != nil {
		writeAPIError(w, http.StatusBadRequest, "invalid_request", "请求格式无效")
		return
	}
	if err := api.service.CorrectMessageCategory(r.Context(), r.PathValue("public_id"), request.Category); err != nil {
		api.writeError(w, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusNoContent)
}

func (api *mailAPI) setFlagged(w http.ResponseWriter, r *http.Request) {
	if !api.authorize(w, r, true) {
		return
	}
	var request struct {
		Flagged bool `json:"flagged"`
	}
	if err := decodeJSON(w, r, &request); err != nil {
		writeAPIError(w, http.StatusBadRequest, "invalid_request", "请求格式无效")
		return
	}
	if err := api.service.SetMessageFlagged(r.Context(), r.PathValue("public_id"), request.Flagged); err != nil {
		api.writeError(w, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusNoContent)
}

func (api *mailAPI) setAccountCleanupProtection(w http.ResponseWriter, r *http.Request) {
	if !api.authorize(w, r, true) {
		return
	}
	var request struct {
		Protected bool `json:"protected"`
	}
	if err := decodeJSON(w, r, &request); err != nil {
		writeAPIError(w, http.StatusBadRequest, "invalid_request", "请求格式无效")
		return
	}
	if err := api.service.SetAccountCleanupProtected(r.Context(), r.PathValue("public_id"), request.Protected); err != nil {
		api.writeError(w, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusNoContent)
}

func (api *mailAPI) cleanupActions(w http.ResponseWriter, r *http.Request) {
	if !api.authorize(w, r, false) {
		return
	}
	items, err := api.service.ListCleanupActions(r.Context(), r.URL.Query().Get("state"), 500)
	if err != nil {
		api.writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"actions": items})
}

func (api *mailAPI) approveCleanup(w http.ResponseWriter, r *http.Request) {
	if !api.authorize(w, r, true) {
		return
	}
	var request struct {
		PublicIDs []string `json:"public_ids"`
		Confirm   string   `json:"confirm"`
	}
	if err := decodeJSON(w, r, &request); err != nil || request.Confirm != "MOVE_TO_HOLDING" || len(request.PublicIDs) == 0 || len(request.PublicIDs) > 500 {
		writeAPIError(w, http.StatusBadRequest, "invalid_cleanup_confirmation", "必须明确确认并选择 1 到 500 封邮件")
		return
	}
	type result struct {
		PublicID string               `json:"public_id"`
		Item     *mailbox.CleanupItem `json:"item,omitempty"`
		Error    string               `json:"error,omitempty"`
	}
	serviceResults := api.service.ApproveCleanupBatch(r.Context(), request.PublicIDs)
	results := make([]result, 0, len(serviceResults))
	for _, item := range serviceResults {
		if item.Err != nil {
			results = append(results, result{PublicID: item.PublicID, Error: cleanupPublicError(item.Err)})
			continue
		}
		results = append(results, result{PublicID: item.PublicID, Item: item.Item})
	}
	writeJSON(w, http.StatusOK, map[string]any{"results": results})
}

func (api *mailAPI) restoreCleanup(w http.ResponseWriter, r *http.Request) {
	if !api.authorize(w, r, true) {
		return
	}
	var request struct {
		Confirm string `json:"confirm"`
	}
	if err := decodeJSON(w, r, &request); err != nil || request.Confirm != "RESTORE" {
		writeAPIError(w, http.StatusBadRequest, "invalid_restore_confirmation", "必须明确确认恢复邮件")
		return
	}
	item, err := api.service.RestoreCleanup(r.Context(), r.PathValue("public_id"))
	if err != nil {
		api.writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, item)
}

func (api *mailAPI) retryCleanup(w http.ResponseWriter, r *http.Request) {
	if !api.authorize(w, r, true) {
		return
	}
	if err := api.service.RetryCleanup(r.Context(), r.PathValue("public_id")); err != nil {
		api.writeError(w, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusNoContent)
}

func (api *mailAPI) auditEvents(w http.ResponseWriter, r *http.Request) {
	if !api.authorize(w, r, false) {
		return
	}
	items, err := api.service.ListAuditEvents(r.Context(), 200)
	if err != nil {
		api.writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"events": items})
}

func cleanupPublicError(err error) string {
	switch {
	case errors.Is(err, mailbox.ErrCleanupProtected):
		return "邮件受保护，不能进入待清理"
	case errors.Is(err, mailbox.ErrCleanupStateConflict):
		return "清理状态已变化，请刷新后重试"
	default:
		return "无法移动到待清理文件夹"
	}
}

func (api *mailAPI) message(w http.ResponseWriter, r *http.Request) {
	if !api.authorize(w, r, false) {
		return
	}
	item, err := api.service.GetMessage(r.Context(), r.PathValue("public_id"))
	if err != nil {
		api.writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, item)
}

func (api *mailAPI) markMessageRead(w http.ResponseWriter, r *http.Request) {
	if !api.authorize(w, r, true) {
		return
	}
	if err := api.service.MarkMessageRead(r.Context(), r.PathValue("public_id")); err != nil {
		api.writeError(w, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusNoContent)
}

func (api *mailAPI) markMessagesRead(w http.ResponseWriter, r *http.Request) {
	if !api.authorize(w, r, true) {
		return
	}
	var request struct {
		PublicIDs []string `json:"public_ids"`
	}
	if err := decodeJSON(w, r, &request); err != nil || len(request.PublicIDs) == 0 || len(request.PublicIDs) > 500 {
		writeAPIError(w, http.StatusBadRequest, "invalid_message_selection", "请选择 1 到 500 封邮件")
		return
	}
	type result struct {
		PublicID string `json:"public_id"`
		Read     bool   `json:"read"`
		Error    string `json:"error,omitempty"`
	}
	serviceResults := api.service.MarkMessagesRead(r.Context(), request.PublicIDs)
	results := make([]result, 0, len(serviceResults))
	updated := 0
	for _, item := range serviceResults {
		if item.Err != nil {
			message := "无法设置已读"
			if errors.Is(item.Err, mailbox.ErrMessageNotFound) {
				message = "邮件不存在或已不在同步文件夹中"
			}
			results = append(results, result{PublicID: item.PublicID, Error: message})
			continue
		}
		if item.Read {
			updated++
		}
		results = append(results, result{PublicID: item.PublicID, Read: item.Read})
	}
	writeJSON(w, http.StatusOK, map[string]any{"updated": updated, "results": results})
}

func (api *mailAPI) messageHTML(w http.ResponseWriter, r *http.Request) {
	if !api.authorize(w, r, false) {
		return
	}
	loadImages := false
	if value := strings.TrimSpace(r.URL.Query().Get("images")); value != "" {
		switch value {
		case "0":
		case "1":
			loadImages = true
		default:
			writeAPIError(w, http.StatusBadRequest, "invalid_images", "图片选项必须是 0 或 1")
			return
		}
	}
	body, err := api.service.GetMessageHTML(r.Context(), r.PathValue("public_id"), loadImages)
	if err != nil {
		api.writeError(w, err)
		return
	}
	imageSource := "data:"
	if loadImages {
		imageSource = "https: data:"
	}
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Content-Security-Policy", "default-src 'none'; img-src "+imageSource+"; style-src 'unsafe-inline'; connect-src 'none'; media-src 'none'; frame-src 'none'; object-src 'none'; base-uri 'none'; form-action 'none'; frame-ancestors 'self'; sandbox")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("X-Frame-Options", "SAMEORIGIN")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(body))
}

func (api *mailAPI) attachments(w http.ResponseWriter, r *http.Request) {
	if !api.authorize(w, r, false) {
		return
	}
	items, err := api.service.ListAttachments(r.Context(), r.PathValue("public_id"))
	if err != nil {
		api.writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"attachments": items})
}

func (api *mailAPI) downloadAttachment(w http.ResponseWriter, r *http.Request) {
	if !api.authorize(w, r, false) {
		return
	}
	download, err := api.service.OpenAttachment(r.Context(), r.PathValue("public_id"), r.PathValue("attachment_id"))
	if err != nil {
		api.writeError(w, err)
		return
	}
	defer download.Body.Close()
	contentType := "application/octet-stream"
	if parsed, _, err := mime.ParseMediaType(download.ContentType); err == nil && parsed != "" {
		contentType = parsed
	}
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": safeAttachmentName(download.Name)}))
	if download.Size > 0 {
		w.Header().Set("Content-Length", strconv.FormatInt(download.Size, 10))
	}
	w.WriteHeader(http.StatusOK)
	_, _ = io.Copy(w, download.Body)
}

func safeAttachmentName(value string) string {
	value = strings.ReplaceAll(value, "\\", "/")
	if index := strings.LastIndex(value, "/"); index >= 0 {
		value = value[index+1:]
	}
	value = strings.Map(func(r rune) rune {
		if r < 32 || r == 127 {
			return -1
		}
		return r
	}, strings.TrimSpace(value))
	if value == "" || value == "." || value == ".." {
		return "attachment"
	}
	if len(value) > 180 {
		value = value[:180]
		for !utf8.ValidString(value) {
			value = value[:len(value)-1]
		}
	}
	return value
}

func (api *mailAPI) status(w http.ResponseWriter, r *http.Request) {
	if !api.authorize(w, r, false) {
		return
	}
	status, err := api.service.Status(r.Context())
	if err != nil {
		api.writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, status)
}

func (api *mailAPI) settings(w http.ResponseWriter, r *http.Request) {
	if !api.authorize(w, r, false) {
		return
	}
	settings, err := api.service.GetSettings(r.Context())
	if err != nil {
		api.writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, settings)
}

func (api *mailAPI) updateSettings(w http.ResponseWriter, r *http.Request) {
	if !api.authorize(w, r, true) {
		return
	}
	var request mailbox.Settings
	if err := decodeJSON(w, r, &request); err != nil {
		writeAPIError(w, http.StatusBadRequest, "invalid_request", "请求格式无效")
		return
	}
	settings, err := api.service.UpdateSettings(r.Context(), request)
	if err != nil {
		api.writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, settings)
}

func (api *mailAPI) personalInboxRules(w http.ResponseWriter, r *http.Request) {
	if !api.authorize(w, r, false) {
		return
	}
	items, err := api.service.ListPersonalInboxRules(r.Context())
	if err != nil {
		api.writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"rules": items})
}

func (api *mailAPI) createPersonalInboxRule(w http.ResponseWriter, r *http.Request) {
	if !api.authorize(w, r, true) {
		return
	}
	var request mailbox.PersonalInboxRule
	if err := decodeJSON(w, r, &request); err != nil {
		writeAPIError(w, http.StatusBadRequest, "invalid_request", "请求格式无效")
		return
	}
	item, err := api.service.CreatePersonalInboxRule(r.Context(), request)
	if err != nil {
		api.writeError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, item)
}

func (api *mailAPI) updatePersonalInboxRule(w http.ResponseWriter, r *http.Request) {
	if !api.authorize(w, r, true) {
		return
	}
	var request mailbox.PersonalInboxRule
	if err := decodeJSON(w, r, &request); err != nil {
		writeAPIError(w, http.StatusBadRequest, "invalid_request", "请求格式无效")
		return
	}
	item, err := api.service.UpdatePersonalInboxRule(r.Context(), r.PathValue("public_id"), request)
	if err != nil {
		api.writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, item)
}

func (api *mailAPI) deletePersonalInboxRule(w http.ResponseWriter, r *http.Request) {
	if !api.authorize(w, r, true) {
		return
	}
	if err := api.service.DeletePersonalInboxRule(r.Context(), r.PathValue("public_id")); err != nil {
		api.writeError(w, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusNoContent)
}

func (api *mailAPI) sync(w http.ResponseWriter, r *http.Request) {
	if !api.authorize(w, r, true) {
		return
	}
	var request struct {
		Account string `json:"account"`
	}
	if err := decodeJSON(w, r, &request); err != nil {
		writeAPIError(w, http.StatusBadRequest, "invalid_request", "请求格式无效")
		return
	}
	if account := strings.TrimSpace(request.Account); account != "" {
		if err := api.service.SyncAccount(r.Context(), account); err != nil {
			api.writeError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]int{"synced": 1})
		return
	}
	count, err := api.service.EnqueueAll(r.Context())
	if err != nil {
		api.writeError(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]int{"queued": count})
}

func (api *mailAPI) authorize(w http.ResponseWriter, r *http.Request, requireCSRF bool) bool {
	cookie, err := r.Cookie(api.authService.CookieName())
	if err != nil {
		writeAPIError(w, http.StatusUnauthorized, "invalid_session", "会话已失效，请重新登录")
		return false
	}
	status, err := api.authService.Status(r.Context(), cookie.Value)
	if err != nil {
		api.logger.Error("mail authorization check failed", "event", "mail_auth_check_failed", "error", err)
		writeAPIError(w, http.StatusInternalServerError, "internal_error", "服务暂时无法完成请求")
		return false
	}
	if !status.Authenticated {
		writeAPIError(w, http.StatusUnauthorized, "invalid_session", "会话已失效，请重新登录")
		return false
	}
	if requireCSRF && subtle.ConstantTimeCompare([]byte(status.CSRFToken), []byte(r.Header.Get("X-CSRF-Token"))) != 1 {
		writeAPIError(w, http.StatusForbidden, "invalid_csrf", "安全校验失败，请刷新页面后重试")
		return false
	}
	return true
}

func (api *mailAPI) writeError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, context.Canceled):
		return
	case errors.Is(err, mailbox.ErrMessageNotFound):
		writeAPIError(w, http.StatusNotFound, "message_not_found", "邮件不存在或已不在同步文件夹中")
	case errors.Is(err, accounts.ErrAccountNotFound):
		writeAPIError(w, http.StatusNotFound, "account_not_found", "账号不存在")
	case errors.Is(err, accounts.ErrAccountDisabled):
		writeAPIError(w, http.StatusConflict, "account_disabled", "账号已停用")
	case errors.Is(err, accounts.ErrReauthorizationRequired):
		writeAPIError(w, http.StatusUnauthorized, "reauth_required", "Microsoft 授权已失效，请重新授权")
	case errors.Is(err, mailbox.ErrNoActiveAccounts):
		writeAPIError(w, http.StatusConflict, "no_active_accounts", "没有可同步账号")
	case errors.Is(err, mailbox.ErrInvalidSettings):
		writeAPIError(w, http.StatusBadRequest, "invalid_settings", "设置值不在允许范围内")
	case errors.Is(err, mailbox.ErrInvalidPersonalRule):
		writeAPIError(w, http.StatusBadRequest, "invalid_personal_rule", "个性化规则必须包含名称和至少一个有效条件")
	case errors.Is(err, mailbox.ErrPersonalRuleNotFound):
		writeAPIError(w, http.StatusNotFound, "personal_rule_not_found", "个性化规则不存在")
	case errors.Is(err, mailbox.ErrInvalidRule):
		writeAPIError(w, http.StatusBadRequest, "invalid_rule", "分类规则无效")
	case errors.Is(err, mailbox.ErrRuleNotFound):
		writeAPIError(w, http.StatusNotFound, "rule_not_found", "分类规则不存在")
	case errors.Is(err, mailbox.ErrInvalidCategory):
		writeAPIError(w, http.StatusBadRequest, "invalid_category", "邮件分类无效")
	case errors.Is(err, mailbox.ErrCleanupNotFound):
		writeAPIError(w, http.StatusNotFound, "cleanup_not_found", "清理记录不存在")
	case errors.Is(err, mailbox.ErrInvalidCleanupState):
		writeAPIError(w, http.StatusBadRequest, "invalid_cleanup_state", "清理状态无效")
	case errors.Is(err, mailbox.ErrCleanupStateConflict):
		writeAPIError(w, http.StatusConflict, "cleanup_state_conflict", "清理状态已变化，请刷新后重试")
	case errors.Is(err, mailbox.ErrCleanupProtected):
		writeAPIError(w, http.StatusConflict, "cleanup_protected", "邮件受保护，不能进入待清理")
	default:
		var graphErr *mailbox.GraphError
		if errors.As(err, &graphErr) {
			if graphErr.Status == http.StatusForbidden {
				writeAPIError(w, http.StatusForbidden, "mail_permission_required", "Microsoft 授权缺少 Mail.ReadWrite，请补充委托权限后重新授权")
				return
			}
			if graphErr.RetryAt != nil {
				seconds := int(math.Ceil(time.Until(*graphErr.RetryAt).Seconds()))
				if seconds < 1 {
					seconds = 1
				}
				w.Header().Set("Retry-After", strconv.Itoa(seconds))
			}
			writeAPIError(w, http.StatusBadGateway, "graph_request_failed", "Microsoft Graph 暂时无法完成同步")
			return
		}
		api.logger.Error("mail request failed", "event", "mail_request_failed", "error", err)
		writeAPIError(w, http.StatusInternalServerError, "mail_request_failed", "邮件服务暂时无法完成请求")
	}
}
