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
	UpdateCommand    string    `json:"update_command,omitempty"`
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
		// A release check must not turn the whole health page into a 500. The
		// application can still report its current version and an actionable
		// reason when GitHub is temporarily unreachable or rate-limited.
		status.Reason = "无法检查 GitHub Releases：" + err.Error()
		return status, nil
	}
	status.LatestVersion = strings.TrimPrefix(strings.TrimSpace(release.TagName), "v")
	status.ReleaseNotes = release.Body
	status.ReleaseURL = release.HTMLURL
	status.UpdateAvailable = versionGreater(status.LatestVersion, status.CurrentVersion)
	status.UpdateCommand = "curl -fsSL https://github.com/" + s.updateRepository + "/releases/latest/download/update.sh | bash"
	if status.UpdateAvailable {
		status.Reason = "请在宝塔 root 终端执行单次升级脚本"
	} else {
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
	release, err := s.latestReleaseAPI(ctx)
	if err == nil {
		return release, nil
	}
	// The API is rate-limited more aggressively than the public latest-release
	// redirect. Use the redirect as a read-only fallback for the default GitHub
	// endpoint; custom endpoints remain deterministic for tests and operators.
	if s.githubAPIBaseURL != "" && s.githubAPIBaseURL != "https://api.github.com" {
		return githubRelease{}, err
	}
	fallback, fallbackErr := s.latestReleaseRedirect(ctx)
	if fallbackErr == nil {
		return fallback, nil
	}
	return githubRelease{}, fmt.Errorf("%w; latest-release fallback failed: %v", err, fallbackErr)
}

func (s *Service) latestReleaseAPI(ctx context.Context) (githubRelease, error) {
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

func (s *Service) latestReleaseRedirect(ctx context.Context) (githubRelease, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://github.com/"+s.updateRepository+"/releases/latest", nil)
	if err != nil {
		return githubRelease{}, err
	}
	client := s.httpClient
	if client == nil {
		client = &http.Client{Timeout: 10 * time.Second}
	}
	redirectClient := *client
	redirectClient.CheckRedirect = func(_ *http.Request, _ []*http.Request) error {
		return http.ErrUseLastResponse
	}
	response, err := redirectClient.Do(request)
	if err != nil {
		return githubRelease{}, fmt.Errorf("request latest-release redirect: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode < http.StatusMultipleChoices || response.StatusCode >= http.StatusBadRequest {
		return githubRelease{}, fmt.Errorf("latest-release redirect returned status %d", response.StatusCode)
	}
	location := strings.TrimSpace(response.Header.Get("Location"))
	parsed, err := url.Parse(location)
	if err != nil || parsed.Scheme != "https" || parsed.Host != "github.com" || parsed.RawQuery != "" || parsed.Fragment != "" {
		return githubRelease{}, errors.New("latest-release redirect location is invalid")
	}
	prefix := "/" + s.updateRepository + "/releases/tag/"
	if !strings.HasPrefix(parsed.Path, prefix) {
		return githubRelease{}, errors.New("latest-release redirect repository does not match")
	}
	tag, err := url.PathUnescape(strings.TrimPrefix(parsed.Path, prefix))
	if err != nil || !stableUpdateTagPattern.MatchString(tag) {
		return githubRelease{}, errors.New("latest-release redirect is not a stable 1.0.N version")
	}
	return githubRelease{TagName: tag, HTMLURL: parsed.String()}, nil
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

