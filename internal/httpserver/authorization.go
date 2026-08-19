package httpserver

import (
	"crypto/subtle"
	"log/slog"
	"net/http"

	"outlook-mail-manager/internal/auth"
)

func authorizeAdministrator(
	w http.ResponseWriter,
	r *http.Request,
	authService *auth.Service,
	logger *slog.Logger,
	requireCSRF bool,
) bool {
	cookie, err := r.Cookie(authService.CookieName())
	if err != nil {
		writeAPIError(w, http.StatusUnauthorized, "invalid_session", "会话已失效，请重新登录")
		return false
	}
	status, err := authService.Status(r.Context(), cookie.Value)
	if err != nil {
		logger.Error("administrator authorization check failed", "event", "admin_auth_check_failed", "error", err)
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
