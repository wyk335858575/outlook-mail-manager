package updater

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"outlook-mail-manager/internal/appversion"
)

type Config struct {
	Repository       string
	Image            string
	DeployDir        string
	StateDir         string
	ComposeService   string
	HealthURL        string
	CosignOIDCIssuer string
	GitHubAPIBaseURL string
	HTTPClient       *http.Client
	RunCommand       func(context.Context, string, ...string) ([]byte, error)
	Now              func() time.Time
}

type Service struct {
	config  Config
	mu      sync.Mutex
	running bool
}

type Job struct {
	ID          string     `json:"id"`
	State       string     `json:"state"`
	Version     string     `json:"version,omitempty"`
	Message     string     `json:"message,omitempty"`
	Error       string     `json:"error,omitempty"`
	CreatedAt   time.Time  `json:"created_at"`
	UpdatedAt   time.Time  `json:"updated_at"`
	CompletedAt *time.Time `json:"completed_at,omitempty"`
}

type release struct {
	TagName    string `json:"tag_name"`
	Draft      bool   `json:"draft"`
	Prerelease bool   `json:"prerelease"`
	Assets     []struct {
		Name               string `json:"name"`
		BrowserDownloadURL string `json:"browser_download_url"`
	} `json:"assets"`
}

type Manifest struct {
	Version    string `json:"version"`
	Tag        string `json:"tag"`
	Repository string `json:"repository"`
	Image      string `json:"image"`
	Digest     string `json:"digest"`
}

var (
	repositoryPattern = regexp.MustCompile(`^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$`)
	imagePattern      = regexp.MustCompile(`^ghcr\.io/[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$`)
	digestPattern     = regexp.MustCompile(`^sha256:[a-f0-9]{64}$`)
	jobIDPattern      = regexp.MustCompile(`^[A-Za-z0-9_-]{8,128}$`)
	stableTagPattern  = regexp.MustCompile(`^v1\.0\.(0|[1-9][0-9]*)$`)
	errUpdateLocked   = errors.New("an update is already running")
)

func New(config Config) (*Service, error) {
	config.Repository = strings.TrimSpace(config.Repository)
	config.Image = strings.TrimSuffix(strings.TrimSpace(config.Image), "/")
	if !repositoryPattern.MatchString(config.Repository) || !imagePattern.MatchString(config.Image) {
		return nil, errors.New("APP_UPDATE_REPOSITORY or APP_IMAGE is invalid")
	}
	var err error
	config.DeployDir, err = filepath.Abs(strings.TrimSpace(config.DeployDir))
	if err != nil || config.DeployDir == "" {
		return nil, errors.New("UPDATER_DEPLOY_DIR is invalid")
	}
	config.StateDir, err = filepath.Abs(strings.TrimSpace(config.StateDir))
	if err != nil || config.StateDir == "" {
		return nil, errors.New("UPDATER_STATE_DIR is invalid")
	}
	if config.ComposeService == "" {
		config.ComposeService = "app"
	}
	if config.HealthURL == "" {
		config.HealthURL = "http://127.0.0.1:8080/healthz"
	}
	if config.CosignOIDCIssuer == "" {
		config.CosignOIDCIssuer = "https://token.actions.githubusercontent.com"
	}
	if config.GitHubAPIBaseURL == "" {
		config.GitHubAPIBaseURL = "https://api.github.com"
	}
	config.GitHubAPIBaseURL = strings.TrimRight(config.GitHubAPIBaseURL, "/")
	if config.HTTPClient == nil {
		config.HTTPClient = &http.Client{Timeout: 20 * time.Second}
	}
	if config.RunCommand == nil {
		config.RunCommand = runCommand
	}
	if config.Now == nil {
		config.Now = time.Now
	}
	if err := os.MkdirAll(filepath.Join(config.StateDir, "jobs"), 0o700); err != nil {
		return nil, fmt.Errorf("create updater state directory: %w", err)
	}
	service := &Service{config: config}
	recoveryLock, err := acquireProcessLock(filepath.Join(config.DeployDir, ".outlook-mail-manager-update.lock"))
	if err == nil {
		recoveryErr := service.recoverInterruptedJobs()
		closeErr := recoveryLock.Close()
		if recoveryErr != nil {
			return nil, recoveryErr
		}
		if closeErr != nil {
			return nil, fmt.Errorf("release updater recovery lock: %w", closeErr)
		}
	} else if !errors.Is(err, errUpdateLocked) {
		return nil, fmt.Errorf("acquire updater recovery lock: %w", err)
	}
	return service, nil
}

