package notify

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

func TestNotificationProtocols(t *testing.T) {
	now := time.Date(2026, time.August, 17, 12, 0, 0, 0, time.UTC)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/sendMessage"):
			var body map[string]string
			_ = json.NewDecoder(r.Body).Decode(&body)
			if body["chat_id"] != "12345" || !strings.Contains(body["text"], "Test content") {
				t.Fatalf("Telegram body = %#v", body)
			}
			_, _ = w.Write([]byte(`{"ok":true}`))
		case r.URL.Path == "/push":
			var body map[string]string
			_ = json.NewDecoder(r.Body).Decode(&body)
			if body["token"] != "push-secret" || body["template"] != "txt" {
				t.Fatalf("PushPlus body = %#v", body)
			}
			_, _ = w.Write([]byte(`{"code":200}`))
		case r.URL.Path == "/cgi-bin/stable_token":
			var body struct {
				GrantType    string `json:"grant_type"`
				AppID        string `json:"appid"`
				Secret       string `json:"secret"`
				ForceRefresh bool   `json:"force_refresh"`
			}
			_ = json.NewDecoder(r.Body).Decode(&body)
			if r.Method != http.MethodPost || body.GrantType != "client_credential" || body.AppID != "wx-app-id" || body.Secret != "wx-app-secret" || body.ForceRefresh {
				t.Fatalf("stable token request = headers:%#v body:%#v", r.Header, body)
			}
			_, _ = w.Write([]byte(`{"access_token":"wx-access-token","expires_in":7200}`))
		case r.URL.Path == "/cgi-bin/message/template/send":
			if r.URL.Query().Get("access_token") != "wx-access-token" {
				t.Fatalf("access token query = %q", r.URL.RawQuery)
			}
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			data, _ := body["data"].(map[string]any)
			title, _ := data["title"].(map[string]any)
			content, _ := data["content"].(map[string]any)
			sender, _ := data["sender"].(map[string]any)
			subject, _ := data["subject"].(map[string]any)
			messageBody, _ := data["body"].(map[string]any)
			if r.Method != http.MethodPost || body["touser"] != "wx-open-id" || body["template_id"] != "wx-template-id" ||
				title["value"] != "Mail" || content["value"] != "Test content" || sender["value"] != "sender@example.com" ||
				subject["value"] != "Test subject" || messageBody["value"] != "Test body" {
				t.Fatalf("WXPush template request = headers:%#v body:%#v", r.Header, body)
			}
			if _, ok := body["url"]; ok {
				t.Fatalf("WXPush template request contains url: %#v", body)
			}
			_, _ = w.Write([]byte(`{"errcode":0,"errmsg":"ok"}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	service := &Service{httpClient: server.Client(), telegramBaseURL: server.URL, pushPlusURL: server.URL + "/push", wxPushBaseURL: server.URL, now: func() time.Time { return now }}
	payload := deliveryPayload{
		EventType: "message", Title: "Mail", Text: "Test content", Sender: "sender@example.com",
		Subject: "Test subject", Body: "Test body",
	}

	if err := service.sendTelegram(context.Background(), payload, channelSecret{TelegramBotToken: "bot-secret", TelegramChatID: "12345"}); err != nil {
		t.Fatalf("sendTelegram() error = %v", err)
	}
	if err := service.sendPushPlus(context.Background(), payload, channelSecret{PushPlusToken: "push-secret"}); err != nil {
		t.Fatalf("sendPushPlus() error = %v", err)
	}
	if err := service.sendWXPush(context.Background(), payload, channelSecret{
		WXPushAppID: "wx-app-id", WXPushAppSecret: "wx-app-secret", WXPushUserID: "wx-open-id", WXPushTemplateID: "wx-template-id",
	}); err != nil {
		t.Fatalf("sendWXPush() error = %v", err)
	}
}

func TestMailPayloadIncludesBodyPreview(t *testing.T) {
	payload := mailPayload(messageContext{
		AccountPublicID: "acc_1", Category: "important", SenderAddress: "sender@example.com",
		Subject: "Payment received", Body: strings.Repeat("正文", 300),
		ReceivedAt: time.Date(2026, time.August, 21, 9, 0, 0, 0, time.UTC),
	}, false)

	if payload.Sender != "sender@example.com" || payload.Subject != "Payment received" {
		t.Fatalf("mail payload identity = sender:%q subject:%q", payload.Sender, payload.Subject)
	}
	if payload.Body == "" || !strings.HasSuffix(payload.Body, "…") || len([]rune(payload.Body)) != 501 {
		t.Fatalf("mail payload body preview = %q", payload.Body)
	}
	if !strings.Contains(payload.Text, "正文："+payload.Body) {
		t.Fatalf("mail payload text = %q", payload.Text)
	}
}

func TestMailPayloadKeepsUnicodeFieldsValidWhenTruncated(t *testing.T) {
	payload := mailPayload(messageContext{
		Category: "normal", SenderAddress: strings.Repeat("发", 200), Subject: strings.Repeat("主题", 200),
		Body: "正文",
	}, false)

	if !utf8.ValidString(payload.Sender) || !utf8.ValidString(payload.Subject) {
		t.Fatalf("truncated notification fields are not valid UTF-8: sender=%q subject=%q", payload.Sender, payload.Subject)
	}
	if len([]rune(payload.Sender)) != 160 || len([]rune(payload.Subject)) != 240 {
		t.Fatalf("truncated notification field lengths = sender:%d subject:%d", len([]rune(payload.Sender)), len([]rune(payload.Subject)))
	}
}

