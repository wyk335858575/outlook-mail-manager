package notify

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestNotificationProtocolsAndWebhookSignature(t *testing.T) {
	now := time.Date(2026, time.August, 17, 12, 0, 0, 0, time.UTC)
	var webhookBody []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/sendMessage"):
			var body map[string]string
			_ = json.NewDecoder(r.Body).Decode(&body)
			if body["chat_id"] != "12345" || !strings.Contains(body["text"], "Test subject") {
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
		case r.URL.Path == "/webhook":
			webhookBody, _ = io.ReadAll(r.Body)
			if strings.Contains(string(webhookBody), "webhook-secret") {
				t.Fatal("webhook secret leaked into request body")
			}
			timestamp := r.Header.Get("X-Outlook-Manager-Timestamp")
			mac := hmac.New(sha256.New, []byte("webhook-secret"))
			mac.Write([]byte(timestamp + "."))
			mac.Write(webhookBody)
			want := "sha256=" + hex.EncodeToString(mac.Sum(nil))
			if r.Header.Get("X-Outlook-Manager-Signature") != want || r.Header.Get("X-Outlook-Manager-Delivery") != "delivery_1" {
				t.Fatalf("webhook signature headers = %#v", r.Header)
			}
			w.WriteHeader(http.StatusNoContent)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	service := &Service{httpClient: server.Client(), telegramBaseURL: server.URL, pushPlusURL: server.URL + "/push", now: func() time.Time { return now }}
	payload := deliveryPayload{EventType: "message", Title: "Mail", Text: "Test subject", Subject: "Test subject"}

	if err := service.sendTelegram(context.Background(), payload, channelSecret{TelegramBotToken: "bot-secret", TelegramChatID: "12345"}); err != nil {
		t.Fatalf("sendTelegram() error = %v", err)
	}
	if err := service.sendPushPlus(context.Background(), payload, channelSecret{PushPlusToken: "push-secret"}); err != nil {
		t.Fatalf("sendPushPlus() error = %v", err)
	}
	if err := service.sendWebhook(context.Background(), queuedDelivery{publicID: "delivery_1", payload: payload}, channelSecret{WebhookURL: server.URL + "/webhook", WebhookSecret: "webhook-secret"}); err != nil {
		t.Fatalf("sendWebhook() error = %v", err)
	}
	if len(webhookBody) == 0 {
		t.Fatal("webhook body was not delivered")
	}
}