func (s *Service) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/status", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{"status": "ready", "repository": s.config.Repository})
	})
	mux.HandleFunc("POST /v1/jobs", s.startJob)
	mux.HandleFunc("GET /v1/jobs/{job_id}", s.getJob)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.ContentLength > 0 {
			http.Error(w, "request body is not allowed", http.StatusBadRequest)
			return
		}
		mux.ServeHTTP(w, r)
	})
}

func (s *Service) startJob(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	if s.running {
		s.mu.Unlock()
		http.Error(w, "an update is already running", http.StatusConflict)
		return
	}
	lock, err := acquireProcessLock(filepath.Join(s.config.DeployDir, ".outlook-mail-manager-update.lock"))
	if errors.Is(err, errUpdateLocked) {
		s.mu.Unlock()
		http.Error(w, "an update is already running", http.StatusConflict)
		return
	}
	if err != nil {
		s.mu.Unlock()
		http.Error(w, "cannot acquire update lock", http.StatusInternalServerError)
		return
	}
	s.running = true
	s.mu.Unlock()
	now := s.config.Now().UTC()
	job := Job{ID: randomID(), State: "queued", Message: "等待更新助手处理", CreatedAt: now, UpdatedAt: now}
	if err := s.saveJob(job); err != nil {
		_ = lock.Close()
		s.mu.Lock()
		s.running = false
		s.mu.Unlock()
		http.Error(w, "cannot persist update job", http.StatusInternalServerError)
		return
	}
	go s.runJob(job, lock)
	writeJSON(w, http.StatusAccepted, job)
}

