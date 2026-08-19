package httpserver

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"outlook-mail-manager/internal/database"
	mailbox "outlook-mail-manager/internal/mail"
)

type fakeMailService struct {
	filter            mailbox.MessageFilter
	syncPublic        string
	queued            int
	htmlPublic        string
	htmlImages        bool
	readPublic        string
	readPublics       []string
	deletedPublic     string
	settings          mailbox.Settings
	attachments       []mailbox.Attachment
	download          mailbox.AttachmentDownload
	personalRule      mailbox.PersonalInboxRule
	deletedPersonalID string
}

func (s *fakeMailService) SearchMessages(_ context.Context, filter mailbox.MessageFilter) ([]mailbox.MessageSummary, error) {
	s.filter = filter
	return []mailbox.MessageSummary{{PublicID: "msg_1", Subject: "Test", Unread: true}}, nil
}

func (s *fakeMailService) GetMessage(_ context.Context, publicID string) (mailbox.MessageDetail, error) {
	return mailbox.MessageDetail{MessageSummary: mailbox.MessageSummary{PublicID: publicID}, BodyText: "plain"}, nil
}

func (s *fakeMailService) MarkMessageRead(_ context.Context, publicID string) error {
	s.readPublic = publicID
	s.readPublics = append(s.readPublics, publicID)
	return nil
}

func (s *fakeMailService) MarkMessagesRead(_ context.Context, publicIDs []string) []mailbox.MessageReadResult {
	seen := make(map[string]struct{}, len(publicIDs))
	results := make([]mailbox.MessageReadResult, 0, len(publicIDs))
	for _, publicID := range publicIDs {
		publicID = strings.TrimSpace(publicID)
		if _, exists := seen[publicID]; exists {
			continue
		}
		seen[publicID] = struct{}{}
		s.readPublics = append(s.readPublics, publicID)
		results = append(results, mailbox.MessageReadResult{PublicID: publicID, Read: true})
	}
	return results
}
func (s *fakeMailService) MoveMessageToDeletedItems(_ context.Context, publicID string) error {
	s.deletedPublic = publicID
	return nil
}

func (s *fakeMailService) GetMessageHTML(_ context.Context, publicID string, loadImages bool) (string, error) {
	s.htmlPublic = publicID
	s.htmlImages = loadImages
	return "<!doctype html><html><body>safe</body></html>", nil
}

func (s *fakeMailService) ListAttachments(context.Context, string) ([]mailbox.Attachment, error) {
	return s.attachments, nil
}

func (s *fakeMailService) OpenAttachment(context.Context, string, string) (mailbox.AttachmentDownload, error) {
	return s.download, nil
}

func (s *fakeMailService) Status(context.Context) (mailbox.Status, error) {
	return mailbox.Status{ActiveAccounts: 1}, nil
}

func (s *fakeMailService) GetSettings(context.Context) (mailbox.Settings, error) {
	if s.settings.SyncIntervalSeconds == 0 {
		s.settings = mailbox.DefaultSettings()
	}
	return s.settings, nil
}

func (s *fakeMailService) UpdateSettings(_ context.Context, settings mailbox.Settings) (mailbox.Settings, error) {
	s.settings = settings
	s.settings.UpdatedAt = "2026-08-17T12:00:00Z"
	return s.settings, nil
}

func (s *fakeMailService) SyncAccount(_ context.Context, publicID string) error {
	s.syncPublic = publicID
	return nil
}

func (s *fakeMailService) EnqueueAll(context.Context) (int, error) {
	s.queued++
	return 1, nil
}

func (s *fakeMailService) ListClassificationRules(context.Context) ([]mailbox.ClassificationRule, error) {
	return nil, nil
}

func (s *fakeMailService) CreateClassificationRule(_ context.Context, rule mailbox.ClassificationRule) (mailbox.ClassificationRule, error) {
	return rule, nil
}

