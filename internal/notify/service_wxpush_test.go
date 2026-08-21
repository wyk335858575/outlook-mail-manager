package notify

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"outlook-mail-manager/internal/database"
	"outlook-mail-manager/internal/datakey"
)

func TestWXPushCachesTokenUntilEarlyExpiry(t *testing.T) {
	now := time.Date(2026, time.August, 20, 8, 0, 0, 0, time.UTC)
	var tokenRequests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/cgi-bin/stable_token":
			request := tokenRequests.Add(1)
			_, _ = w.Write([]byte(`{"access_token":"token-` + string(rune('0'+request)) + `","expires_in":301}`))
		case "/cgi-bin/message/template/send":
			_, _ = w.Write([]byte(`{"errcode":0,"errmsg":"ok"}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	service := &Service{httpClient: server.Client(), wxPushBaseURL: server.URL, now: func() time.Time { return now }}
	secret := testWXPushSecret()
	payload := deliveryPayload{Title: "Test", Text: "Content"}

	if err := service.sendWXPush(context.Background(), payload, secret); err != nil {
		t.Fatalf("first sendWXPush() error = %v", err)
	}
	if err := service.sendWXPush(context.Background(), payload, secret); err != nil {
		t.Fatalf("cached sendWXPush() error = %v", err)
	}
	if tokenRequests.Load() != 1 {
		t.Fatalf("stable token requests = %d, want 1", tokenRequests.Load())
	}
	now = now.Add(2 * time.Second)
	if err := service.sendWXPush(context.Background(), payload, secret); err != nil {
		t.Fatalf("expired sendWXPush() error = %v", err)
	}
	if tokenRequests.Load() != 2 {
		t.Fatalf("stable token requests after early expiry = %d, want 2", tokenRequests.Load())
	}
}

func TestWXPushInvalidTokenRefreshesAndRetriesOnce(t *testing.T) {
	var tokenRequests atomic.Int32
	var sendRequests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/cgi-bin/stable_token":
			request := tokenRequests.Add(1)
			_, _ = w.Write([]byte(`{"access_token":"token-` + string(rune('0'+request)) + `","expires_in":7200}`))
		case "/cgi-bin/message/template/send":
			sendRequests.Add(1)
			if r.URL.Query().Get("access_token") == "token-1" {
				_, _ = w.Write([]byte(`{"errcode":40014,"errmsg":"invalid access_token"}`))
				return
			}
			_, _ = w.Write([]byte(`{"errcode":0,"errmsg":"ok"}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	service := &Service{httpClient: server.Client(), wxPushBaseURL: server.URL, now: time.Now}

	if err := service.sendWXPush(context.Background(), deliveryPayload{Title: "Test", Text: "Content"}, testWXPushSecret()); err != nil {
		t.Fatalf("sendWXPush() error = %v", err)
	}
	if tokenRequests.Load() != 2 || sendRequests.Load() != 2 {
		t.Fatalf("requests = token:%d send:%d, want 2/2", tokenRequests.Load(), sendRequests.Load())
	}
}

func TestWXPushInvalidTokenStopsAfterOneRetry(t *testing.T) {
	var tokenRequests atomic.Int32
	var sendRequests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/cgi-bin/stable_token":
			tokenRequests.Add(1)
			_, _ = w.Write([]byte(`{"access_token":"invalid-token","expires_in":7200}`))
		case "/cgi-bin/message/template/send":
			sendRequests.Add(1)
			_, _ = w.Write([]byte(`{"errcode":42001,"errmsg":"access_token expired"}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	service := &Service{httpClient: server.Client(), wxPushBaseURL: server.URL, now: time.Now}

	err := service.sendWXPush(context.Background(), deliveryPayload{Title: "Test", Text: "Content"}, testWXPushSecret())
	if !errors.Is(err, ErrWXPushAPI) {
		t.Fatalf("sendWXPush() error = %v, want ErrWXPushAPI", err)
	}
	if tokenRequests.Load() != 2 || sendRequests.Load() != 2 {
		t.Fatalf("requests = token:%d send:%d, want 2/2", tokenRequests.Load(), sendRequests.Load())
	}
}

func TestWXPushConcurrentSendsShareTokenRefresh(t *testing.T) {
	var tokenRequests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/cgi-bin/stable_token":
			tokenRequests.Add(1)
			time.Sleep(30 * time.Millisecond)
			_, _ = w.Write([]byte(`{"access_token":"shared-token","expires_in":7200}`))
		case "/cgi-bin/message/template/send":
			_, _ = w.Write([]byte(`{"errcode":0,"errmsg":"ok"}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	service := &Service{httpClient: server.Client(), wxPushBaseURL: server.URL, now: time.Now}
	var wait sync.WaitGroup
	errorsFound := make(chan error, 8)
	for index := 0; index < 8; index++ {
		wait.Add(1)
		go func() {
			defer wait.Done()
			errorsFound <- service.sendWXPush(context.Background(), deliveryPayload{Title: "Test", Text: "Content"}, testWXPushSecret())
		}()
	}
	wait.Wait()
	close(errorsFound)
	for err := range errorsFound {
		if err != nil {
			t.Fatalf("concurrent sendWXPush() error = %v", err)
		}
	}
	if tokenRequests.Load() != 1 {
		t.Fatalf("stable token requests = %d, want 1", tokenRequests.Load())
	}
}

func TestWXPushConcurrentInvalidTokenRefreshesOnce(t *testing.T) {
	var tokenRequests atomic.Int32
	var oldTokenRequests atomic.Int32
	var releaseOld sync.Once
	oldRequestsReady := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/cgi-bin/stable_token":
			request := tokenRequests.Add(1)
			if request == 1 {
				_, _ = w.Write([]byte(`{"access_token":"old-token","expires_in":7200}`))
				return
			}
			_, _ = w.Write([]byte(`{"access_token":"new-token","expires_in":7200}`))
		case "/cgi-bin/message/template/send":
			if r.URL.Query().Get("access_token") == "old-token" {
				if oldTokenRequests.Add(1) == 8 {
					releaseOld.Do(func() { close(oldRequestsReady) })
				}
				<-oldRequestsReady
				_, _ = w.Write([]byte(`{"errcode":40014,"errmsg":"invalid access_token"}`))
				return
			}
			_, _ = w.Write([]byte(`{"errcode":0,"errmsg":"ok"}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	service := &Service{httpClient: server.Client(), wxPushBaseURL: server.URL, now: time.Now}
	var wait sync.WaitGroup
	errorsFound := make(chan error, 8)
	for index := 0; index < 8; index++ {
		wait.Add(1)
		go func() {
			defer wait.Done()
			errorsFound <- service.sendWXPush(context.Background(), deliveryPayload{Title: "Test", Text: "Content"}, testWXPushSecret())
		}()
	}
	wait.Wait()
	close(errorsFound)
	for err := range errorsFound {
		if err != nil {
			t.Fatalf("concurrent retry sendWXPush() error = %v", err)
		}
	}
	if tokenRequests.Load() != 2 {
		t.Fatalf("stable token requests = %d, want 2", tokenRequests.Load())
	}
}

func TestWXPushErrorClassification(t *testing.T) {
	for _, test := range []struct {
		code int
		want error
	}{
		{40003, ErrWXPushUser},
		{43004, ErrWXPushUser},
		{40037, ErrWXPushTemplate},
		{47003, ErrWXPushTemplate},
		{45009, ErrWXPushAPI},
	} {
		if err := wxPushSendError(test.code); !errors.Is(err, test.want) {
			t.Fatalf("wxPushSendError(%d) = %v, want %v", test.code, err, test.want)
		}
	}
}

func TestWXPushRequiresSingleOpenID(t *testing.T) {
	input := ChannelInput{Name: "WXPush", Kind: "wxpush"}
	if err := validateChannel(input, testWXPushSecret()); err != nil {
		t.Fatalf("validateChannel(single OpenID) error = %v", err)
	}
	secret := testWXPushSecret()
	secret.WXPushUserID = "openid-one|openid-two"
	if err := validateChannel(input, secret); !errors.Is(err, ErrInvalidChannel) {
		t.Fatalf("validateChannel(multiple OpenIDs) error = %v, want ErrInvalidChannel", err)
	}
}

func TestWXPushSecretsStayEncryptedAndLegacyChannelsAreMarked(t *testing.T) {
	store, err := database.Open(context.Background(), t.TempDir())
	if err != nil {
		t.Fatalf("database.Open() error = %v", err)
	}
	defer store.Close()
	keyring := datakey.New(nil)
	if err := keyring.Unlock(make([]byte, 32)); err != nil {
		t.Fatalf("unlock data key: %v", err)
	}
	service, err := New(store.DB, keyring, Options{})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	direct, err := service.CreateChannel(context.Background(), ChannelInput{
		Name: "Direct WXPush", Kind: "wxpush", Enabled: true,
		WXPushAppID: "secret-app-id", WXPushAppSecret: "secret-app-secret", WXPushUserID: "secret-open-id", WXPushTemplateID: "secret-template-id",
	})
	if err != nil {
		t.Fatalf("CreateChannel() error = %v", err)
	}
	var cipher string
	if err := store.DB.QueryRow(`SELECT config_ciphertext FROM notification_channels WHERE public_id = ?`, direct.PublicID).Scan(&cipher); err != nil {
		t.Fatalf("load ciphertext: %v", err)
	}
	for _, secret := range []string{"secret-app-secret", "secret-open-id"} {
		if strings.Contains(cipher, secret) {
			t.Fatalf("ciphertext contains %q", secret)
		}
	}

	legacyID := "channel_legacy_wxpush"
	legacyCipher, err := service.sealChannel(legacyID, channelSecret{WXPushURL: "https://legacy.example/wxsend", WXPushToken: "legacy-token"})
	if err != nil {
		t.Fatalf("seal legacy channel: %v", err)
	}
	now := formatTime(time.Now().UTC())
	if _, err := store.DB.Exec(`INSERT INTO notification_channels (
		public_id, name, kind, config_ciphertext, enabled, system_enabled, created_at_utc, updated_at_utc
	) VALUES (?, 'Legacy WXPush', 'wxpush', ?, 0, 0, ?, ?)`, legacyID, legacyCipher, now, now); err != nil {
		t.Fatalf("insert legacy channel: %v", err)
	}
	channels, err := service.ListChannels(context.Background())
	if err != nil {
		t.Fatalf("ListChannels() error = %v", err)
	}
	encoded, err := json.Marshal(channels)
	if err != nil {
		t.Fatalf("json.Marshal() error = %v", err)
	}
	for _, secret := range []string{"secret-app-secret", "secret-open-id", "legacy-token", "wx-access-token"} {
		if strings.Contains(string(encoded), secret) {
			t.Fatalf("channel response contains %q: %s", secret, encoded)
		}
	}
	for _, channel := range channels {
		if channel.PublicID == legacyID {
			if !channel.NeedsReconfiguration || channel.Destination != "旧版配置，请删除后重建" {
				t.Fatalf("legacy channel = %+v", channel)
			}
			return
		}
	}
	t.Fatal("legacy channel was not listed")
}

func testWXPushSecret() channelSecret {
	return channelSecret{
		WXPushAppID: "app-id", WXPushAppSecret: "app-secret", WXPushUserID: "open-id", WXPushTemplateID: "template-id",
	}
}
