package config

import (
	"strings"
	"testing"
)

func TestLoadUsesSecureDefaultsForLocalDevelopment(t *testing.T) {
	values := map[string]string{
		"APP_DATA_DIR": t.TempDir(),
		"MS_CLIENT_ID": " personal-client-id ",
	}

	cfg, err := load(mapLookup(values))
	if err != nil {
		t.Fatalf("load() error = %v", err)
	}

	if cfg.ListenAddr != ":8080" {
		t.Fatalf("ListenAddr = %q, want :8080", cfg.ListenAddr)
	}
	if cfg.BaseURL.String() != "http://localhost:8080" {
		t.Fatalf("BaseURL = %q", cfg.BaseURL)
	}
	if cfg.Timezone.String() != "Asia/Shanghai" {
		t.Fatalf("Timezone = %q", cfg.Timezone)
	}
	if cfg.MicrosoftClientID != "personal-client-id" {
		t.Fatalf("MicrosoftClientID = %q", cfg.MicrosoftClientID)
	}
}

func TestLoadUsesRuntimeDataDirectoryByDefault(t *testing.T) {
	cfg, err := load(mapLookup(nil))
	if err != nil {
		t.Fatalf("load() error = %v", err)
	}
	if cfg.DataDir != "./data/runtime" {
		t.Fatalf("DataDir = %q, want ./data/runtime", cfg.DataDir)
	}
}

func TestLoadRequiresHTTPSOutsideLoopback(t *testing.T) {
	values := map[string]string{
		"APP_DATA_DIR": t.TempDir(),
		"APP_BASE_URL": "http://mail.example.com",
	}

	_, err := load(mapLookup(values))
	if err == nil || !strings.Contains(err.Error(), "HTTPS") {
		t.Fatalf("load() error = %v, want HTTPS error", err)
	}
}

func mapLookup(values map[string]string) func(string) (string, bool) {
	return func(key string) (string, bool) {
		value, ok := values[key]
		return value, ok
	}
}
