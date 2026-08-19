package httpserver

import (
	"crypto/subtle"
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"strings"

	"outlook-mail-manager/internal/accounts"
	"outlook-mail-manager/internal/auth"
)

const maxAccountImportBytes = 1 << 20
const maxOAuthImportBytes = 10 << 20

type accountsAPI struct {
	service     *accounts.Service
	authService *auth.Service
	logger      *slog.Logger
}

func newAccountsAPI(service *accounts.Service, authService *auth.Service, logger *slog.Logger) *accountsAPI {
	return &accountsAPI{service: service, authService: authService, logger: logger}
}

func (api *accountsAPI) register(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/accounts", api.list)
	mux.HandleFunc("GET /api/accounts/selection", api.selection)
	mux.HandleFunc("POST /api/accounts/batch/status", api.setBatchStatus)
	mux.HandleFunc("POST /api/accounts/batch/delete", api.deleteBatch)
	mux.HandleFunc("GET /api/accounts/config", api.config)
	mux.HandleFunc("PUT /api/accounts/config", api.updateConfig)
	mux.HandleFunc("POST /api/accounts/import", api.importAccounts)
	mux.HandleFunc("POST /api/accounts/oauth-imports", api.startOAuthImport)
	mux.HandleFunc("GET /api/accounts/oauth-imports/{job_id}", api.oauthImport)
	mux.HandleFunc("PUT /api/accounts/{public_id}", api.updateAccount)
	mux.HandleFunc("POST /api/accounts/{public_id}/oauth-credentials", api.replaceOAuthCredentials)
	mux.HandleFunc("POST /api/accounts/{public_id}/oauth/start", api.startAuthorization)
	mux.HandleFunc("GET /api/accounts/oauth/{job_id}", api.authorization)
	mux.HandleFunc("POST /api/accounts/oauth/{job_id}/confirm", api.confirmAuthorization)
	mux.HandleFunc("POST /api/accounts/oauth/{job_id}/restart", api.restartAuthorization)
	mux.HandleFunc("POST /api/accounts/{public_id}/status", api.setStatus)
	mux.HandleFunc("POST /api/accounts/{public_id}/check", api.check)
	mux.HandleFunc("DELETE /api/accounts/{public_id}", api.delete)
}

