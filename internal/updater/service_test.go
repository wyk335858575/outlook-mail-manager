package updater

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestNewRejectsUntrustedImageRegistry(t *testing.T) {
	_, err := New(Config{Repository: "owner/repo", Image: "docker.io/owner/repo", DeployDir: t.TempDir(), StateDir: t.TempDir()})
	if err == nil {
		t.Fatal("New() accepted a non-GHCR image")
	}
}

func TestComposeEnvironmentUsesDeploymentEnvFileValues(t *testing.T) {
	t.Setenv("APP_IMAGE", "ghcr.io/owner/repo@sha256:old")
	t.Setenv("APP_VERSION", "1.0.3")
	var captured []string
	service, err := New(Config{
		Repository: "owner/repo", Image: "ghcr.io/owner/repo", DeployDir: t.TempDir(), StateDir: t.TempDir(),
		RunCommandWithEnv: func(_ context.Context, _ string, environment []string, _ ...string) ([]byte, error) {
			captured = environment
			return nil, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.compose(context.Background(), "config"); err != nil {
		t.Fatal(err)
	}
	if len(captured) == 0 {
		t.Fatal("compose did not invoke the environment-aware command runner")
	}
	value := strings.Join(captured, "\x00")
	if strings.Contains(value, "APP_IMAGE=") || strings.Contains(value, "APP_VERSION=") {
		t.Fatalf("compose inherited deployment overrides: %v", captured)
	}
	if !strings.Contains(value, "PATH=") {
		t.Fatalf("compose environment lost PATH: %v", captured)
	}
}

func TestManifestMismatchStopsBeforeCommands(t *testing.T) {
	var commandMu sync.Mutex
	var commands []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if strings.HasSuffix(r.URL.Path, "/releases/latest") {
			_, _ = w.Write([]byte(`{"tag_name":"v1.0.0","assets":[{"name":"release-manifest.json","browser_download_url":"` + serverURL(r) + `/manifest"},{"name":"release-manifest.json.bundle","browser_download_url":"` + serverURL(r) + `/bundle"}]}`))
			return
		}
		if r.URL.Path == "/bundle" {
			_, _ = w.Write([]byte(`{"mediaType":"application/vnd.dev.sigstore.bundle.v0.3+json"}`))
			return
		}
		_, _ = w.Write([]byte(`{"version":"1.0.0","tag":"v1.0.0","repository":"other/repo","image":"ghcr.io/owner/repo","digest":"sha256:` + strings.Repeat("a", 64) + `"}`))
	}))
	defer server.Close()
	service, err := New(Config{Repository: "owner/repo", Image: "ghcr.io/owner/repo", DeployDir: t.TempDir(), StateDir: t.TempDir(), GitHubAPIBaseURL: server.URL, HTTPClient: server.Client(), RunCommand: func(_ context.Context, name string, args ...string) ([]byte, error) {
		commandMu.Lock()
		commands = append(commands, name+" "+strings.Join(args, " "))
		commandMu.Unlock()
		return nil, nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, "/v1/jobs", nil)
	response := httptest.NewRecorder()
	service.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusAccepted {
		t.Fatalf("status = %d", response.Code)
	}
	var job Job
	for range 100 {
		files, _ := os.ReadDir(filepath.Join(service.config.StateDir, "jobs"))
		if len(files) == 1 {
			job, _ = service.loadJob(strings.TrimSuffix(files[0].Name(), ".json"))
			if job.State == "failed" {
				break
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	commandMu.Lock()
	defer commandMu.Unlock()
	if job.State != "failed" || len(commands) != 1 || !strings.HasPrefix(commands[0], "cosign verify-blob ") {
		t.Fatalf("job = %+v, commands = %v", job, commands)
	}
}

func TestManifestAndImageIdentityAreBoundToReleaseTag(t *testing.T) {
	server := signedReleaseServer(t, "v1.1.0", "owner/repo", "ghcr.io/owner/repo")
	defer server.Close()
	var commands []string
	service, err := New(Config{
		Repository: "owner/repo", Image: "ghcr.io/owner/repo", DeployDir: t.TempDir(), StateDir: t.TempDir(),
		GitHubAPIBaseURL: server.URL, HTTPClient: server.Client(),
		RunCommand: func(_ context.Context, name string, args ...string) ([]byte, error) {
			commands = append(commands, name+" "+strings.Join(args, " "))
			return nil, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	release, _, err := service.fetchRelease(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	expected := "--certificate-identity https://github.com/owner/repo/.github/workflows/release.yml@refs/tags/" + release.TagName
	if len(commands) != 1 || !strings.Contains(commands[0], expected) || strings.Contains(commands[0], "identity-regexp") {
		t.Fatalf("manifest verification command = %v", commands)
	}
}

func TestRunOnceCompletesUsingTemporaryCosignBinary(t *testing.T) {
	releaseServer := signedReleaseServer(t, "v1.0.1", "owner/repo", "ghcr.io/owner/repo")
	defer releaseServer.Close()
	health := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) }))
	defer health.Close()
	deployDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(deployDir, ".env"), []byte("APP_VERSION=1.0.0\nAPP_IMAGE=ghcr.io/owner/repo:1.0.0\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	var commands []string
	runner := func(_ context.Context, name string, args ...string) ([]byte, error) {
		joined := name + " " + strings.Join(args, " ")
		commands = append(commands, joined)
		if strings.Contains(joined, "run --rm --no-deps app backup-for-update") {
			return []byte("backup created: outlook-manager-20260819T000000Z.db (10 bytes)"), nil
		}
		return []byte("ok"), nil
	}
	service, err := New(Config{
		Repository: "owner/repo", Image: "ghcr.io/owner/repo", DeployDir: deployDir, StateDir: t.TempDir(),
		GitHubAPIBaseURL: releaseServer.URL, HTTPClient: releaseServer.Client(), HealthURL: health.URL,
		CosignBinary: "/tmp/cosign", RunCommand: runner,
	})
	if err != nil {
		t.Fatal(err)
	}
	job, err := service.RunOnce(context.Background(), nil)
	if err != nil || job.State != "completed" || job.Version != "1.0.1" {
		t.Fatalf("RunOnce() job = %+v, error = %v", job, err)
	}
	if len(commands) < 2 || !strings.HasPrefix(commands[0], "/tmp/cosign verify-blob ") {
		t.Fatalf("commands = %v", commands)
	}
	joinedCommands := strings.Join(commands, "\n")
	pullAt := strings.Index(joinedCommands, "docker pull ghcr.io/owner/repo@sha256:")
	stopAt := strings.Index(joinedCommands, " stop app")
	backupAt := strings.Index(joinedCommands, "run --rm --no-deps app backup-for-update")
	startAt := strings.Index(joinedCommands, "up -d --no-build --force-recreate app")
	if pullAt < 0 || stopAt < pullAt || backupAt < stopAt || startAt < backupAt {
		t.Fatalf("unsafe update command order: %v", commands)
	}
	data, err := os.ReadFile(filepath.Join(deployDir, ".env"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "APP_VERSION=1.0.1") || !strings.Contains(string(data), "APP_IMAGE=ghcr.io/owner/repo@sha256:") {
		t.Fatalf("updated environment = %q", data)
	}
}

func TestManifestVerificationFailureStopsBeforeParsing(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.HasSuffix(r.URL.Path, "/releases/latest"):
			_, _ = w.Write([]byte(`{"tag_name":"v1.0.1","assets":[{"name":"release-manifest.json","browser_download_url":"` + serverURL(r) + `/manifest"},{"name":"release-manifest.json.bundle","browser_download_url":"` + serverURL(r) + `/bundle"}]}`))
		case r.URL.Path == "/manifest":
			_, _ = w.Write([]byte(`not-json`))
		default:
			_, _ = w.Write([]byte(`{}`))
		}
	}))
	defer server.Close()
	service, err := New(Config{Repository: "owner/repo", Image: "ghcr.io/owner/repo", DeployDir: t.TempDir(), StateDir: t.TempDir(), GitHubAPIBaseURL: server.URL, HTTPClient: server.Client(), RunCommand: func(context.Context, string, ...string) ([]byte, error) {
		return nil, errors.New("signature rejected")
	}})
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = service.fetchRelease(context.Background())
	if err == nil || !strings.Contains(err.Error(), "verify signed release manifest") {
		t.Fatalf("fetchRelease() error = %v", err)
	}
}

func TestFetchReleaseRejectsUnsupportedTagsBeforeVerification(t *testing.T) {
	for _, releaseJSON := range []string{
		`{"tag_name":"v1.0.1","prerelease":true}`,
		`{"tag_name":"v2.0.0"}`,
		`{"tag_name":"v1.0.01"}`,
	} {
		t.Run(releaseJSON, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(releaseJSON))
			}))
			defer server.Close()
			var commands atomic.Int32
			service, err := New(Config{Repository: "owner/repo", Image: "ghcr.io/owner/repo", DeployDir: t.TempDir(), StateDir: t.TempDir(), GitHubAPIBaseURL: server.URL, HTTPClient: server.Client(), RunCommand: func(context.Context, string, ...string) ([]byte, error) {
				commands.Add(1)
				return nil, nil
			}})
			if err != nil {
				t.Fatal(err)
			}
			if _, _, err := service.fetchRelease(context.Background()); err == nil || commands.Load() != 0 {
				t.Fatalf("fetchRelease() error = %v, commands = %d", err, commands.Load())
			}
		})
	}
}

