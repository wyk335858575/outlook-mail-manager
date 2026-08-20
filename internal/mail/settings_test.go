package mail

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestSettingsPersistAndAffectMailLimits(t *testing.T) {
	service, store, accountID, folders := newSyncTestService(t)
	defer store.Close()

	defaults, err := service.GetSettings(context.Background())
	if err != nil {
		t.Fatalf("GetSettings() error = %v", err)
	}
	if defaults != (Settings{
		SyncIntervalSeconds: 5,
		InitialSyncDays:     30,
		BodyCacheKiB:        256,
		MessagePageSize:     100,
		Timezone:            "Asia/Shanghai",
		ReaderMode:          "text",
		MarkReadOnOpen:      true,
		DefaultFolder:       "all",
		DefaultUnreadOnly:   false,
		AutoSelectFirst:     true,
		ShowBodyPreview:     true,
		UpdatedAt:           defaults.UpdatedAt,
	}) {
		t.Fatalf("default settings = %#v", defaults)
	}

	updated := defaults
	updated.SyncIntervalSeconds = 1800
	updated.InitialSyncDays = 7
	updated.BodyCacheKiB = 64
	updated.MessagePageSize = 50
	updated.Timezone = "UTC"
	updated.ReaderMode = "html"
	updated.MarkReadOnOpen = false
	updated.DefaultFolder = "inbox"
	updated.DefaultUnreadOnly = true
	updated.AutoSelectFirst = false
	updated.ShowBodyPreview = false
	saved, err := service.UpdateSettings(context.Background(), updated)
	if err != nil {
		t.Fatalf("UpdateSettings() error = %v", err)
	}
	if saved.SyncIntervalSeconds != 1800 || saved.InitialSyncDays != 7 || saved.BodyCacheKiB != 64 || saved.MarkReadOnOpen ||
		saved.DefaultFolder != "inbox" || !saved.DefaultUnreadOnly || saved.AutoSelectFirst || saved.ShowBodyPreview {
		t.Fatalf("saved settings = %#v", saved)
	}
	if window := service.schedulerCycleWindow(context.Background()); window != 30*time.Minute {
		t.Fatalf("scheduler window = %v", window)
	}

	message := graphMessage{ID: "immutable-settings", Subject: "Large body", ReceivedDateTime: "2026-08-17T10:00:00Z"}
	message.Body.ContentType = "text"
	message.Body.Content = strings.Repeat("a", 80<<10)
	if err := service.applyPage(context.Background(), accountID, folders[0], []graphMessage{message}, false, ""); err != nil {
		t.Fatalf("applyPage() error = %v", err)
	}
	var body string
	var truncated bool
	if err := store.DB.QueryRow(
		"SELECT body_text, body_truncated FROM messages WHERE immutable_id = ?", message.ID,
	).Scan(&body, &truncated); err != nil {
		t.Fatalf("load cached body: %v", err)
	}
	if len(body) != 64<<10 || !truncated {
		t.Fatalf("cached body bytes = %d, truncated = %v", len(body), truncated)
	}

	if err := service.resetCursor(context.Background(), folders[0].id); err != nil {
		t.Fatalf("resetCursor() error = %v", err)
	}
	var windowStart string
	if err := store.DB.QueryRow(
		"SELECT initial_window_start_utc FROM sync_cursors WHERE folder_id = ?", folders[0].id,
	).Scan(&windowStart); err != nil {
		t.Fatalf("load reset cursor: %v", err)
	}
	wantStart := formatTime(service.now().UTC().Add(-7 * 24 * time.Hour))
	if windowStart != wantStart {
		t.Fatalf("window start = %q, want %q", windowStart, wantStart)
	}
}

func TestSettingsRejectInvalidValues(t *testing.T) {
	service, store, _, _ := newSyncTestService(t)
	defer store.Close()

	settings, err := service.GetSettings(context.Background())
	if err != nil {
		t.Fatalf("GetSettings() error = %v", err)
	}
	settings.SyncIntervalSeconds = 11
	if _, err := service.UpdateSettings(context.Background(), settings); !errors.Is(err, ErrInvalidSettings) {
		t.Fatalf("invalid interval error = %v", err)
	}
	settings = DefaultSettings()
	settings.Timezone = "Mars/Olympus"
	if _, err := service.UpdateSettings(context.Background(), settings); !errors.Is(err, ErrInvalidSettings) {
		t.Fatalf("invalid timezone error = %v", err)
	}
	settings = DefaultSettings()
	settings.DefaultFolder = "deleteditems"
	if _, err := service.UpdateSettings(context.Background(), settings); !errors.Is(err, ErrInvalidSettings) {
		t.Fatalf("invalid default folder error = %v", err)
	}
}

