package httpserver

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math"
	"net/http"
	"strconv"
	"sync"
	"time"

	"outlook-mail-manager/internal/auth"
)

const maxAuthRequestBytes = 64 << 10

type authAPI struct {
	service          *auth.Service
	logger           *slog.Logger
	bootstrapLimiter *fixedWindowLimiter
	loginLimiter     *fixedWindowLimiter
	now              func() time.Time
}

type fixedWindowLimiter struct {
	mu          sync.Mutex
	limit       int
	window      time.Duration
	windowStart time.Time
	count       int
}

func newAuthAPI(service *auth.Service, logger *slog.Logger) *authAPI {
	return &authAPI{
		service:          service,
		logger:           logger,
		bootstrapLimiter: &fixedWindowLimiter{limit: 10, window: 5 * time.Minute},
		loginLimiter:     &fixedWindowLimiter{limit: 10, window: 5 * time.Minute},
		now:              time.Now,
	}
}

func (api *authAPI) register(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/auth/status", api.status)
	mux.HandleFunc("POST /api/auth/setup/start", api.setupStart)
	mux.HandleFunc("POST /api/auth/setup/complete", api.setupComplete)
	mux.HandleFunc("POST /api/auth/login", api.login)
	mux.HandleFunc("POST /api/auth/logout", api.logout)
}

func (api *authAPI) status(w http.ResponseWriter, r *http.Request) {
	status, err := api.service.Status(r.Context(), api.sessionToken(r))
	if err != nil {
		api.writeServiceError(w, err)
		return
	}

	response := map[string]any{
		"initialized":   status.Initialized,
		"authenticated": status.Authenticated,
	}
	if status.Authenticated {
		response["username"] = status.Username
		response["csrf_token"] = status.CSRFToken
		response["session_expires_at"] = status.SessionExpiresAt.UTC().Format(time.RFC3339Nano)
	}
	writeJSON(w, http.StatusOK, response)
}

func (api *authAPI) setupStart(w http.ResponseWriter, r *http.Request) {
	if !api.allow(w, api.bootstrapLimiter) {
		return
	}
	var request struct {
		Username             string `json:"username"`
		Password             string `json:"password"`
		PasswordConfirmation string `json:"password_confirmation"`
	}
	if err := decodeJSON(w, r, &request); err != nil {
		writeAPIError(w, http.StatusBadRequest, "invalid_request", "请求格式无效")
		return
	}
	if request.Password != request.PasswordConfirmation {
		writeAPIError(w, http.StatusBadRequest, "password_mismatch", "两次输入的密码不一致")
		return
	}

	start, err := api.service.StartSetup(r.Context(), request.Username, request.Password)
	if err != nil {
		api.writeServiceError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"challenge_id":     start.ChallengeID,
		"secret":           start.Secret,
		"provisioning_uri": start.ProvisioningURI,
		"expires_at":       start.ExpiresAt.UTC().Format(time.RFC3339Nano),
	})
}

func (api *authAPI) setupComplete(w http.ResponseWriter, r *http.Request) {
	if !api.allow(w, api.bootstrapLimiter) {
		return
	}
	var request struct {
		ChallengeID string `json:"challenge_id"`
		Passcode    string `json:"passcode"`
	}
	if err := decodeJSON(w, r, &request); err != nil {
		writeAPIError(w, http.StatusBadRequest, "invalid_request", "请求格式无效")
		return
	}

	grant, err := api.service.CompleteSetup(r.Context(), request.ChallengeID, request.Passcode)
	if err != nil {
		api.writeServiceError(w, err)
		return
	}
	api.setSessionCookie(w, grant)
	writeJSON(w, http.StatusCreated, map[string]any{
		"csrf_token":         grant.CSRFToken,
		"session_expires_at": grant.ExpiresAt.UTC().Format(time.RFC3339Nano),
	})
}

func (api *authAPI) login(w http.ResponseWriter, r *http.Request) {
	if !api.allow(w, api.loginLimiter) {
		return
	}
	var request struct {
		Username string `json:"username"`
		Password string `json:"password"`
		Passcode string `json:"passcode"`
	}
	if err := decodeJSON(w, r, &request); err != nil {
		writeAPIError(w, http.StatusBadRequest, "invalid_request", "请求格式无效")
		return
	}

	grant, err := api.service.Login(r.Context(), request.Username, request.Password, request.Passcode)
	if err != nil {
		api.writeServiceError(w, err)
		return
	}
	api.setSessionCookie(w, grant)
	writeJSON(w, http.StatusOK, map[string]any{
		"csrf_token":         grant.CSRFToken,
		"session_expires_at": grant.ExpiresAt.UTC().Format(time.RFC3339Nano),
	})
}