func TestHandlerRejectsRequestBodies(t *testing.T) {
	service, err := New(Config{Repository: "owner/repo", Image: "ghcr.io/owner/repo", DeployDir: t.TempDir(), StateDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, "/v1/jobs", strings.NewReader(`{"image":"attacker/image"}`))
	response := httptest.NewRecorder()
	service.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusBadRequest {
		t.Fatalf("status = %d", response.Code)
	}
}

func TestHandlerRejectsConcurrentUpdateJobs(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		close(started)
		<-release
		http.Error(w, "stop", http.StatusServiceUnavailable)
	}))
	defer server.Close()
	service, err := New(Config{Repository: "owner/repo", Image: "ghcr.io/owner/repo", DeployDir: t.TempDir(), StateDir: t.TempDir(), GitHubAPIBaseURL: server.URL, HTTPClient: server.Client()})
	if err != nil {
		t.Fatal(err)
	}
	first := httptest.NewRecorder()
	service.Handler().ServeHTTP(first, httptest.NewRequest(http.MethodPost, "/v1/jobs", nil))
	if first.Code != http.StatusAccepted {
		t.Fatalf("first status = %d", first.Code)
	}
	<-started
	second := httptest.NewRecorder()
	service.Handler().ServeHTTP(second, httptest.NewRequest(http.MethodPost, "/v1/jobs", nil))
	close(release)
	if second.Code != http.StatusConflict {
		t.Fatalf("second status = %d", second.Code)
	}
	waitForServiceIdle(t, service)
}