func (s *fakeMailService) DeleteClassificationRule(context.Context, string) error { return nil }
func (s *fakeMailService) CorrectMessageCategory(context.Context, string, mailbox.Category) error {
	return nil
}
func (s *fakeMailService) ReclassifyAll(context.Context) error                            { return nil }
func (s *fakeMailService) SetMessageFlagged(context.Context, string, bool) error          { return nil }
func (s *fakeMailService) SetAccountCleanupProtected(context.Context, string, bool) error { return nil }
func (s *fakeMailService) ListCleanupActions(context.Context, string, int) ([]mailbox.CleanupItem, error) {
	return nil, nil
}
func (s *fakeMailService) ApproveCleanup(context.Context, string) (mailbox.CleanupItem, error) {
	return mailbox.CleanupItem{}, nil
}
func (s *fakeMailService) ApproveCleanupBatch(_ context.Context, publicIDs []string) []mailbox.CleanupApprovalResult {
	results := make([]mailbox.CleanupApprovalResult, 0, len(publicIDs))
	for _, publicID := range publicIDs {
		item := mailbox.CleanupItem{PublicID: publicID}
		results = append(results, mailbox.CleanupApprovalResult{PublicID: publicID, Item: &item})
	}
	return results
}
func (s *fakeMailService) RestoreCleanup(context.Context, string) (mailbox.CleanupItem, error) {
	return mailbox.CleanupItem{}, nil
}
func (s *fakeMailService) RetryCleanup(context.Context, string) error { return nil }
func (s *fakeMailService) ListAuditEvents(context.Context, int) ([]mailbox.AuditEvent, error) {
	return nil, nil
}
func (s *fakeMailService) ListPersonalInboxRules(context.Context) ([]mailbox.PersonalInboxRule, error) {
	return nil, nil
}
func (s *fakeMailService) CreatePersonalInboxRule(_ context.Context, rule mailbox.PersonalInboxRule) (mailbox.PersonalInboxRule, error) {
	s.personalRule = rule
	return rule, nil
}
func (s *fakeMailService) UpdatePersonalInboxRule(_ context.Context, _ string, rule mailbox.PersonalInboxRule) (mailbox.PersonalInboxRule, error) {
	s.personalRule = rule
	return rule, nil
}
func (s *fakeMailService) DeletePersonalInboxRule(_ context.Context, publicID string) error {
	s.deletedPersonalID = publicID
	return nil
}
func TestMailAPIRequiresSessionAndCSRF(t *testing.T) {
	ctx := context.Background()
	store, err := database.Open(ctx, t.TempDir())
	if err != nil {
		t.Fatalf("database.Open() error = %v", err)
	}
	defer store.Close()
	now := time.Date(2026, time.August, 17, 12, 0, 0, 0, time.UTC)
	authService, _, grant := testAuthenticatedAuthService(t, store, func() time.Time { return now })
	service := &fakeMailService{}
	handler := New(store.DB, slog.New(slog.NewTextHandler(testWriter{t}, nil)), testAssets(), authService, nil, service, nil, nil, nil)
	cookie := &http.Cookie{Name: authService.CookieName(), Value: grant.Token}

	response := performJSON(t, handler, http.MethodGet, "/api/mail/messages", nil, nil, "")
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("list without session status = %d", response.Code)
	}
	response = performJSON(t, handler, http.MethodGet, "/api/mail/messages?q=invoice&folder=inbox&unread=true&personal=true&limit=25", nil, cookie, "")
	if response.Code != http.StatusOK {
		t.Fatalf("list status = %d, body = %s", response.Code, response.Body.String())
	}
	if service.filter.Query != "invoice" || service.filter.Folder != "inbox" || service.filter.Unread == nil || !*service.filter.Unread || !service.filter.PersonalOnly || service.filter.Limit != 25 {
		t.Fatalf("filter = %#v", service.filter)
	}
	response = performJSON(t, handler, http.MethodPost, "/api/mail/messages/msg_1/read", nil, cookie, "")
	if response.Code != http.StatusForbidden {
		t.Fatalf("mark read without CSRF status = %d", response.Code)
	}
	response = performJSON(t, handler, http.MethodPost, "/api/mail/messages/msg_1/read", nil, cookie, grant.CSRFToken)
	if response.Code != http.StatusNoContent || service.readPublic != "msg_1" {
		t.Fatalf("mark read status = %d, public = %q", response.Code, service.readPublic)
	}
	response = performJSON(t, handler, http.MethodPost, "/api/mail/messages/msg_1/delete", map[string]string{
		"confirm": "wrong",
	}, cookie, grant.CSRFToken)
	if response.Code != http.StatusBadRequest || service.deletedPublic != "" {
		t.Fatalf("delete without confirmation status = %d, public = %q", response.Code, service.deletedPublic)
	}
	response = performJSON(t, handler, http.MethodPost, "/api/mail/messages/msg_1/delete", map[string]string{
		"confirm": "MOVE_TO_DELETED_ITEMS",
	}, cookie, grant.CSRFToken)
	if response.Code != http.StatusNoContent || service.deletedPublic != "msg_1" {
		t.Fatalf("delete message status = %d, public = %q", response.Code, service.deletedPublic)
	}
	response = performJSON(t, handler, http.MethodPost, "/api/mail/messages/read", map[string]any{"public_ids": []string{"msg_1", "msg_2"}}, cookie, "")
	if response.Code != http.StatusForbidden {
		t.Fatalf("batch read without CSRF status = %d", response.Code)
	}
	response = performJSON(t, handler, http.MethodPost, "/api/mail/messages/read", map[string]any{"public_ids": []string{"msg_1", "msg_2", "msg_1"}}, cookie, grant.CSRFToken)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"updated":2`) || len(service.readPublics) != 3 {
		t.Fatalf("batch read status = %d, calls = %#v, body = %s", response.Code, service.readPublics, response.Body.String())
	}
	response = performJSON(t, handler, http.MethodGet, "/api/settings", nil, cookie, "")
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"sync_interval_seconds":600`) {
		t.Fatalf("settings status = %d, body = %s", response.Code, response.Body.String())
	}
	settings := mailbox.DefaultSettings()
	settings.SyncIntervalSeconds = 1800
	response = performJSON(t, handler, http.MethodPut, "/api/settings", settings, cookie, "")
	if response.Code != http.StatusForbidden {
		t.Fatalf("settings without CSRF status = %d", response.Code)
	}
	response = performJSON(t, handler, http.MethodPut, "/api/settings", settings, cookie, grant.CSRFToken)
	if response.Code != http.StatusOK || service.settings.SyncIntervalSeconds != 1800 {
		t.Fatalf("settings update status = %d, settings = %#v", response.Code, service.settings)
	}
	personalRule := mailbox.PersonalInboxRule{Name: "付款", Enabled: true, SubjectKeywords: []string{"payment"}}
	response = performJSON(t, handler, http.MethodPost, "/api/personal-inbox/rules", personalRule, cookie, "")
	if response.Code != http.StatusForbidden {
		t.Fatalf("personal rule without CSRF status = %d", response.Code)
	}
	response = performJSON(t, handler, http.MethodPost, "/api/personal-inbox/rules", personalRule, cookie, grant.CSRFToken)
	if response.Code != http.StatusCreated || service.personalRule.Name != "付款" {
		t.Fatalf("personal rule create status = %d, rule = %#v", response.Code, service.personalRule)
	}
	response = performJSON(t, handler, http.MethodDelete, "/api/personal-inbox/rules/personal_1", nil, cookie, grant.CSRFToken)
	if response.Code != http.StatusNoContent || service.deletedPersonalID != "personal_1" {
		t.Fatalf("personal rule delete status = %d, id = %q", response.Code, service.deletedPersonalID)
	}

	response = performJSON(t, handler, http.MethodPost, "/api/mail/sync", map[string]string{}, cookie, "")
	if response.Code != http.StatusForbidden {
		t.Fatalf("sync without CSRF status = %d", response.Code)
	}
	response = performJSON(t, handler, http.MethodPost, "/api/mail/sync", map[string]string{"account": "acc_1"}, cookie, grant.CSRFToken)
	if response.Code != http.StatusOK || service.syncPublic != "acc_1" {
		t.Fatalf("account sync status = %d, account = %q", response.Code, service.syncPublic)
	}
	response = performJSON(t, handler, http.MethodPost, "/api/mail/sync", map[string]string{}, cookie, grant.CSRFToken)
	if response.Code != http.StatusAccepted || service.queued != 1 {
		t.Fatalf("queue status = %d, queued calls = %d", response.Code, service.queued)
	}
}