func (s *Service) getJob(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("job_id")
	if !jobIDPattern.MatchString(id) {
		http.Error(w, "invalid job id", http.StatusBadRequest)
		return
	}
	job, err := s.loadJob(id)
	if errors.Is(err, os.ErrNotExist) {
		http.Error(w, "job not found", http.StatusNotFound)
		return
	}
	if err != nil {
		http.Error(w, "cannot load update job", http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, job)
}

func (s *Service) runJob(job Job, lock *processLock) {
	defer func() {
		_ = lock.Close()
		s.mu.Lock()
		s.running = false
		s.mu.Unlock()
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	defer cancel()
	set := func(state, message string) {
		job.State, job.Message, job.UpdatedAt = state, message, s.config.Now().UTC()
		_ = s.saveJob(job)
	}
	set("checking", "正在读取最新稳定版")
	release, manifest, err := s.fetchRelease(ctx)
	if err != nil {
		s.failJob(&job, err)
		return
	}
	envPath := filepath.Join(s.config.DeployDir, ".env")
	previousEnv, err := os.ReadFile(envPath)
	if err != nil {
		s.failJob(&job, fmt.Errorf("read deployment environment: %w", err))
		return
	}
	currentVersion := envValue(previousEnv, "APP_VERSION")
	if !versionGreater(manifest.Version, currentVersion) {
		s.failJob(&job, fmt.Errorf("refuse update from %s to %s: downgrade and same-version updates are not allowed", currentVersion, manifest.Version))
		return
	}
	job.Version = manifest.Version
	set("backing_up", "正在创建升级前一致性备份")
	backupName, err := s.createBackup(ctx)
	if err != nil {
		s.failJob(&job, err)
		return
	}
	set("verifying", "正在验证固定镜像摘要和发布身份")
	imageRef := manifest.Image + "@" + manifest.Digest
	if _, err := s.config.RunCommand(ctx, "cosign", "verify", imageRef,
		"--certificate-identity", s.releaseIdentity(release.TagName),
		"--certificate-oidc-issuer", s.config.CosignOIDCIssuer); err != nil {
		s.failJob(&job, fmt.Errorf("verify signed image: %w", err))
		return
	}
	set("pulling", "正在拉取已验证的固定镜像")
	if _, err := s.config.RunCommand(ctx, "docker", "pull", imageRef); err != nil {
		s.failJob(&job, fmt.Errorf("pull image: %w", err))
		return
	}
	set("restarting", "正在切换版本并重启应用")
	if err := updateEnvFile(envPath, previousEnv, imageRef, manifest.Version); err != nil {
		s.failJob(&job, err)
		return
	}
	_, err = s.compose(ctx, "up", "-d", "--no-build", s.config.ComposeService)
	if err == nil {
		err = s.waitForHealth(ctx, 90*time.Second)
	}
	if err != nil {
		set("rolling_back", "健康检查失败，正在恢复旧镜像、配置和数据库")
		rollbackErr := s.rollback(ctx, envPath, previousEnv, backupName)
		if rollbackErr != nil {
			s.failJob(&job, fmt.Errorf("update failed: %v; rollback failed: %w", err, rollbackErr))
		} else {
			s.failJob(&job, fmt.Errorf("update failed and was rolled back: %w", err))
		}
		return
	}
	_ = release
	now := s.config.Now().UTC()
	job.State, job.Message, job.Error, job.UpdatedAt, job.CompletedAt = "completed", "更新完成，升级前备份已保留", "", now, &now
	_ = s.saveJob(job)
}

func (s *Service) fetchRelease(ctx context.Context) (release, Manifest, error) {
	var item release
	if err := s.getJSON(ctx, s.config.GitHubAPIBaseURL+"/repos/"+s.config.Repository+"/releases/latest", &item); err != nil {
		return item, Manifest{}, err
	}
	if item.Draft || item.Prerelease || !stableTagPattern.MatchString(item.TagName) {
		return item, Manifest{}, errors.New("latest release is not a signed stable version")
	}
	manifestURL, bundleURL := "", ""
	for _, asset := range item.Assets {
		switch asset.Name {
		case "release-manifest.json":
			manifestURL = asset.BrowserDownloadURL
		case "release-manifest.json.bundle":
			bundleURL = asset.BrowserDownloadURL
		}
	}
	if manifestURL == "" || bundleURL == "" {
		return item, Manifest{}, errors.New("signed release manifest or verification bundle is missing")
	}
	manifestData, err := s.getBytes(ctx, manifestURL, 1<<20)
	if err != nil {
		return item, Manifest{}, err
	}
	bundleData, err := s.getBytes(ctx, bundleURL, 2<<20)
	if err != nil {
		return item, Manifest{}, err
	}
	verificationDir, err := os.MkdirTemp(s.config.StateDir, "verify-")
	if err != nil {
		return item, Manifest{}, fmt.Errorf("create manifest verification directory: %w", err)
	}
	defer os.RemoveAll(verificationDir)
	manifestPath := filepath.Join(verificationDir, "release-manifest.json")
	bundlePath := filepath.Join(verificationDir, "release-manifest.json.bundle")
	if err := os.WriteFile(manifestPath, manifestData, 0o600); err != nil {
		return item, Manifest{}, fmt.Errorf("write release manifest: %w", err)
	}
	if err := os.WriteFile(bundlePath, bundleData, 0o600); err != nil {
		return item, Manifest{}, fmt.Errorf("write release manifest bundle: %w", err)
	}
	if _, err := s.config.RunCommand(ctx, "cosign", "verify-blob", "--bundle", bundlePath,
		"--certificate-identity", s.releaseIdentity(item.TagName),
		"--certificate-oidc-issuer", s.config.CosignOIDCIssuer, manifestPath); err != nil {
		return item, Manifest{}, fmt.Errorf("verify signed release manifest: %w", err)
	}
	var manifest Manifest
	if err := json.Unmarshal(manifestData, &manifest); err != nil {
		return item, manifest, fmt.Errorf("decode verified release manifest: %w", err)
	}
	if manifest.Repository != s.config.Repository || manifest.Image != s.config.Image || manifest.Tag != item.TagName || strings.TrimPrefix(item.TagName, "v") != manifest.Version || !digestPattern.MatchString(manifest.Digest) {
		return item, manifest, errors.New("release manifest does not match configured repository and image")
	}
	return item, manifest, nil
}

func (s *Service) releaseIdentity(tag string) string {
	return "https://github.com/" + s.config.Repository + "/.github/workflows/release.yml@refs/tags/" + tag
}

func (s *Service) getBytes(ctx context.Context, rawURL string, limit int64) ([]byte, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, err
	}
	response, err := s.config.HTTPClient.Do(request)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		io.Copy(io.Discard, io.LimitReader(response.Body, 4096))
		return nil, fmt.Errorf("download release asset: status %d", response.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limit {
		return nil, errors.New("release asset exceeds size limit")
	}
	return data, nil
}

func (s *Service) getJSON(ctx context.Context, rawURL string, target any) error {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return err
	}
	request.Header.Set("Accept", "application/vnd.github+json")
	request.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	response, err := s.config.HTTPClient.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		io.Copy(io.Discard, io.LimitReader(response.Body, 4096))
		return fmt.Errorf("download release metadata: status %d", response.StatusCode)
	}
	return json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(target)
}