func TestSeparateServicesRejectConcurrentUpdateJobs(t *testing.T) {
	started := make(chan struct{})
	releaseRequest := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		close(started)
		<-releaseRequest
		http.Error(w, "stop", http.StatusServiceUnavailable)
	}))
	defer server.Close()
	deployDir := t.TempDir()
	first, err := New(Config{Repository: "owner/repo", Image: "ghcr.io/owner/repo", DeployDir: deployDir, StateDir: t.TempDir(), GitHubAPIBaseURL: server.URL, HTTPClient: server.Client()})
	if err != nil {
		t.Fatal(err)
	}
	second, err := New(Config{Repository: "owner/repo", Image: "ghcr.io/owner/repo", DeployDir: deployDir, StateDir: t.TempDir(), GitHubAPIBaseURL: server.URL, HTTPClient: server.Client()})
	if err != nil {
		t.Fatal(err)
	}
	response := httptest.NewRecorder()
	first.Handler().ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/v1/jobs", nil))
	if response.Code != http.StatusAccepted {
		t.Fatalf("first status = %d", response.Code)
	}
	<-started
	response = httptest.NewRecorder()
	second.Handler().ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/v1/jobs", nil))
	close(releaseRequest)
	if response.Code != http.StatusConflict {
		t.Fatalf("second status = %d", response.Code)
	}
	waitForServiceIdle(t, first)
}