func TestMailHTMLRequiresSessionAndAppliesIsolatedPolicy(t *testing.T) {
	ctx := context.Background()
	store, err := database.Open(ctx, t.TempDir())
	if err != nil {
		t.Fatalf("database.Open() error = %v", err)
	}
	defer store.Close()
	now := time.Date(2026, time.August, 17, 12, 0, 0, 0, time.UTC)
	authService, _, grant := testAuthenticatedAuthService(t, store, func() time.Time { return now })
	service := &fakeMailService{}
	handler := New(store.DB, slog.New(slog.NewTextHandler(testWriter{t}, nil)), testAssets(), authService, nil, service, nil, nil, nil)
	cookie := &http.Cookie{Name: authService.CookieName(), Value: grant.Token}

	response := performJSON(t, handler, http.MethodGet, "/api/mail/messages/msg_1/html", nil, nil, "")
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("HTML without session status = %d", response.Code)
	}

	response = performJSON(t, handler, http.MethodGet, "/api/mail/messages/msg_1/html?images=0", nil, cookie, "")
	if response.Code != http.StatusOK || service.htmlPublic != "msg_1" || service.htmlImages {
		t.Fatalf("blocked HTML status = %d, public = %q, images = %v", response.Code, service.htmlPublic, service.htmlImages)
	}
	if response.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("Cache-Control = %q", response.Header().Get("Cache-Control"))
	}
	csp := response.Header().Get("Content-Security-Policy")
	if !strings.Contains(csp, "img-src data:") || strings.Contains(csp, "img-src https:") || !strings.Contains(csp, "frame-ancestors 'self'") || !strings.Contains(csp, "sandbox") {
		t.Fatalf("blocked CSP = %q", csp)
	}

	response = performJSON(t, handler, http.MethodGet, "/api/mail/messages/msg_1/html?images=1", nil, cookie, "")
	if response.Code != http.StatusOK || !service.htmlImages {
		t.Fatalf("enabled HTML status = %d, images = %v", response.Code, service.htmlImages)
	}
	if csp := response.Header().Get("Content-Security-Policy"); !strings.Contains(csp, "img-src https: data:") {
		t.Fatalf("enabled CSP = %q", csp)
	}
}