func (s *Service) createBackup(ctx context.Context) (string, error) {
	output, err := s.compose(ctx, "exec", "-T", s.config.ComposeService, "/usr/local/bin/outlook-mail-manager", "backup")
	if err != nil {
		return "", fmt.Errorf("create update backup: %w", err)
	}
	match := regexp.MustCompile(`backup created: (outlook-manager-[A-Za-z0-9_-]+\.db)`).FindSubmatch(output)
	if len(match) != 2 {
		return "", errors.New("create update backup: backup name was not returned")
	}
	return string(match[1]), nil
}

func (s *Service) rollback(ctx context.Context, envPath string, previousEnv []byte, backupName string) error {
	if err := atomicWrite(envPath, previousEnv, 0o600); err != nil {
		return err
	}
	_, _ = s.compose(ctx, "stop", s.config.ComposeService)
	if _, err := s.compose(ctx, "run", "--rm", s.config.ComposeService, "restore", "/data/backups/"+backupName); err != nil {
		return err
	}
	if _, err := s.compose(ctx, "up", "-d", "--no-build", s.config.ComposeService); err != nil {
		return err
	}
	return s.waitForHealth(ctx, 90*time.Second)
}

func (s *Service) waitForHealth(ctx context.Context, timeout time.Duration) error {
	deadline := s.config.Now().Add(timeout)
	for s.config.Now().Before(deadline) {
		request, _ := http.NewRequestWithContext(ctx, http.MethodGet, s.config.HealthURL, nil)
		response, err := s.config.HTTPClient.Do(request)
		if err == nil {
			io.Copy(io.Discard, io.LimitReader(response.Body, 4096))
			response.Body.Close()
			if response.StatusCode == http.StatusOK {
				return nil
			}
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(2 * time.Second):
		}
	}
	return errors.New("application health check timed out")
}

func (s *Service) compose(ctx context.Context, args ...string) ([]byte, error) {
	base := []string{"compose", "--project-directory", s.config.DeployDir, "--env-file", filepath.Join(s.config.DeployDir, ".env")}
	return s.config.RunCommand(ctx, "docker", append(base, args...)...)
}

func (s *Service) failJob(job *Job, err error) {
	now := s.config.Now().UTC()
	job.State, job.Message, job.Error, job.UpdatedAt, job.CompletedAt = "failed", "更新未完成", sanitizeError(err), now, &now
	_ = s.saveJob(*job)
}

func (s *Service) saveJob(job Job) error {
	if !jobIDPattern.MatchString(job.ID) {
		return errors.New("invalid update job id")
	}
	encoded, err := json.Marshal(job)
	if err != nil {
		return err
	}
	return atomicWrite(filepath.Join(s.config.StateDir, "jobs", job.ID+".json"), encoded, 0o600)
}