func waitForServiceIdle(t *testing.T, service *Service) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		service.mu.Lock()
		running := service.running
		service.mu.Unlock()
		if !running {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("update job did not finish")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestNewMarksInterruptedJobsFailed(t *testing.T) {
	stateDir := t.TempDir()
	jobsDir := filepath.Join(stateDir, "jobs")
	if err := os.MkdirAll(jobsDir, 0o700); err != nil {
		t.Fatal(err)
	}
	created := time.Date(2026, 8, 19, 1, 0, 0, 0, time.UTC)
	data := `{"id":"job_interrupted","state":"pulling","created_at":"` + created.Format(time.RFC3339) + `","updated_at":"` + created.Format(time.RFC3339) + `"}`
	if err := os.WriteFile(filepath.Join(jobsDir, "job_interrupted.json"), []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
	now := created.Add(time.Minute)
	service, err := New(Config{Repository: "owner/repo", Image: "ghcr.io/owner/repo", DeployDir: t.TempDir(), StateDir: stateDir, Now: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}
	job, err := service.loadJob("job_interrupted")
	if err != nil {
		t.Fatal(err)
	}
	if job.State != "failed" || job.CompletedAt == nil || !job.CompletedAt.Equal(now) || !strings.Contains(job.Message, "已中断") {
		t.Fatalf("recovered job = %+v", job)
	}
}

func TestUpdateEnvFileOnlyChangesFixedKeys(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".env")
	previous := []byte("APP_BASE_URL=https://mail.example.com\nAPP_IMAGE=old\nAPP_VERSION=0.10.0\n")
	if err := updateEnvFile(path, previous, "ghcr.io/owner/repo@sha256:"+strings.Repeat("a", 64), "0.11.0"); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	value := string(data)
	if !strings.Contains(value, "APP_BASE_URL=https://mail.example.com") || !strings.Contains(value, "APP_VERSION=0.11.0") || strings.Contains(value, "APP_IMAGE=old") {
		t.Fatalf("updated environment = %q", value)
	}
}

func TestAtomicWriteUsesUniqueTemporaryFile(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, "state.json")
	if err := os.WriteFile(path+".tmp", []byte("unrelated"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := atomicWrite(path, []byte("current"), 0o600); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "current" {
		t.Fatalf("atomic file = %q", data)
	}
	stale, err := os.ReadFile(path + ".tmp")
	if err != nil || string(stale) != "unrelated" {
		t.Fatalf("unrelated temporary file = %q, %v", stale, err)
	}
}

func TestVersionGreaterRejectsSameVersionAndDowngrade(t *testing.T) {
	if versionGreater("0.11.0", "0.11.0") || versionGreater("0.10.9", "0.11.0") || !versionGreater("0.11.1", "0.11.0") {
		t.Fatal("version comparison did not enforce upgrade-only behavior")
	}
}

func TestRestartFailureRunsRollbackAndDoesNotReportSuccess(t *testing.T) {
	health := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) }))
	defer health.Close()
	var releaseServer *httptest.Server
	releaseServer = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if strings.HasSuffix(r.URL.Path, "/releases/latest") {
			_, _ = w.Write([]byte(`{"tag_name":"v1.0.0","assets":[{"name":"release-manifest.json","browser_download_url":"` + releaseServer.URL + `/manifest"},{"name":"release-manifest.json.bundle","browser_download_url":"` + releaseServer.URL + `/bundle"}]}`))
			return
		}
		if r.URL.Path == "/bundle" {
			_, _ = w.Write([]byte(`{}`))
			return
		}
		_, _ = w.Write([]byte(`{"version":"1.0.0","tag":"v1.0.0","repository":"owner/repo","image":"ghcr.io/owner/repo","digest":"sha256:` + strings.Repeat("a", 64) + `"}`))
	}))
	defer releaseServer.Close()
	deployDir, stateDir := t.TempDir(), t.TempDir()
	if err := os.WriteFile(filepath.Join(deployDir, ".env"), []byte("APP_VERSION=0.11.0\nAPP_IMAGE=old\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	var firstUp atomic.Bool
	var stopSeen atomic.Bool
	var forceRecreateCount atomic.Int32
	var restoreSawNewConfig atomic.Bool
	runner := func(_ context.Context, name string, args ...string) ([]byte, error) {
		joined := name + " " + strings.Join(args, " ")
		if strings.Contains(joined, " stop app") {
			stopSeen.Store(true)
		}
		if strings.Contains(joined, "--force-recreate app") {
			forceRecreateCount.Add(1)
		}
		switch {
		case strings.Contains(joined, "run --rm --no-deps app backup-for-update"):
			return []byte("backup created: outlook-manager-20260819T000000Z.db (10 bytes)"), nil
		case strings.Contains(joined, "run --rm --no-deps app restore"):
			data, _ := os.ReadFile(filepath.Join(deployDir, ".env"))
			restoreSawNewConfig.Store(strings.Contains(string(data), "APP_VERSION=1.0.0"))
			return []byte("restore completed"), nil
		case strings.Contains(joined, "compose") && strings.Contains(joined, " up -d --no-build --force-recreate app") && !firstUp.Swap(true):
			return nil, context.DeadlineExceeded
		case strings.Contains(joined, " logs --no-color --tail 80 app"):
			return []byte("startup failed: bind address already in use"), nil
		default:
			return []byte("ok"), nil
		}
	}
	service, err := New(Config{Repository: "owner/repo", Image: "ghcr.io/owner/repo", DeployDir: deployDir, StateDir: stateDir, GitHubAPIBaseURL: releaseServer.URL, HTTPClient: releaseServer.Client(), HealthURL: health.URL, RunCommand: runner})
	if err != nil {
		t.Fatal(err)
	}
	response := httptest.NewRecorder()
	service.Handler().ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/v1/jobs", nil))
	if response.Code != http.StatusAccepted {
		t.Fatalf("status = %d", response.Code)
	}
	var job Job
	for range 200 {
		files, _ := os.ReadDir(filepath.Join(stateDir, "jobs"))
		if len(files) == 1 {
			job, _ = service.loadJob(strings.TrimSuffix(files[0].Name(), ".json"))
			if job.State == "failed" {
				break
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	if job.State != "failed" || !strings.Contains(job.Error, "rolled back") || !strings.Contains(job.Error, "startup failed") {
		t.Fatalf("job = %+v", job)
	}
	if !stopSeen.Load() || forceRecreateCount.Load() != 2 {
		t.Fatalf("stop seen = %v, force recreate count = %d", stopSeen.Load(), forceRecreateCount.Load())
	}
	if !restoreSawNewConfig.Load() {
		t.Fatal("rollback restore did not run with the verified new image configuration")
	}
	data, err := os.ReadFile(filepath.Join(deployDir, ".env"))
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "APP_VERSION=0.11.0\nAPP_IMAGE=old\n" {
		t.Fatalf("environment was not restored: %q", data)
	}
}

func TestBackupFailureRestartsPreviousDeploymentWithoutRestoringDatabase(t *testing.T) {
	releaseServer := signedReleaseServer(t, "v1.0.1", "owner/repo", "ghcr.io/owner/repo")
	defer releaseServer.Close()
	health := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) }))
	defer health.Close()
	deployDir := t.TempDir()
	previous := "APP_VERSION=1.0.0\nAPP_IMAGE=ghcr.io/owner/repo:1.0.0\n"
	if err := os.WriteFile(filepath.Join(deployDir, ".env"), []byte(previous), 0o600); err != nil {
		t.Fatal(err)
	}
	var restoreCalled atomic.Bool
	var restartCalled atomic.Bool
	runner := func(_ context.Context, name string, args ...string) ([]byte, error) {
		joined := name + " " + strings.Join(args, " ")
		switch {
		case strings.Contains(joined, "run --rm --no-deps app backup-for-update"):
			return nil, errors.New("missing app_settings row")
		case strings.Contains(joined, " app restore "):
			restoreCalled.Store(true)
		case strings.Contains(joined, "up -d --no-build --force-recreate app"):
			restartCalled.Store(true)
		}
		return []byte("ok"), nil
	}
	service, err := New(Config{
		Repository: "owner/repo", Image: "ghcr.io/owner/repo", DeployDir: deployDir, StateDir: t.TempDir(),
		GitHubAPIBaseURL: releaseServer.URL, HTTPClient: releaseServer.Client(), HealthURL: health.URL,
		CosignBinary: "/tmp/cosign", RunCommand: runner,
	})
	if err != nil {
		t.Fatal(err)
	}
	job, err := service.RunOnce(context.Background(), nil)
	if err == nil || job.State != "failed" || !strings.Contains(job.Error, "create update backup") {
		t.Fatalf("RunOnce() job = %+v, error = %v", job, err)
	}
	if restoreCalled.Load() || !restartCalled.Load() {
		t.Fatalf("restore called = %v, restart called = %v", restoreCalled.Load(), restartCalled.Load())
	}
	data, err := os.ReadFile(filepath.Join(deployDir, ".env"))
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != previous {
		t.Fatalf("environment was not restored: %q", data)
	}
}

func serverURL(r *http.Request) string { return "http://" + r.Host }

func signedReleaseServer(t *testing.T, tag, repository, image string) *httptest.Server {
	t.Helper()
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.HasSuffix(r.URL.Path, "/releases/latest"):
			_, _ = w.Write([]byte(`{"tag_name":"` + tag + `","assets":[{"name":"release-manifest.json","browser_download_url":"` + server.URL + `/manifest"},{"name":"release-manifest.json.bundle","browser_download_url":"` + server.URL + `/bundle"}]}`))
		case r.URL.Path == "/bundle":
			_, _ = w.Write([]byte(`{}`))
		default:
			_, _ = w.Write([]byte(`{"version":"` + strings.TrimPrefix(tag, "v") + `","tag":"` + tag + `","repository":"` + repository + `","image":"` + image + `","digest":"sha256:` + strings.Repeat("a", 64) + `"}`))
		}
	}))
	return server
}
