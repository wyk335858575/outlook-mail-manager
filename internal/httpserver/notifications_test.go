package httpserver

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"testing"
	"time"

	"outlook-mail-manager/internal/database"
	"outlook-mail-manager/internal/notify"
)

func TestWXPushTestConfigRequiresAuthCSRFAndDoesNotPersist(t *testing.T) {
	store, err := database.Open(context.Background(), t.TempDir())
	if err != nil {
		t.Fatalf("database.Open() error = %v", err)
	}
	defer store.Close()
	now := time.Date(2026, time.August, 20, 10, 0, 0, 0, time.UTC)
	authService, keyring, grant := testAuthenticatedAuthService(t, store, func() time.Time { return now })
	mode := "success"
	client := &http.Client{Transport: notificationRoundTripFunc(func(request *http.Request) (*http.Response, error) {
		if request.URL.Host != "api.weixin.qq.com" {
			t.Fatalf("WXPush request host = %q", request.URL.Host)
		}
		if mode == "network" {
			return nil, errors.New("network unavailable")
		}
		switch request.URL.Path {
		case "/cgi-bin/stable_token":
			body, _ := io.ReadAll(request.Body)
			if !bytes.Contains(body, []byte(`"appid":"test-app-id`)) || !bytes.Contains(body, []byte(`"secret":"test-app-secret"`)) {
				t.Fatalf("stable token body = %s", body)
			}
			if mode == "credentials" {
				return notificationJSONResponse(`{"errcode":40125,"errmsg":"invalid appsecret"}`), nil
			}
			return notificationJSONResponse(`{"access_token":"test-access-token","expires_in":7200}`), nil
		case "/cgi-bin/message/template/send":
			if request.URL.Query().Get("access_token") != "test-access-token" {
				t.Fatalf("template access token = %q", request.URL.RawQuery)
			}
			body, _ := io.ReadAll(request.Body)
			if bytes.Contains(body, []byte(`"url"`)) || !bytes.Contains(body, []byte(`"touser":"test-open-id"`)) {
				t.Fatalf("template body = %s", body)
			}
			switch mode {
			case "user":
				return notificationJSONResponse(`{"errcode":40003,"errmsg":"invalid openid"}`), nil
			case "template":
				return notificationJSONResponse(`{"errcode":47003,"errmsg":"template mismatch"}`), nil
			default:
				return notificationJSONResponse(`{"errcode":0,"errmsg":"ok"}`), nil
			}
		default:
			t.Fatalf("unexpected WXPush path %q", request.URL.Path)
			return nil, nil
		}
	})}
	notificationService, err := notify.New(store.DB, keyring, notify.Options{HTTPClient: client})
	if err != nil {
		t.Fatalf("notify.New() error = %v", err)
	}
	var logs bytes.Buffer
	handler := New(store.DB, slog.New(slog.NewTextHandler(&logs, nil)), testAssets(), authService, nil, nil, notificationService, nil, nil)
	cookie := &http.Cookie{Name: authService.CookieName(), Value: grant.Token}
	input := map[string]any{
		"kind": "wxpush", "wxpush_app_id": "test-app-id", "wxpush_app_secret": "test-app-secret",
		"wxpush_user_id": "test-open-id", "wxpush_template_id": "test-template-id",
	}

	response := performJSON(t, handler, http.MethodPost, "/api/notifications/channels/test-config", input, nil, "")
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated status = %d, want 401", response.Code)
	}
	response = performJSON(t, handler, http.MethodPost, "/api/notifications/channels/test-config", input, cookie, "")
	if response.Code != http.StatusForbidden {
		t.Fatalf("missing CSRF status = %d, want 403", response.Code)
	}
	response = performJSON(t, handler, http.MethodPost, "/api/notifications/channels/test-config", map[string]string{"kind": "wxpush"}, cookie, grant.CSRFToken)
	if response.Code != http.StatusBadRequest {
		t.Fatalf("invalid config status = %d, want 400", response.Code)
	}
	var auditsBefore int
	if err := store.DB.QueryRow(`SELECT COUNT(*) FROM audit_events`).Scan(&auditsBefore); err != nil {
		t.Fatalf("count audits: %v", err)
	}
	response = performJSON(t, handler, http.MethodPost, "/api/notifications/channels/test-config", input, cookie, grant.CSRFToken)
	if response.Code != http.StatusOK || response.Body.String() != "{\"status\":\"sent\"}\n" {
		t.Fatalf("success response = %d %s", response.Code, response.Body.String())
	}

	for _, failure := range []struct {
		mode   string
		code   string
		status int
	}{
		{"user", "wxpush_user_failed", http.StatusBadRequest},
		{"template", "wxpush_template_failed", http.StatusBadRequest},
		{"network", "wxpush_network_failed", http.StatusBadGateway},
	} {
		mode = failure.mode
		response = performJSON(t, handler, http.MethodPost, "/api/notifications/channels/test-config", input, cookie, grant.CSRFToken)
		if response.Code != failure.status || !strings.Contains(response.Body.String(), failure.code) {
			t.Fatalf("%s response = %d %s", failure.mode, response.Code, response.Body.String())
		}
	}
	mode = "credentials"
	input["wxpush_app_id"] = "test-app-id-2"
	response = performJSON(t, handler, http.MethodPost, "/api/notifications/channels/test-config", input, cookie, grant.CSRFToken)
	if response.Code != http.StatusBadRequest || !strings.Contains(response.Body.String(), "wxpush_credentials_failed") {
		t.Fatalf("credentials response = %d %s", response.Code, response.Body.String())
	}

	var channels, deliveries, auditsAfter int
	if err := store.DB.QueryRow(`SELECT COUNT(*) FROM notification_channels`).Scan(&channels); err != nil {
		t.Fatalf("count channels: %v", err)
	}
	if err := store.DB.QueryRow(`SELECT COUNT(*) FROM notification_deliveries`).Scan(&deliveries); err != nil {
		t.Fatalf("count deliveries: %v", err)
	}
	if err := store.DB.QueryRow(`SELECT COUNT(*) FROM audit_events`).Scan(&auditsAfter); err != nil {
		t.Fatalf("count audits after testing: %v", err)
	}
	if channels != 0 || deliveries != 0 || auditsAfter != auditsBefore {
		t.Fatalf("test config persistence = channels:%d deliveries:%d audits:%d->%d", channels, deliveries, auditsBefore, auditsAfter)
	}
	combined := logs.String() + response.Body.String()
	for _, secret := range []string{"test-app-secret", "test-open-id", "test-access-token"} {
		if strings.Contains(combined, secret) {
			t.Fatalf("logs or response contain %q: %s", secret, combined)
		}
	}
}

type notificationRoundTripFunc func(*http.Request) (*http.Response, error)

func (function notificationRoundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return function(request)
}

func notificationJSONResponse(body string) *http.Response {
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(strings.NewReader(body)),
	}
}
