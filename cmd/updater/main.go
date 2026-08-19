package main

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"outlook-mail-manager/internal/updater"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run() error {
	socketPath := env("UPDATER_SOCKET_PATH", "/run/outlook-mail-manager-updater/updater.sock")
	service, err := updater.New(updater.Config{
		Repository: os.Getenv("APP_UPDATE_REPOSITORY"), Image: os.Getenv("APP_IMAGE"),
		DeployDir:      env("UPDATER_DEPLOY_DIR", "/opt/outlook-mail-manager"),
		StateDir:       env("UPDATER_STATE_DIR", "/var/lib/outlook-mail-manager-updater"),
		ComposeService: env("UPDATER_COMPOSE_SERVICE", "app"), HealthURL: env("UPDATER_HEALTH_URL", "http://127.0.0.1:8080/healthz"),
		CosignOIDCIssuer: os.Getenv("UPDATER_COSIGN_OIDC_ISSUER"),
	})
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(socketPath), 0o750); err != nil {
		return err
	}
	if err := os.Remove(socketPath); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	listener, err := net.Listen("unix", socketPath)
	if err != nil {
		return err
	}
	defer listener.Close()
	if err := os.Chmod(socketPath, 0o660); err != nil {
		return err
	}
	server := &http.Server{Handler: service.Handler(), ReadHeaderTimeout: 5 * time.Second}
	shutdown, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go func() {
		<-shutdown.Done()
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = server.Shutdown(ctx)
	}()
	err = server.Serve(listener)
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}

func env(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}