func (s *Service) recoverInterruptedJobs() error {
	entries, err := os.ReadDir(filepath.Join(s.config.StateDir, "jobs"))
	if err != nil {
		return fmt.Errorf("read updater jobs: %w", err)
	}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		id := strings.TrimSuffix(entry.Name(), ".json")
		if !jobIDPattern.MatchString(id) {
			continue
		}
		job, err := s.loadJob(id)
		if err != nil {
			return fmt.Errorf("load updater job %s: %w", id, err)
		}
		if job.ID != id {
			return fmt.Errorf("load updater job %s: persisted id does not match file name", id)
		}
		if job.State == "completed" || job.State == "failed" {
			continue
		}
		now := s.config.Now().UTC()
		job.State = "failed"
		job.Message = "更新助手重启，原更新任务已中断"
		job.Error = "更新任务未正常结束，请确认当前应用健康状态后重试"
		job.UpdatedAt = now
		job.CompletedAt = &now
		if err := s.saveJob(job); err != nil {
			return fmt.Errorf("recover updater job %s: %w", id, err)
		}
	}
	return nil
}

func (s *Service) loadJob(id string) (Job, error) {
	data, err := os.ReadFile(filepath.Join(s.config.StateDir, "jobs", id+".json"))
	if err != nil {
		return Job{}, err
	}
	var job Job
	err = json.Unmarshal(data, &job)
	return job, err
}

func updateEnvFile(path string, previous []byte, imageRef, version string) error {
	lines := strings.Split(strings.ReplaceAll(string(previous), "\r\n", "\n"), "\n")
	seenImage, seenVersion := false, false
	for index, line := range lines {
		if strings.HasPrefix(line, "APP_IMAGE=") {
			lines[index], seenImage = "APP_IMAGE="+imageRef, true
		}
		if strings.HasPrefix(line, "APP_VERSION=") {
			lines[index], seenVersion = "APP_VERSION="+version, true
		}
	}
	if !seenImage {
		lines = append(lines, "APP_IMAGE="+imageRef)
	}
	if !seenVersion {
		lines = append(lines, "APP_VERSION="+version)
	}
	return atomicWrite(path, []byte(strings.Join(lines, "\n")), 0o600)
}

func envValue(data []byte, key string) string {
	prefix := key + "="
	for _, line := range strings.Split(strings.ReplaceAll(string(data), "\r\n", "\n"), "\n") {
		if strings.HasPrefix(line, prefix) {
			return strings.TrimSpace(strings.TrimPrefix(line, prefix))
		}
	}
	return ""
}

func versionGreater(latest, current string) bool {
	return appversion.Greater(latest, current)
}

func atomicWrite(path string, data []byte, mode os.FileMode) error {
	directory := filepath.Dir(path)
	temporary, err := os.CreateTemp(directory, "."+filepath.Base(path)+".tmp-")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	removeTemporary := true
	defer func() {
		_ = temporary.Close()
		if removeTemporary {
			_ = os.Remove(temporaryPath)
		}
	}()
	if err := temporary.Chmod(mode); err != nil {
		return err
	}
	if _, err := temporary.Write(data); err != nil {
		return err
	}
	if err := temporary.Sync(); err != nil {
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	if err := os.Rename(temporaryPath, path); err != nil {
		return err
	}
	removeTemporary = false
	return syncDirectory(directory)
}

func runCommand(ctx context.Context, name string, args ...string) ([]byte, error) {
	command := exec.CommandContext(ctx, name, args...)
	output, err := command.CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("%s failed: %w", name, err)
	}
	return output, nil
}

func randomID() string {
	value := make([]byte, 16)
	if _, err := rand.Read(value); err != nil {
		return fmt.Sprintf("job_%d", time.Now().UnixNano())
	}
	return "job_" + base64.RawURLEncoding.EncodeToString(value)
}

func sanitizeError(err error) string {
	if err == nil {
		return ""
	}
	value := err.Error()
	if len(value) > 500 {
		value = value[:500]
	}
	return value
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
