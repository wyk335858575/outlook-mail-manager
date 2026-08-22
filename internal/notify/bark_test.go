package notify

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestBarkSendsV2JSONPayload(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/push" || r.Method != http.MethodPost {
			t.Fatalf("Bark request = %s %s", r.Method, r.URL.Path)
		}
		_, _ = w.Write([]byte(`{"code":200,"message":"success"}`))
	}))
	defer server.Close()
	service := &Service{httpClient: server.Client()}
	if err := service.sendBark(context.Background(), deliveryPayload{Title: "Mail", Text: "Content"}, channelSecret{BarkServerURL: server.URL, BarkDeviceKey: "device-key"}); err != nil {
		t.Fatalf("sendBark() error = %v", err)
	}
}

func TestBarkClassifiesNetworkAndAPIFailures(t *testing.T) {
	for _, test := range []struct {
		name   string
		body   string
		status int
		want   error
	}{
		{name: "api", body: `{"code":400,"message":"bad device key"}`, status: http.StatusOK, want: ErrBarkAPI},
		{name: "http api", body: `{"code":400,"message":"bad device key"}`, status: http.StatusBadRequest, want: ErrBarkAPI},
		{name: "invalid json", body: `not-json`, status: http.StatusOK, want: ErrBarkNetwork},
	} {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(test.status)
				_, _ = w.Write([]byte(test.body))
			}))
			defer server.Close()
			service := &Service{httpClient: server.Client()}
			err := service.sendBark(context.Background(), deliveryPayload{Title: "Mail", Text: "Content"}, channelSecret{BarkServerURL: server.URL, BarkDeviceKey: "device-key"})
			if !errors.Is(err, test.want) {
				t.Fatalf("sendBark() error = %v, want %v", err, test.want)
			}
		})
	}
}

func TestBarkSecretValidation(t *testing.T) {
	valid := channelSecret{BarkServerURL: "https://bark.example.com", BarkDeviceKey: "device-key"}
	if err := validateBarkSecret(valid); err != nil {
		t.Fatalf("valid Bark secret rejected: %v", err)
	}
	for _, secret := range []channelSecret{
		{BarkServerURL: "", BarkDeviceKey: "device-key"},
		{BarkServerURL: "ftp://bark.example.com", BarkDeviceKey: "device-key"},
		{BarkServerURL: "https://bark.example.com?token=secret", BarkDeviceKey: "device-key"},
		{BarkServerURL: "https://bark.example.com", BarkDeviceKey: "device key"},
	} {
		if err := validateBarkSecret(secret); !errors.Is(err, ErrInvalidChannel) {
			t.Fatalf("invalid Bark secret accepted: %+v", secret)
		}
	}
}
