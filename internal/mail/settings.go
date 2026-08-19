package mail

import (
	"context"
	"errors"
	"fmt"
	"time"
)

const (
	defaultInitialSyncDays = 30
	defaultBodyCacheKiB    = 256
	defaultMessagePageSize = 100
	defaultTimezone        = "Asia/Shanghai"
	defaultReaderMode      = "text"
	maxBodyBytes           = defaultBodyCacheKiB << 10
)

var ErrInvalidSettings = errors.New("invalid settings")

type Settings struct {
	SyncIntervalSeconds int    `json:"sync_interval_seconds"`
	InitialSyncDays     int    `json:"initial_sync_days"`
	BodyCacheKiB        int    `json:"body_cache_kib"`
	MessagePageSize     int    `json:"message_page_size"`
	Timezone            string `json:"timezone"`
	ReaderMode          string `json:"reader_mode"`
	MarkReadOnOpen      bool   `json:"mark_read_on_open"`
	DefaultFolder       string `json:"default_folder"`
	DefaultUnreadOnly   bool   `json:"default_unread_only"`
	AutoSelectFirst     bool   `json:"auto_select_first_message"`
	ShowBodyPreview     bool   `json:"show_body_preview"`
	UpdatedAt           string `json:"updated_at"`
}

func DefaultSettings() Settings {
	return Settings{
		SyncIntervalSeconds: int(defaultSyncWindow / time.Second),
		InitialSyncDays:     defaultInitialSyncDays,
		BodyCacheKiB:        defaultBodyCacheKiB,
		MessagePageSize:     defaultMessagePageSize,
		Timezone:            defaultTimezone,
		ReaderMode:          defaultReaderMode,
		MarkReadOnOpen:      true,
		DefaultFolder:       "all",
		AutoSelectFirst:     true,
		ShowBodyPreview:     true,
	}
}

func (s *Service) GetSettings(ctx context.Context) (Settings, error) {
	var settings Settings
	err := s.db.QueryRowContext(ctx, `
		SELECT sync_interval_seconds, initial_sync_days, body_cache_kib,
			message_page_size, timezone, reader_mode, mark_read_on_open,
			default_folder, default_unread_only, auto_select_first_message, show_body_preview,
			updated_at_utc
		FROM app_settings WHERE id = 1
	`).Scan(
		&settings.SyncIntervalSeconds, &settings.InitialSyncDays, &settings.BodyCacheKiB,
		&settings.MessagePageSize, &settings.Timezone, &settings.ReaderMode,
		&settings.MarkReadOnOpen, &settings.DefaultFolder, &settings.DefaultUnreadOnly,
		&settings.AutoSelectFirst, &settings.ShowBodyPreview, &settings.UpdatedAt,
	)
	if err != nil {
		return Settings{}, fmt.Errorf("load application settings: %w", err)
	}
	return settings, nil
}

func (s *Service) UpdateSettings(ctx context.Context, settings Settings) (Settings, error) {
	if err := validateSettings(settings); err != nil {
		return Settings{}, err
	}
	now := formatTime(s.now().UTC())
	if _, err := s.db.ExecContext(ctx, `
		UPDATE app_settings SET
			sync_interval_seconds = ?, initial_sync_days = ?, body_cache_kib = ?,
			message_page_size = ?, timezone = ?, reader_mode = ?,
			mark_read_on_open = ?, default_folder = ?, default_unread_only = ?,
			auto_select_first_message = ?, show_body_preview = ?, updated_at_utc = ?
		WHERE id = 1
	`,
		settings.SyncIntervalSeconds, settings.InitialSyncDays, settings.BodyCacheKiB,
		settings.MessagePageSize, settings.Timezone, settings.ReaderMode,
		settings.MarkReadOnOpen, settings.DefaultFolder, settings.DefaultUnreadOnly,
		settings.AutoSelectFirst, settings.ShowBodyPreview, now,
	); err != nil {
		return Settings{}, fmt.Errorf("save application settings: %w", err)
	}
	select {
	case s.settingsChanged <- struct{}{}:
	default:
	}
	return s.GetSettings(ctx)
}

func validateSettings(settings Settings) error {
	if !allowedInt(settings.SyncIntervalSeconds, 5, 60, 300, 600, 900, 1800, 3600) {
		return fmt.Errorf("%w: unsupported synchronization interval", ErrInvalidSettings)
	}
	if !allowedInt(settings.InitialSyncDays, 7, 14, 30, 60, 90) {
		return fmt.Errorf("%w: unsupported initial synchronization window", ErrInvalidSettings)
	}
	if !allowedInt(settings.BodyCacheKiB, 64, 128, 256, 512, 1024) {
		return fmt.Errorf("%w: unsupported message body cache limit", ErrInvalidSettings)
	}
	if !allowedInt(settings.MessagePageSize, 25, 50, 100, 200) {
		return fmt.Errorf("%w: unsupported message page size", ErrInvalidSettings)
	}
	if _, err := time.LoadLocation(settings.Timezone); err != nil {
		return fmt.Errorf("%w: invalid timezone", ErrInvalidSettings)
	}
	if settings.ReaderMode != "text" && settings.ReaderMode != "html" {
		return fmt.Errorf("%w: unsupported reader mode", ErrInvalidSettings)
	}
	if settings.DefaultFolder != "all" && settings.DefaultFolder != "inbox" && settings.DefaultFolder != "junkemail" {
		return fmt.Errorf("%w: unsupported default folder", ErrInvalidSettings)
	}
	return nil
}

func allowedInt(value int, allowed ...int) bool {
	for _, candidate := range allowed {
		if value == candidate {
			return true
		}
	}
	return false
}
