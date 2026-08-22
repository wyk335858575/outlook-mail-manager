package maintenance

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"outlook-mail-manager/internal/database"
)

func TestUpdateStatusReadsLatestStableReleaseWithoutEnablingMissingHelper(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/repos/owner/outlook-mail-manager/releases/latest" {
			t.Fatalf("unexpected path %q", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"tag_name":"v1.1.0","body":"Security fixes","html_url":"https://github.com/owner/outlook-mail-manager/releases/tag/v1.1.0"}`))
	}))
	defer server.Close()
	store, err := database.Open(context.Background(), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	service, err := New(store.DB, t.TempDir(), Options{
		Version: "0.11.0", UpdateRepository: "owner/outlook-mail-manager", UpdateImage: "ghcr.io/owner/outlook-mail-manager",
		UpdateSocket: t.TempDir() + "/missing.sock", GitHubAPIBaseURL: server.URL, HTTPClient: server.Client(),
		Now: func() time.Time { return time.Date(2026, 8, 19, 4, 0, 0, 0, time.UTC) },
	})
	if err != nil {
		t.Fatal(err)
	}
	status, err := service.UpdateStatus(context.Background())
	if err != nil {
		t.Fatalf("UpdateStatus() error = %v", err)
	}
	if status.CurrentVersion != "0.11.0" || status.LatestVersion != "1.1.0" || !status.UpdateAvailable {
		t.Fatalf("status = %+v", status)
	}
	if status.UpdaterAvailable || status.CanUpdate {
		t.Fatalf("missing updater unexpectedly enabled update: %+v", status)
	}
	if status.UpdateCommand != "curl -fsSL https://github.com/owner/outlook-mail-manager/releases/latest/download/update.sh | bash" {
		t.Fatalf("update command = %q", status.UpdateCommand)
	}
}

func TestUpdateStatusRejectsPrereleaseAndUnexpectedVersionLine(t *testing.T) {
	for _, body := range []string{
		`{"tag_name":"v1.0.1","prerelease":true}`,
		`{"tag_name":"v2.0.0"}`,
		`{"tag_name":"v1.0.01"}`,
	} {
		t.Run(body, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(body))
			}))
			defer server.Close()
			store, err := database.Open(context.Background(), t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			defer store.Close()
			service, err := New(store.DB, t.TempDir(), Options{Version: "1.0.0", UpdateRepository: "owner/repo", UpdateImage: "ghcr.io/owner/repo", GitHubAPIBaseURL: server.URL, HTTPClient: server.Client()})
			if err != nil {
				t.Fatal(err)
			}
			status, err := service.UpdateStatus(context.Background())
			if err != nil {
				t.Fatalf("UpdateStatus() returned an error: %v", err)
			}
			if status.LatestVersion != "" || !strings.Contains(status.Reason, "无法检查 GitHub Releases") {
				t.Fatalf("unsupported release was not reported safely: %+v", status)
			}
		})
	}
}

func TestUpdateStatusReturnsActionableReasonWhenGitHubIsUnavailable(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer server.Close()
	store, err := database.Open(context.Background(), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	service, err := New(store.DB, t.TempDir(), Options{
		Version: "1.0.8", UpdateRepository: "owner/repo", UpdateImage: "ghcr.io/owner/repo",
		GitHubAPIBaseURL: server.URL, HTTPClient: server.Client(),
	})
	if err != nil {
		t.Fatal(err)
	}
	status, err := service.UpdateStatus(context.Background())
	if err != nil {
		t.Fatalf("UpdateStatus() returned an error: %v", err)
	}
	if !status.Configured || status.LatestVersion != "" || !strings.Contains(status.Reason, "无法检查 GitHub Releases") {
		t.Fatalf("GitHub failure was not reported safely: %+v", status)
	}
}

func TestVersionGreaterUsesSemanticComponents(t *testing.T) {
	if !versionGreater("0.11.0", "0.10.9") || !versionGreater("1.1.0", "1.0.9") || versionGreater("0.10.9", "0.11.0") || versionGreater("bad", "0.11.0") {
		t.Fatal("version comparison returned an unexpected result")
	}
}
