package httpserver

import (
	"context"
	"database/sql"
	"encoding/json"
	"io/fs"
	"log/slog"
	"net/http"
	"path"
	"strings"
	"time"

	"outlook-mail-manager/internal/accounts"
	"outlook-mail-manager/internal/auth"
)

func New(
	db *sql.DB,
	logger *slog.Logger,
	assets fs.FS,
	authService *auth.Service,
	accountService *accounts.Service,
	mailService mailService,
	notificationService notificationService,
	tokenService apiTokenService,
	maintenanceService maintenanceService,
) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", healthHandler(db))
	newAuthAPI(authService, logger).register(mux)
	if accountService != nil {
		newAccountsAPI(accountService, authService, logger).register(mux)
	}
	if mailService != nil {
		newMailAPI(mailService, authService, logger).register(mux)
	}
	if notificationService != nil {
		newNotificationsAPI(notificationService, authService, logger).register(mux)
	}
	if tokenService != nil {
		newAPITokensAPI(tokenService, authService, logger).register(mux)
	}
	if maintenanceService != nil {
		newMaintenanceAPI(maintenanceService, mailService, authService, logger).register(mux)
	}
	if tokenService != nil && mailService != nil && maintenanceService != nil {
		newExternalAPI(db, tokenService, mailService, maintenanceService, logger).register(mux)
	}
	mux.Handle("/", spaHandler(assets))

	return securityHeaders(requestLogger(logger, mux))
}

func healthHandler(db *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), time.Second)
		defer cancel()

		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		if err := db.PingContext(ctx); err != nil {
			w.WriteHeader(http.StatusServiceUnavailable)
			json.NewEncoder(w).Encode(map[string]string{"status": "unavailable"})
			return
		}
		json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
	}
}

func spaHandler(assets fs.FS) http.Handler {
	files := http.FileServer(http.FS(assets))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			http.NotFound(w, r)
			return
		}

		requested := strings.TrimPrefix(path.Clean(r.URL.Path), "/")
		if requested != "." && requested != "" {
			if info, err := fs.Stat(assets, requested); err == nil && !info.IsDir() {
				if requested == "index.html" {
					w.Header().Set("Cache-Control", "no-store")
				} else if strings.HasPrefix(requested, "assets/") {
					w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
				}
				files.ServeHTTP(w, r)
				return
			}
			accept := r.Header.Get("Accept")
			if accept != "" && !strings.Contains(accept, "text/html") {
				http.NotFound(w, r)
				return
			}
		}

		if _, err := fs.Stat(assets, "index.html"); err != nil {
			w.Header().Set("Content-Type", "application/json; charset=utf-8")
			w.WriteHeader(http.StatusServiceUnavailable)
			json.NewEncoder(w).Encode(map[string]string{"error": "web_assets_unavailable"})
			return
		}

		clone := r.Clone(r.Context())
		urlCopy := *r.URL
		urlCopy.Path = "/"
		clone.URL = &urlCopy
		w.Header().Set("Cache-Control", "no-store")
		files.ServeHTTP(w, clone)
	})
}

func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Security-Policy", "default-src 'self'; connect-src 'self'; form-action 'self'; img-src 'self' data:; script-src 'self'; style-src 'self'; object-src 'none'; base-uri 'none'; frame-ancestors 'none'")
		w.Header().Set("Permissions-Policy", "camera=(), microphone=(), geolocation=()")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		next.ServeHTTP(w, r)
	})
}

func requestLogger(logger *slog.Logger, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		started := time.Now()
		recorder := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(recorder, r)
		logger.Info("request completed",
			"event", "http_request",
			"method", r.Method,
			"path", r.URL.Path,
			"status", recorder.status,
			"duration_ms", time.Since(started).Milliseconds(),
		)
	})
}

type statusRecorder struct {
	http.ResponseWriter
	status      int
	wroteHeader bool
}

func (r *statusRecorder) WriteHeader(status int) {
	if r.wroteHeader {
		return
	}
	r.wroteHeader = true
	r.status = status
	r.ResponseWriter.WriteHeader(status)
}
