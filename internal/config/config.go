package config

import (
	"fmt"
	"net"
	"net/url"
	"os"
	"strings"
	"time"
)

type Config struct {
	ListenAddr        string
	DataDir           string
	BaseURL           *url.URL
	Timezone          *time.Location
	LogLevel          string
	MicrosoftClientID string
	UpdateRepository  string
	UpdateImage       string
	UpdateSocket      string
}

func Load() (Config, error) {
	return load(os.LookupEnv)
}

func load(lookup func(string) (string, bool)) (Config, error) {
	listenAddr := envOrDefault(lookup, "APP_LISTEN_ADDR", ":8080")
	dataDir := envOrDefault(lookup, "APP_DATA_DIR", "./data/runtime")
	baseURLValue := envOrDefault(lookup, "APP_BASE_URL", "http://localhost:8080")
	timezoneValue := envOrDefault(lookup, "APP_TIMEZONE", "Asia/Shanghai")
	logLevel := strings.ToLower(envOrDefault(lookup, "APP_LOG_LEVEL", "info"))

	baseURL, err := url.Parse(baseURLValue)
	if err != nil || baseURL.Host == "" {
		return Config{}, fmt.Errorf("APP_BASE_URL must be an absolute URL")
	}
	if baseURL.User != nil || baseURL.RawQuery != "" || baseURL.Fragment != "" {
		return Config{}, fmt.Errorf("APP_BASE_URL must not contain credentials, a query, or a fragment")
	}
	if baseURL.Scheme != "https" && !(baseURL.Scheme == "http" && isLoopbackHost(baseURL.Hostname())) {
		return Config{}, fmt.Errorf("APP_BASE_URL must use HTTPS outside local development")
	}

	timezone, err := time.LoadLocation(timezoneValue)
	if err != nil {
		return Config{}, fmt.Errorf("APP_TIMEZONE is invalid: %w", err)
	}

	if logLevel != "debug" && logLevel != "info" && logLevel != "warn" && logLevel != "error" {
		return Config{}, fmt.Errorf("APP_LOG_LEVEL must be debug, info, warn, or error")
	}
	return Config{
		ListenAddr:        listenAddr,
		DataDir:           dataDir,
		BaseURL:           baseURL,
		Timezone:          timezone,
		LogLevel:          logLevel,
		MicrosoftClientID: envOrDefault(lookup, "MS_CLIENT_ID", ""),
		UpdateRepository:  envOrDefault(lookup, "APP_UPDATE_REPOSITORY", ""),
		UpdateImage:       envOrDefault(lookup, "APP_IMAGE", ""),
		UpdateSocket:      envOrDefault(lookup, "APP_UPDATE_SOCKET", "/run/outlook-mail-manager-updater/updater.sock"),
	}, nil
}

func envOrDefault(lookup func(string) (string, bool), key, fallback string) string {
	value, ok := lookup(key)
	if !ok || strings.TrimSpace(value) == "" {
		return fallback
	}
	return strings.TrimSpace(value)
}

func isLoopbackHost(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}
