package maintenance

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"outlook-mail-manager/internal/appversion"
)

var updateRepositoryPattern = regexp.MustCompile(`^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$`)
var stableUpdateTagPattern = regexp.MustCompile(`^v1\.0\.(0|[1-9][0-9]*)$`)

type UpdateStatus struct {
	CurrentVersion   string    `json:"current_version"`
	LatestVersion    string    `json:"latest_version,omitempty"`
	ReleaseNotes     string    `json:"release_notes,omitempty"`
	ReleaseURL       string    `json:"release_url,omitempty"`
	CheckedAt        time.Time `json:"checked_at"`
	Configured       bool      `json:"configured"`
	UpdateAvailable  bool      `json:"update_available"`
	UpdaterAvailable bool      `json:"updater_available"`
	CanUpdate        bool      `json:"can_update"`
	Reason           string    `json:"reason,omitempty"`
}

type UpdateJob struct {
	ID          string     `json:"id"`
	State       string     `json:"state"`
	Version     string     `json:"version,omitempty"`
	Message     string     `json:"message,omitempty"`
	Error       string     `json:"error,omitempty"`
	CreatedAt   time.Time  `json:"created_at"`
	UpdatedAt   time.Time  `json:"updated_at"`
	CompletedAt *time.Time `json:"completed_at,omitempty"`
}

type githubRelease struct {
	TagName    string `json:"tag_name"`
	Body       string `json:"body"`
	HTMLURL    string `json:"html_url"`
	Draft      bool   `json:"draft"`
	Prerelease bool   `json:"prerelease"`
}

func (s *Service) UpdateStatus(ctx context.Context) (UpdateStatus, error) {
	status := UpdateStatus{CurrentVersion: displayVersion(s.version), CheckedAt: s.now().UTC()}
	if !updateRepositoryPattern.MatchString(s.updateRepository) || strings.TrimSpace(s.updateImage) == "" {
		status.Reason = "未配置 APP_UPDATE_REPOSITORY 和 APP_IMAGE"
		return status, nil
	}
	status.Configured = true
	release, err := s.latestRelease(ctx)
	if err != nil {
		return UpdateStatus{}, err
	}
	status.LatestVersion = strings.TrimPrefix(strings.TrimSpace(release.TagName), "v")
	status.ReleaseNotes = release.Body
	status.ReleaseURL = release.HTMLURL
	status.UpdateAvailable = versionGreater(status.LatestVersion, status.CurrentVersion)
	status.UpdaterAvailable = s.updaterAvailable(ctx)
	status.CanUpdate = status.UpdateAvailable && status.UpdaterAvailable
	if !status.UpdaterAvailable {
		status.Reason = "宿主机更新助手未安装或 Unix Socket 不可用"
	} else if !status.UpdateAvailable {
		status.Reason = "当前已是最新稳定版"
	}
	return status, nil
}

func (s *Service) StartUpdate(ctx context.Context) (UpdateJob, error) {
	if !updateRepositoryPattern.MatchString(s.updateRepository) || strings.TrimSpace(s.updateImage) == "" {
		return UpdateJob{}, errors.New("online update is not configured")
	}
	var job UpdateJob
	if err := s.updaterRequest(ctx, http.MethodPost, "/v1/jobs", &job); err != nil {
		return UpdateJob{}, err
	}
	_ = s.recordAudit(ctx, "update.started", job.ID, map[string]any{"job": job.ID})
	return job, nil
}

func (s *Service) GetUpdateJob(ctx context.Context, id string) (UpdateJob, error) {
	if !regexp.MustCompile(`^[A-Za-z0-9_-]{8,128}$`).MatchString(strings.TrimSpace(id)) {
		return UpdateJob{}, errors.New("invalid update job id")
	}
	var job UpdateJob
	if err := s.updaterRequest(ctx, http.MethodGet, "/v1/jobs/"+url.PathEscape(id), &job); err != nil {
		return UpdateJob{}, err
	}
	return job, nil
}

func (s *Service) latestRelease(ctx context.Context) (githubRelease, error) {
	base := s.githubAPIBaseURL
	if base == "" {
		base = "https://api.github.com"
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/repos/"+s.updateRepository+"/releases/latest", nil)
	if err != nil {
		return githubRelease{}, err
	}
	request.Header.Set("Accept", "application/vnd.github+json")
	request.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	client := s.httpClient
	if client == nil {
		client = &http.Client{Timeout: 10 * time.Second}
	}
	response, err := client.Do(request)
	if err != nil {
		return githubRelease{}, fmt.Errorf("check latest GitHub release: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		io.Copy(io.Discard, io.LimitReader(response.Body, 4096))
		return githubRelease{}, fmt.Errorf("check latest GitHub release: status %d", response.StatusCode)
	}
	var release githubRelease
	if err := json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(&release); err != nil {
		return githubRelease{}, fmt.Errorf("decode latest GitHub release: %w", err)
	}
	if release.Draft || release.Prerelease || !stableUpdateTagPattern.MatchString(strings.TrimSpace(release.TagName)) {
		return githubRelease{}, errors.New("latest GitHub release is not a stable 1.0.N version")
	}
	return release, nil
}

func (s *Service) updaterAvailable(ctx context.Context) bool {
	if s.updateSocket == "" {
		return false
	}
	var response map[string]any
	return s.updaterRequest(ctx, http.MethodGet, "/v1/status", &response) == nil
}

func (s *Service) updaterRequest(ctx context.Context, method, path string, target any) error {
	if strings.TrimSpace(s.updateSocket) == "" {
		return errors.New("updater socket is not configured")
	}
	transport := &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			var dialer net.Dialer
			return dialer.DialContext(ctx, "unix", s.updateSocket)
		},
	}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: 10 * time.Second}
	request, err := http.NewRequestWithContext(ctx, method, "http://updater"+path, nil)
	if err != nil {
		return err
	}
	response, err := client.Do(request)
	if err != nil {
		return fmt.Errorf("contact host updater: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		io.Copy(io.Discard, io.LimitReader(response.Body, 4096))
		return fmt.Errorf("host updater returned status %d", response.StatusCode)
	}
	if target == nil || response.StatusCode == http.StatusNoContent {
		return nil
	}
	if err := json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(target); err != nil {
		return fmt.Errorf("decode host updater response: %w", err)
	}
	return nil
}

func displayVersion(value string) string {
	value = strings.TrimPrefix(strings.TrimSpace(value), "v")
	if value == "" {
		return "dev"
	}
	return value
}

func versionGreater(latest, current string) bool {
	return appversion.Greater(latest, current)
}