func TestSettingsAllowFiveSecondBestEffortSync(t *testing.T) {
	service, store, _, _ := newSyncTestService(t)
	defer store.Close()
	service.ctx = context.Background()
	service.settingsChanged = make(chan struct{}, 1)

	settings, err := service.GetSettings(context.Background())
	if err != nil {
		t.Fatalf("GetSettings() error = %v", err)
	}
	settings.SyncIntervalSeconds = 5
	if _, err := service.UpdateSettings(context.Background(), settings); err != nil {
		t.Fatalf("UpdateSettings(5 seconds) error = %v", err)
	}
	if window := service.schedulerCycleWindow(context.Background()); window != 5*time.Second {
		t.Fatalf("scheduler window = %v, want 5s", window)
	}
	if changed, stopped := service.waitForSchedule(time.Hour); !changed || stopped {
		t.Fatalf("settings change signal = changed:%v stopped:%v", changed, stopped)
	}
}

func TestBackgroundQueueDeduplicatesAccount(t *testing.T) {
	service, store, _, _ := newSyncTestService(t)
	defer store.Close()
	service.ctx = context.Background()
	service.background = make(chan syncJob, 2)

	if !service.enqueueBackground(42) {
		t.Fatal("first background enqueue was rejected")
	}
	if service.enqueueBackground(42) {
		t.Fatal("duplicate background enqueue was accepted")
	}
	if got := len(service.background); got != 1 {
		t.Fatalf("background queue length = %d, want 1", got)
	}
	service.backgroundJobs.Delete(int64(42))
	if !service.enqueueBackground(42) {
		t.Fatal("account was not enqueueable after completion")
	}
}

func TestScheduledAccountsRespectRetryBackoff(t *testing.T) {
	service, store, accountID, _ := newSyncTestService(t)
	defer store.Close()

	if _, err := store.DB.Exec(`UPDATE accounts SET sync_next_retry_at_utc = ? WHERE id = ?`, formatTime(service.now().Add(time.Hour)), accountID); err != nil {
		t.Fatalf("set future retry: %v", err)
	}
	items, err := service.scheduledAccounts(context.Background(), 5*time.Second)
	if err != nil {
		t.Fatalf("scheduledAccounts(future retry) error = %v", err)
	}
	if len(items) != 0 {
		t.Fatalf("future retry account was scheduled: %#v", items)
	}

	if _, err := store.DB.Exec(`UPDATE accounts SET sync_next_retry_at_utc = ? WHERE id = ?`, formatTime(service.now().Add(-time.Second)), accountID); err != nil {
		t.Fatalf("set elapsed retry: %v", err)
	}
	items, err = service.scheduledAccounts(context.Background(), 5*time.Second)
	if err != nil || len(items) != 1 || items[0].id != accountID {
		t.Fatalf("scheduledAccounts(elapsed retry) = %#v, %v", items, err)
	}
}

func TestSyncOffsetsSpreadThousandAccountsAcrossCycle(t *testing.T) {
	const accountCount = 1000
	window := 10 * time.Minute
	quarters := [4]int{}
	unique := make(map[time.Duration]struct{}, accountCount)

	for accountID := int64(1); accountID <= accountCount; accountID++ {
		offset := syncOffset(accountID, window)
		if offset < 0 || offset >= window {
			t.Fatalf("account %d offset = %v, want within [0, %v)", accountID, offset, window)
		}
		unique[offset] = struct{}{}
		quarters[int(offset*4/window)]++
	}

	if len(unique) < 990 {
		t.Fatalf("unique offsets = %d, want at least 990", len(unique))
	}
	for index, count := range quarters {
		if count < 200 || count > 300 {
			t.Fatalf("quarter %d account count = %d, want between 200 and 300", index+1, count)
		}
	}
}