func TestMailAttachmentDownloadSanitizesNameAndDisablesCaching(t *testing.T) {
	ctx := context.Background()
	store, err := database.Open(ctx, t.TempDir())
	if err != nil {
		t.Fatalf("database.Open() error = %v", err)
	}
	defer store.Close()
	now := time.Date(2026, time.August, 17, 12, 0, 0, 0, time.UTC)
	authService, _, grant := testAuthenticatedAuthService(t, store, func() time.Time { return now })
	service := &fakeMailService{download: mailbox.AttachmentDownload{
		Attachment: mailbox.Attachment{ID: "att_1", Name: "..\\..\\report\r\nX-Evil: yes.pdf", ContentType: "application/pdf", Size: 7},
		Body:       io.NopCloser(strings.NewReader("payload")),
	}}
	handler := New(store.DB, slog.New(slog.NewTextHandler(testWriter{t}, nil)), testAssets(), authService, nil, service, nil, nil, nil)
	cookie := &http.Cookie{Name: authService.CookieName(), Value: grant.Token}

	response := performJSON(t, handler, http.MethodGet, "/api/mail/messages/msg_1/attachments/att_1", nil, cookie, "")
	if response.Code != http.StatusOK || response.Body.String() != "payload" {
		t.Fatalf("download status = %d, body = %q", response.Code, response.Body.String())
	}
	if response.Header().Get("Cache-Control") != "no-store" || response.Header().Get("X-Evil") != "" {
		t.Fatalf("download headers = %#v", response.Header())
	}
	disposition := response.Header().Get("Content-Disposition")
	if strings.ContainsAny(disposition, "\r\n") || strings.Contains(disposition, "..") {
		t.Fatalf("Content-Disposition = %q", disposition)
	}
}

func TestSafeAttachmentNamePreservesUTF8WhenTruncated(t *testing.T) {
	name := safeAttachmentName("a" + strings.Repeat("界", 100))
	if len(name) > 180 || !utf8.ValidString(name) {
		t.Fatalf("safe attachment name has %d bytes and valid UTF-8 = %v", len(name), utf8.ValidString(name))
	}
}