func (api *accountsAPI) startOAuthImport(w http.ResponseWriter, r *http.Request) {
	if !api.authorize(w, r, true) {
		return
	}
	var request struct {
		Accounts          []accounts.OAuthImportInput `json:"accounts"`
		OverwriteExisting bool                        `json:"overwrite_existing"`
	}
	if err := decodeJSONLimit(w, r, &request, maxOAuthImportBytes); err != nil {
		writeAPIError(w, http.StatusBadRequest, "invalid_request", "OAuth 导入内容格式无效")
		return
	}
	job, err := api.service.StartOAuthImport(r.Context(), request.Accounts, request.OverwriteExisting)
	if err != nil {
		var validationError *accounts.ImportValidationError
		if errors.As(err, &validationError) {
			writeAPIError(w, http.StatusBadRequest, "invalid_import", validationError.Message)
			return
		}
		api.writeError(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, job)
}

func (api *accountsAPI) oauthImport(w http.ResponseWriter, r *http.Request) {
	if !api.authorize(w, r, false) {
		return
	}
	job, err := api.service.GetOAuthImport(r.Context(), r.PathValue("job_id"))
	if err != nil {
		api.writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, job)
}

func (api *accountsAPI) updateAccount(w http.ResponseWriter, r *http.Request) {
	if !api.authorize(w, r, true) {
		return
	}
	var request accounts.AccountUpdate
	if err := decodeJSON(w, r, &request); err != nil {
		writeAPIError(w, http.StatusBadRequest, "invalid_request", "账号资料格式无效")
		return
	}
	item, err := api.service.UpdateAccount(r.Context(), r.PathValue("public_id"), request)
	if err != nil {
		var validationError *accounts.ImportValidationError
		if errors.As(err, &validationError) {
			writeAPIError(w, http.StatusBadRequest, "invalid_account", validationError.Message)
			return
		}
		api.writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, item)
}

func (api *accountsAPI) replaceOAuthCredentials(w http.ResponseWriter, r *http.Request) {
	if !api.authorize(w, r, true) {
		return
	}
	var request struct {
		ClientID     string `json:"client_id"`
		RefreshToken string `json:"refresh_token"`
	}
	if err := decodeJSONLimit(w, r, &request, 16<<10); err != nil {
		writeAPIError(w, http.StatusBadRequest, "invalid_request", "OAuth 凭据格式无效")
		return
	}
	if err := api.service.ReplaceOAuthCredentials(r.Context(), r.PathValue("public_id"), request.ClientID, request.RefreshToken); err != nil {
		var validationError *accounts.ImportValidationError
		if errors.As(err, &validationError) {
			writeAPIError(w, http.StatusBadRequest, "invalid_credentials", validationError.Message)
			return
		}
		api.writeError(w, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusNoContent)
}

func (api *accountsAPI) list(w http.ResponseWriter, r *http.Request) {
	if !api.authorize(w, r, false) {
		return
	}
	page, pageSize, valid := parseAccountPagination(r)
	if !valid {
		writeAPIError(w, http.StatusBadRequest, "invalid_pagination", "分页参数无效，可用每页数量为 25、50 或 100")
		return
	}
	result, err := api.service.ListAccounts(r.Context(), accounts.AccountListOptions{
		Query: strings.TrimSpace(r.URL.Query().Get("q")), Status: strings.TrimSpace(r.URL.Query().Get("status")),
		Page: page, PageSize: pageSize,
	})
	if err != nil {
		api.writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (api *accountsAPI) selection(w http.ResponseWriter, r *http.Request) {
	if !api.authorize(w, r, false) {
		return
	}
	ids, err := api.service.SelectAccountIDs(r.Context(), r.URL.Query().Get("q"), r.URL.Query().Get("status"))
	if err != nil {
		api.writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"public_ids": ids, "total": len(ids)})
}

func (api *accountsAPI) setBatchStatus(w http.ResponseWriter, r *http.Request) {
	if !api.authorize(w, r, true) {
		return
	}
	var request struct {
		PublicIDs []string `json:"public_ids"`
		Disabled  bool     `json:"disabled"`
	}
	if err := decodeJSONLimit(w, r, &request, 128<<10); err != nil {
		writeAPIError(w, http.StatusBadRequest, "invalid_request", "批量账号状态请求格式无效")
		return
	}
	result, err := api.service.SetDisabledBatch(r.Context(), request.PublicIDs, request.Disabled)
	if err != nil {
		api.writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (api *accountsAPI) deleteBatch(w http.ResponseWriter, r *http.Request) {
	if !api.authorize(w, r, true) {
		return
	}
	var request struct {
		PublicIDs []string `json:"public_ids"`
		Confirm   string   `json:"confirm"`
	}
	if err := decodeJSONLimit(w, r, &request, 128<<10); err != nil {
		writeAPIError(w, http.StatusBadRequest, "invalid_request", "批量删除账号请求格式无效")
		return
	}
	if request.Confirm != "DELETE_LOCAL_ACCOUNTS" {
		writeAPIError(w, http.StatusBadRequest, "invalid_delete_confirmation", "必须明确确认删除本地账号数据")
		return
	}
	result, err := api.service.DeleteBatch(r.Context(), request.PublicIDs)
	if err != nil {
		api.writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func parseAccountPagination(r *http.Request) (int, int, bool) {
	query := r.URL.Query()
	if !query.Has("page") && !query.Has("page_size") {
		return 1, 0, true
	}
	page, pageSize := 1, 50
	var err error
	if value := strings.TrimSpace(query.Get("page")); value != "" {
		page, err = strconv.Atoi(value)
		if err != nil || page < 1 {
			return 0, 0, false
		}
	}
	if value := strings.TrimSpace(query.Get("page_size")); value != "" {
		pageSize, err = strconv.Atoi(value)
		if err != nil {
			return 0, 0, false
		}
	}
	if pageSize != 25 && pageSize != 50 && pageSize != 100 {
		return 0, 0, false
	}
	return page, pageSize, true
}

func (api *accountsAPI) config(w http.ResponseWriter, r *http.Request) {
	if !api.authorize(w, r, false) {
		return
	}
	config, err := api.service.GetMicrosoftConfig(r.Context())
	if err != nil {
		api.writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, config)
}

func (api *accountsAPI) updateConfig(w http.ResponseWriter, r *http.Request) {
	if !api.authorize(w, r, true) {
		return
	}
	var request struct {
		ClientID string `json:"client_id"`
	}
	if err := decodeJSON(w, r, &request); err != nil {
		writeAPIError(w, http.StatusBadRequest, "invalid_request", "请求格式无效")
		return
	}
	config, err := api.service.UpdateMicrosoftConfig(r.Context(), request.ClientID)
	if err != nil {
		api.writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, config)
}

func (api *accountsAPI) importAccounts(w http.ResponseWriter, r *http.Request) {
	if !api.authorize(w, r, true) {
		return
	}
	var request struct {
		Data string `json:"data"`
	}
	if err := decodeJSONLimit(w, r, &request, maxAccountImportBytes); err != nil {
		writeAPIError(w, http.StatusBadRequest, "invalid_request", "导入内容格式无效")
		return
	}
	result, err := api.service.Import(r.Context(), request.Data)
	if err != nil {
		var validationError *accounts.ImportValidationError
		if errors.As(err, &validationError) {
			writeAPIError(w, http.StatusBadRequest, "invalid_import", validationError.Message)
			return
		}
		api.writeError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, result)
}

func (api *accountsAPI) startAuthorization(w http.ResponseWriter, r *http.Request) {
	if !api.authorize(w, r, true) {
		return
	}
	result, err := api.service.StartAuthorization(r.Context(), r.PathValue("public_id"))
	if err != nil {
		api.writeError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, result)
}

func (api *accountsAPI) authorization(w http.ResponseWriter, r *http.Request) {
	if !api.authorize(w, r, false) {
		return
	}
	result, err := api.service.Authorization(r.PathValue("job_id"))
	if err != nil {
		api.writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (api *accountsAPI) confirmAuthorization(w http.ResponseWriter, r *http.Request) {
	if !api.authorize(w, r, true) {
		return
	}
	result, err := api.service.ConfirmAuthorization(r.Context(), r.PathValue("job_id"))
	if err != nil {
		api.writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (api *accountsAPI) restartAuthorization(w http.ResponseWriter, r *http.Request) {
	if !api.authorize(w, r, true) {
		return
	}
	result, err := api.service.RestartAuthorization(r.Context(), r.PathValue("job_id"))
	if err != nil {
		api.writeError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, result)
}

func (api *accountsAPI) setStatus(w http.ResponseWriter, r *http.Request) {
	if !api.authorize(w, r, true) {
		return
	}
	var request struct {
		Disabled bool `json:"disabled"`
	}
	if err := decodeJSON(w, r, &request); err != nil {
		writeAPIError(w, http.StatusBadRequest, "invalid_request", "请求格式无效")
		return
	}
	if err := api.service.SetDisabled(r.Context(), r.PathValue("public_id"), request.Disabled); err != nil {
		api.writeError(w, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusNoContent)
}

func (api *accountsAPI) check(w http.ResponseWriter, r *http.Request) {
	if !api.authorize(w, r, true) {
		return
	}
	if err := api.service.CheckAccount(r.Context(), r.PathValue("public_id")); err != nil {
		api.writeError(w, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusNoContent)
}

func (api *accountsAPI) delete(w http.ResponseWriter, r *http.Request) {
	if !api.authorize(w, r, true) {
		return
	}
	if err := api.service.Delete(r.Context(), r.PathValue("public_id")); err != nil {
		api.writeError(w, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusNoContent)
}

func (api *accountsAPI) authorize(w http.ResponseWriter, r *http.Request, requireCSRF bool) bool {
	cookie, err := r.Cookie(api.authService.CookieName())
	if err != nil {
		writeAPIError(w, http.StatusUnauthorized, "invalid_session", "会话已失效，请重新登录")
		return false
	}
	status, err := api.authService.Status(r.Context(), cookie.Value)
	if err != nil {
		api.logger.Error("account authorization check failed", "event", "account_auth_check_failed", "error", err)
		writeAPIError(w, http.StatusInternalServerError, "internal_error", "服务暂时无法完成请求")
		return false
	}
	if !status.Authenticated {
		writeAPIError(w, http.StatusUnauthorized, "invalid_session", "会话已失效，请重新登录")
		return false
	}
	if requireCSRF && subtle.ConstantTimeCompare([]byte(status.CSRFToken), []byte(r.Header.Get("X-CSRF-Token"))) != 1 {
		writeAPIError(w, http.StatusForbidden, "invalid_csrf", "安全校验失败，请刷新页面后重试")
		return false
	}
	return true
}

func (api *accountsAPI) writeError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, accounts.ErrAccountNotFound), errors.Is(err, accounts.ErrAuthorizationNotFound):
		writeAPIError(w, http.StatusNotFound, "not_found", "请求的账号或授权任务不存在")
	case errors.Is(err, accounts.ErrAccountDisabled):
		writeAPIError(w, http.StatusConflict, "account_disabled", "账号已停用")
	case errors.Is(err, accounts.ErrMicrosoftNotConfigured):
		writeAPIError(w, http.StatusConflict, "microsoft_not_configured", "请先在设置页配置 Microsoft Client ID")
	case errors.Is(err, accounts.ErrInvalidMicrosoftClientID):
		writeAPIError(w, http.StatusBadRequest, "invalid_microsoft_client_id", "Client ID 必须是 Microsoft Entra 显示的 GUID")
	case errors.Is(err, accounts.ErrAuthorizationState):
		writeAPIError(w, http.StatusConflict, "authorization_state", "当前授权任务不能执行此操作")
	case errors.Is(err, accounts.ErrDuplicateMicrosoftAccount):
		writeAPIError(w, http.StatusConflict, "duplicate_microsoft_account", "这个 Microsoft 账号已经绑定")
	case errors.Is(err, accounts.ErrReauthorizationRequired):
		writeAPIError(w, http.StatusUnauthorized, "reauth_required", "Microsoft 授权已失效，请重新授权")
	case errors.Is(err, accounts.ErrOAuthConfiguration):
		writeAPIError(w, http.StatusServiceUnavailable, "oauth_configuration", "Microsoft OAuth 应用配置无效")
	case errors.Is(err, accounts.ErrInvalidAccountList):
		writeAPIError(w, http.StatusBadRequest, "invalid_account_filter", "账号搜索或状态筛选无效")
	case errors.Is(err, accounts.ErrInvalidAccountBatch):
		writeAPIError(w, http.StatusBadRequest, "invalid_account_batch", "必须选择 1 到 1000 个账号")
	case errors.Is(err, accounts.ErrAccountBatchLimit):
		writeAPIError(w, http.StatusBadRequest, "account_batch_limit", "匹配账号超过 1000 个，请缩小筛选范围")
	default:
		var retryError *accounts.RetryError
		if errors.As(err, &retryError) {
			writeAPIError(w, http.StatusServiceUnavailable, "oauth_retry", "Microsoft 服务暂时不可用，请稍后重试")
			return
		}
		api.logger.Error("account request failed", "event", "account_request_failed", "error", err)
		writeAPIError(w, http.StatusBadGateway, "account_request_failed", "Microsoft 账号请求未完成")
	}
}