func (api *authAPI) logout(w http.ResponseWriter, r *http.Request) {
	err := api.service.Logout(r.Context(), api.sessionToken(r), r.Header.Get("X-CSRF-Token"))
	if err != nil {
		api.writeServiceError(w, err)
		return
	}
	api.clearSessionCookie(w)
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusNoContent)
}

func (api *authAPI) allow(w http.ResponseWriter, limiter *fixedWindowLimiter) bool {
	allowed, retryAfter := limiter.allow(api.now().UTC())
	if allowed {
		return true
	}
	w.Header().Set("Retry-After", strconv.Itoa(retryAfter))
	writeAPIError(w, http.StatusTooManyRequests, "rate_limited", "尝试次数过多，请稍后再试")
	return false
}

func (limiter *fixedWindowLimiter) allow(now time.Time) (bool, int) {
	limiter.mu.Lock()
	defer limiter.mu.Unlock()
	if limiter.windowStart.IsZero() || now.Sub(limiter.windowStart) >= limiter.window {
		limiter.windowStart = now
		limiter.count = 0
	}
	if limiter.count >= limiter.limit {
		retry := int(math.Ceil(limiter.windowStart.Add(limiter.window).Sub(now).Seconds()))
		if retry < 1 {
			retry = 1
		}
		return false, retry
	}
	limiter.count++
	return true, 0
}

func (api *authAPI) sessionToken(r *http.Request) string {
	cookie, err := r.Cookie(api.service.CookieName())
	if err != nil {
		return ""
	}
	return cookie.Value
}

func (api *authAPI) setSessionCookie(w http.ResponseWriter, grant auth.SessionGrant) {
	http.SetCookie(w, &http.Cookie{
		Name:     api.service.CookieName(),
		Value:    grant.Token,
		Path:     "/",
		Expires:  grant.ExpiresAt,
		HttpOnly: true,
		Secure:   api.service.SecureCookies(),
		SameSite: http.SameSiteLaxMode,
	})
}

func (api *authAPI) clearSessionCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name:     api.service.CookieName(),
		Value:    "",
		Path:     "/",
		Expires:  time.Unix(1, 0),
		MaxAge:   -1,
		HttpOnly: true,
		Secure:   api.service.SecureCookies(),
		SameSite: http.SameSiteLaxMode,
	})
}

func (api *authAPI) writeServiceError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, auth.ErrUsernamePolicy):
		writeAPIError(w, http.StatusBadRequest, "username_policy", "管理员账号需要 3 到 64 个字符，只能包含字母、数字及 . _ - @")
	case errors.Is(err, auth.ErrPasswordPolicy):
		writeAPIError(w, http.StatusBadRequest, "password_policy", "密码至少需要 12 个字符")
	case errors.Is(err, auth.ErrAlreadyInitialized):
		writeAPIError(w, http.StatusConflict, "already_initialized", "管理员已经完成初始化")
	case errors.Is(err, auth.ErrNotInitialized):
		writeAPIError(w, http.StatusConflict, "not_initialized", "管理员尚未初始化")
	case errors.Is(err, auth.ErrInvalidChallenge):
		writeAPIError(w, http.StatusUnauthorized, "invalid_challenge", "初始化会话已失效，请重新开始")
	case errors.Is(err, auth.ErrInvalidFactor), errors.Is(err, auth.ErrInvalidCredentials):
		writeAPIError(w, http.StatusUnauthorized, "invalid_credentials", "密码或验证信息无效")
	case errors.Is(err, auth.ErrInvalidSession):
		writeAPIError(w, http.StatusUnauthorized, "invalid_session", "会话已失效，请重新登录")
	case errors.Is(err, auth.ErrInvalidCSRF):
		writeAPIError(w, http.StatusForbidden, "invalid_csrf", "安全校验失败，请刷新页面后重试")
	default:
		api.logger.Error("authentication request failed", "event", "auth_request_failed", "error", err)
		writeAPIError(w, http.StatusInternalServerError, "internal_error", "服务暂时无法完成请求")
	}
}

func decodeJSON(w http.ResponseWriter, r *http.Request, destination any) error {
	return decodeJSONLimit(w, r, destination, maxAuthRequestBytes)
}

func decodeJSONLimit(w http.ResponseWriter, r *http.Request, destination any, limit int64) error {
	r.Body = http.MaxBytesReader(w, r.Body, limit)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		return err
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return fmt.Errorf("request must contain one JSON object")
	}
	return nil
}

func writeAPIError(w http.ResponseWriter, status int, code, message string) {
	writeJSON(w, status, map[string]any{
		"error": map[string]string{
			"code":    code,
			"message": message,
		},
	})
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
