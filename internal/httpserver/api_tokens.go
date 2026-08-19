package httpserver

import (
	"context"
	"errors"
	"log/slog"
	"net/http"

	"outlook-mail-manager/internal/apitoken"
	"outlook-mail-manager/internal/auth"
)

type apiTokenService interface {
	Create(context.Context, apitoken.TokenInput) (apitoken.CreatedToken, error)
	List(context.Context) ([]apitoken.Token, error)
	Revoke(context.Context, string) error
	Verify(context.Context, string, string, string) (apitoken.Grant, error)
}

type apiTokensAPI struct {
	service     apiTokenService
	authService *auth.Service
	logger      *slog.Logger
}

func newAPITokensAPI(service apiTokenService, authService *auth.Service, logger *slog.Logger) *apiTokensAPI {
	return &apiTokensAPI{service: service, authService: authService, logger: logger}
}

func (api *apiTokensAPI) register(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/api-tokens", api.list)
	mux.HandleFunc("POST /api/api-tokens", api.create)
	mux.HandleFunc("POST /api/api-tokens/{public_id}/revoke", api.revoke)
}

func (api *apiTokensAPI) list(w http.ResponseWriter, r *http.Request) {
	if !authorizeAdministrator(w, r, api.authService, api.logger, false) {
		return
	}
	items, err := api.service.List(r.Context())
	if err != nil {
		api.writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"tokens": items})
}

func (api *apiTokensAPI) create(w http.ResponseWriter, r *http.Request) {
	if !authorizeAdministrator(w, r, api.authService, api.logger, true) {
		return
	}
	var input apitoken.TokenInput
	if err := decodeJSON(w, r, &input); err != nil {
		writeAPIError(w, http.StatusBadRequest, "invalid_request", "请求格式无效")
		return
	}
	item, err := api.service.Create(r.Context(), input)
	if err != nil {
		api.writeError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, item)
}

func (api *apiTokensAPI) revoke(w http.ResponseWriter, r *http.Request) {
	if !authorizeAdministrator(w, r, api.authService, api.logger, true) {
		return
	}
	if err := api.service.Revoke(r.Context(), r.PathValue("public_id")); err != nil {
		api.writeError(w, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusNoContent)
}

func (api *apiTokensAPI) writeError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, apitoken.ErrInvalidTokenInput):
		writeAPIError(w, http.StatusBadRequest, "invalid_api_token", "API token 的 scope、账号范围或 IP 范围无效")
	case errors.Is(err, apitoken.ErrTokenNotFound):
		writeAPIError(w, http.StatusNotFound, "api_token_not_found", "API token 不存在")
	default:
		api.logger.Error("API token request failed", "event", "api_token_request_failed", "error", err)
		writeAPIError(w, http.StatusInternalServerError, "api_token_request_failed", "API token 服务暂时无法完成请求")
	}
}
