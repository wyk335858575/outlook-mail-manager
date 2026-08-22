package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"
	_ "time/tzdata"

	"outlook-mail-manager/internal/accounts"
	"outlook-mail-manager/internal/apitoken"
	"outlook-mail-manager/internal/auth"
	"outlook-mail-manager/internal/config"
	"outlook-mail-manager/internal/database"
	"outlook-mail-manager/internal/datakey"
	"outlook-mail-manager/internal/httpserver"
	"outlook-mail-manager/internal/logging"
	"outlook-mail-manager/internal/mail"
	"outlook-mail-manager/internal/maintenance"
	"outlook-mail-manager/internal/notify"
	webui "outlook-mail-manager/web"
)

var version = "dev"

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(args []string) error {
	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("load configuration: %w", err)
	}
	if len(args) > 0 {
		switch args[0] {
		case "backup":
			if len(args) != 1 {
				return errors.New("usage: outlook-mail-manager backup")
			}
			return createBackup(cfg.DataDir)
		case "backup-for-update":
			if len(args) != 1 {
				return errors.New("usage: outlook-mail-manager backup-for-update")
			}
			return createUpdateBackup(cfg.DataDir)
		case "restore":
			if len(args) != 2 {
				return errors.New("usage: outlook-mail-manager restore <backup.db>")
			}
			safety, err := maintenance.Restore(cfg.DataDir, args[1])
			if err != nil {
				return err
			}
			fmt.Println("restore completed")
			if safety != "" {
				fmt.Println("previous database preserved at", safety)
			}
			return nil
		default:
			return fmt.Errorf("unknown command %q", args[0])
		}
	}
	logger := logging.New(cfg.LogLevel)

	startupCtx, cancelStartup := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancelStartup()
	store, err := database.Open(startupCtx, cfg.DataDir)
	if err != nil {
		return err
	}
	defer store.Close()
	keyring := datakey.New(nil)
	authService, err := auth.New(store.DB, auth.Options{
		Keyring:       keyring,
		SecureCookies: cfg.BaseURL.Scheme == "https",
	})
	if err != nil {
		return fmt.Errorf("initialize administrator security: %w", err)
	}
	if err := authService.ValidateStartup(startupCtx); err != nil {
		return err
	}
	accountService, err := accounts.New(store.DB, accounts.Options{
		Keyring:  keyring,
		ClientID: cfg.MicrosoftClientID,
	})
	if err != nil {
		return fmt.Errorf("initialize Microsoft accounts: %w", err)
	}
	defer accountService.Close()
	notificationService, err := notify.New(store.DB, keyring, notify.Options{})
	if err != nil {
		return fmt.Errorf("initialize notifications: %w", err)
	}
	defer notificationService.Close()
	tokenService, err := apitoken.New(store.DB, apitoken.Options{})
	if err != nil {
		return fmt.Errorf("initialize API tokens: %w", err)
	}
	maintenanceService, err := maintenance.New(store.DB, cfg.DataDir, maintenance.Options{
		Version: version, UpdateRepository: cfg.UpdateRepository, UpdateImage: cfg.UpdateImage, UpdateSocket: cfg.UpdateSocket,
	})
	if err != nil {
		return fmt.Errorf("initialize maintenance: %w", err)
	}
	mailService, err := mail.New(store.DB, accountService.TokenManager(), mail.Options{
		DataDir: cfg.DataDir, Notifier: notificationService,
	})
	if err != nil {
		return fmt.Errorf("initialize mail synchronization: %w", err)
	}
	keyring.OnUnlock(notificationService.Start)
	keyring.OnUnlock(mailService.Start)
	defer mailService.Close()

	server := &http.Server{
		Addr:              cfg.ListenAddr,
		Handler:           httpserver.New(store.DB, logger, webui.Assets(), authService, accountService, mailService, notificationService, tokenService, maintenanceService),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       60 * time.Second,
		MaxHeaderBytes:    32 << 10,
	}

	shutdownCtx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go func() {
		<-shutdownCtx.Done()
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := server.Shutdown(ctx); err != nil {
			logger.Error("server shutdown failed", "event", "server_shutdown_failed", "error", err)
		}
	}()

	logger.Info("server starting",
		"event", "server_starting",
		"listen_addr", cfg.ListenAddr,
		"base_url", cfg.BaseURL.String(),
		"app_version", version,
	)
	err = server.ListenAndServe()
	if err != nil && !errors.Is(err, http.ErrServerClosed) {
		return fmt.Errorf("serve HTTP: %w", err)
	}
	logger.Info("server stopped", "event", "server_stopped")
	return nil
}

func createBackup(dataDir string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	store, err := database.Open(ctx, dataDir)
	if err != nil {
		return err
	}
	defer store.Close()
	service, err := maintenance.New(store.DB, dataDir, maintenance.Options{})
	if err != nil {
		return err
	}
	backup, err := service.CreateBackup(ctx)
	if err != nil {
		return err
	}
	fmt.Printf("backup created: %s (%d bytes, sha256 %s)\n", backup.Name, backup.SizeBytes, backup.SHA256)
	return nil
}

func createUpdateBackup(dataDir string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	backup, err := maintenance.CreateUpdateBackup(ctx, dataDir)
	if err != nil {
		return err
	}
	fmt.Printf("backup created: %s (%d bytes, sha256 %s)\n", backup.Name, backup.SizeBytes, backup.SHA256)
	return nil
}
