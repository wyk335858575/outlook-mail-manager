package httpserver

import (
	"context"
	"errors"
	"log/slog"
	"net/http"

	"outlook-mail-manager/internal/auth"
	mailbox "outlook-mail-manager/internal/mail"
	"outlook-mail-manager/internal/maintenance"
)

type maintenanceService interface {
	CreateBackup(context.Context) (maintenance.Backup, error)
	ListBackups(context.Context) ([]maintenance.Backup, error)
	DeleteBackup(context.Context, string) error
	Status(context.Context) (maintenance.Status, error)
	UpdateStatus(context.Context) (maintenance.UpdateStatus, error)
	StartUpdate(context.Context) (maintenance.UpdateJob, error)
	GetUpdateJob(context.Context, string) (maintenance.UpdateJob, error)
}

type maintenanceAPI struct {
	service     maintenanceService
	mail        mailService
	authService *auth.Service
	logger      *slog.Logger
}

func newMaintenanceAPI(service maintenanceService, mail mailService, authService *auth.Service, logger *slog.Logger) *maintenanceAPI {
	return &maintenanceAPI{service: service, mail: mail, authService: authService, logger: logger}
}

func (api *maintenanceAPI) register(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/health/detail", api.health)
	mux.HandleFunc("GET /api/backups", api.backups)
	mux.HandleFunc("POST /api/backups", api.createBackup)
	mux.HandleFunc("DELETE /api/backups/{name}", api.deleteBackup)
	mux.HandleFunc("GET /api/update/status", api.updateStatus)
	mux.HandleFunc("POST /api/update/jobs", api.startUpdate)
	mux.HandleFunc("GET /api/update/jobs/{job_id}", api.updateJob)
}

func (api *maintenanceAPI) updateStatus(w http.ResponseWriter, r *http.Request) {
	if !authorizeAdministrator(w, r, api.authService, api.logger, false) {
		return
	}
	status, err := api.service.UpdateStatus(r.Context())
	if err != nil {
		api.writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, status)
}

func (api *maintenanceAPI) startUpdate(w http.ResponseWriter, r *http.Request) {
	if !authorizeAdministrator(w, r, api.authService, api.logger, true) {
		return
	}
	job, err := api.service.StartUpdate(r.Context())
	if err != nil {
		api.writeError(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, job)
}

func (api *maintenanceAPI) updateJob(w http.ResponseWriter, r *http.Request) {
	if !authorizeAdministrator(w, r, api.authService, api.logger, false) {
		return
	}
	job, err := api.service.GetUpdateJob(r.Context(), r.PathValue("job_id"))
	if err != nil {
		api.writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, job)
}

func (api *maintenanceAPI) deleteBackup(w http.ResponseWriter, r *http.Request) {
	if !authorizeAdministrator(w, r, api.authService, api.logger, true) {
		return
	}
	if err := api.service.DeleteBackup(r.Context(), r.PathValue("name")); err != nil {
		switch {
		case errors.Is(err, maintenance.ErrBackupNotFound):
			writeAPIError(w, http.StatusNotFound, "backup_not_found", "备份不存在")
		case errors.Is(err, maintenance.ErrInvalidBackupName):
			writeAPIError(w, http.StatusBadRequest, "invalid_backup_name", "备份文件名无效")
		default:
			api.writeError(w, err)
		}
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusNoContent)
}

func (api *maintenanceAPI) health(w http.ResponseWriter, r *http.Request) {
	if !authorizeAdministrator(w, r, api.authService, api.logger, false) {
		return
	}
	maintenanceStatus, err := api.service.Status(r.Context())
	if err != nil {
		api.writeError(w, err)
		return
	}
	mailStatus := mailbox.Status{}
	if api.mail != nil {
		mailStatus, err = api.mail.Status(r.Context())
		if err != nil {
			api.writeError(w, err)
			return
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"maintenance": maintenanceStatus, "mail": mailStatus})
}

func (api *maintenanceAPI) backups(w http.ResponseWriter, r *http.Request) {
	if !authorizeAdministrator(w, r, api.authService, api.logger, false) {
		return
	}
	items, err := api.service.ListBackups(r.Context())
	if err != nil {
		api.writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"backups": items})
}

func (api *maintenanceAPI) createBackup(w http.ResponseWriter, r *http.Request) {
	if !authorizeAdministrator(w, r, api.authService, api.logger, true) {
		return
	}
	item, err := api.service.CreateBackup(r.Context())
	if err != nil {
		api.writeError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, item)
}

func (api *maintenanceAPI) writeError(w http.ResponseWriter, err error) {
	api.logger.Error("maintenance request failed", "event", "maintenance_request_failed", "error", err)
	writeAPIError(w, http.StatusInternalServerError, "maintenance_request_failed", "健康检查或备份暂时无法完成")
}
