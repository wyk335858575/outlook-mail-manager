package httpserver

import (
	"context"
	"errors"
	"log/slog"
	"net/http"

	"outlook-mail-manager/internal/auth"
	"outlook-mail-manager/internal/notify"
)

type notificationService interface {
	ListChannels(context.Context) ([]notify.Channel, error)
	CreateChannel(context.Context, notify.ChannelInput) (notify.Channel, error)
	UpdateChannel(context.Context, string, notify.ChannelInput) (notify.Channel, error)
	DeleteChannel(context.Context, string) error
	TestChannel(context.Context, string) (notify.Delivery, error)
	ListRules(context.Context) ([]notify.Rule, error)
	CreateRule(context.Context, notify.Rule) (notify.Rule, error)
	UpdateRule(context.Context, string, notify.Rule) (notify.Rule, error)
	DeleteRule(context.Context, string) error
	ListDeliveries(context.Context, int) ([]notify.Delivery, error)
	RetryDelivery(context.Context, string) error
}

type notificationsAPI struct {
	service     notificationService
	authService *auth.Service
	logger      *slog.Logger
}

func newNotificationsAPI(service notificationService, authService *auth.Service, logger *slog.Logger) *notificationsAPI {
	return &notificationsAPI{service: service, authService: authService, logger: logger}
}

func (api *notificationsAPI) register(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/notifications/channels", api.channels)
	mux.HandleFunc("POST /api/notifications/channels", api.createChannel)
	mux.HandleFunc("PUT /api/notifications/channels/{public_id}", api.updateChannel)
	mux.HandleFunc("DELETE /api/notifications/channels/{public_id}", api.deleteChannel)
	mux.HandleFunc("POST /api/notifications/channels/{public_id}/test", api.testChannel)
	mux.HandleFunc("GET /api/notifications/rules", api.rules)
	mux.HandleFunc("POST /api/notifications/rules", api.createRule)
	mux.HandleFunc("PUT /api/notifications/rules/{public_id}", api.updateRule)
	mux.HandleFunc("DELETE /api/notifications/rules/{public_id}", api.deleteRule)
	mux.HandleFunc("GET /api/notifications/deliveries", api.deliveries)
	mux.HandleFunc("POST /api/notifications/deliveries/{public_id}/retry", api.retryDelivery)
}

func (api *notificationsAPI) channels(w http.ResponseWriter, r *http.Request) {
	if !authorizeAdministrator(w, r, api.authService, api.logger, false) {
		return
	}
	items, err := api.service.ListChannels(r.Context())
	if err != nil {
		api.writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"channels": items})
}

func (api *notificationsAPI) createChannel(w http.ResponseWriter, r *http.Request) {
	if !authorizeAdministrator(w, r, api.authService, api.logger, true) {
		return
	}
	var input notify.ChannelInput
	if err := decodeJSON(w, r, &input); err != nil {
		writeAPIError(w, http.StatusBadRequest, "invalid_request", "请求格式无效")
		return
	}
	item, err := api.service.CreateChannel(r.Context(), input)
	if err != nil {
		api.writeError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, item)
}

func (api *notificationsAPI) updateChannel(w http.ResponseWriter, r *http.Request) {
	if !authorizeAdministrator(w, r, api.authService, api.logger, true) {
		return
	}
	var input notify.ChannelInput
	if err := decodeJSON(w, r, &input); err != nil {
		writeAPIError(w, http.StatusBadRequest, "invalid_request", "请求格式无效")
		return
	}
	item, err := api.service.UpdateChannel(r.Context(), r.PathValue("public_id"), input)
	if err != nil {
		api.writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, item)
}

func (api *notificationsAPI) deleteChannel(w http.ResponseWriter, r *http.Request) {
	if !authorizeAdministrator(w, r, api.authService, api.logger, true) {
		return
	}
	if err := api.service.DeleteChannel(r.Context(), r.PathValue("public_id")); err != nil {
		api.writeError(w, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusNoContent)
}

func (api *notificationsAPI) testChannel(w http.ResponseWriter, r *http.Request) {
	if !authorizeAdministrator(w, r, api.authService, api.logger, true) {
		return
	}
	item, err := api.service.TestChannel(r.Context(), r.PathValue("public_id"))
	if err != nil {
		api.writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, item)
}

func (api *notificationsAPI) rules(w http.ResponseWriter, r *http.Request) {
	if !authorizeAdministrator(w, r, api.authService, api.logger, false) {
		return
	}
	items, err := api.service.ListRules(r.Context())
	if err != nil {
		api.writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"rules": items})
}

func (api *notificationsAPI) createRule(w http.ResponseWriter, r *http.Request) {
	if !authorizeAdministrator(w, r, api.authService, api.logger, true) {
		return
	}
	var input notify.Rule
	if err := decodeJSON(w, r, &input); err != nil {
		writeAPIError(w, http.StatusBadRequest, "invalid_request", "请求格式无效")
		return
	}
	item, err := api.service.CreateRule(r.Context(), input)
	if err != nil {
		api.writeError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, item)
}

func (api *notificationsAPI) updateRule(w http.ResponseWriter, r *http.Request) {
	if !authorizeAdministrator(w, r, api.authService, api.logger, true) {
		return
	}
	var input notify.Rule
	if err := decodeJSON(w, r, &input); err != nil {
		writeAPIError(w, http.StatusBadRequest, "invalid_request", "请求格式无效")
		return
	}
	item, err := api.service.UpdateRule(r.Context(), r.PathValue("public_id"), input)
	if err != nil {
		api.writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, item)
}

func (api *notificationsAPI) deleteRule(w http.ResponseWriter, r *http.Request) {
	if !authorizeAdministrator(w, r, api.authService, api.logger, true) {
		return
	}
	if err := api.service.DeleteRule(r.Context(), r.PathValue("public_id")); err != nil {
		api.writeError(w, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusNoContent)
}

func (api *notificationsAPI) deliveries(w http.ResponseWriter, r *http.Request) {
	if !authorizeAdministrator(w, r, api.authService, api.logger, false) {
		return
	}
	items, err := api.service.ListDeliveries(r.Context(), 200)
	if err != nil {
		api.writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"deliveries": items})
}

func (api *notificationsAPI) retryDelivery(w http.ResponseWriter, r *http.Request) {
	if !authorizeAdministrator(w, r, api.authService, api.logger, true) {
		return
	}
	if err := api.service.RetryDelivery(r.Context(), r.PathValue("public_id")); err != nil {
		api.writeError(w, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusNoContent)
}

func (api *notificationsAPI) writeError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, notify.ErrInvalidChannel):
		writeAPIError(w, http.StatusBadRequest, "invalid_notification_channel", "通知通道配置无效")
	case errors.Is(err, notify.ErrChannelNotFound):
		writeAPIError(w, http.StatusNotFound, "notification_channel_not_found", "通知通道不存在")
	case errors.Is(err, notify.ErrInvalidRule):
		writeAPIError(w, http.StatusBadRequest, "invalid_notification_rule", "通知规则无效")
	case errors.Is(err, notify.ErrRuleNotFound):
		writeAPIError(w, http.StatusNotFound, "notification_rule_not_found", "通知规则不存在")
	case errors.Is(err, notify.ErrDeliveryNotFound):
		writeAPIError(w, http.StatusNotFound, "notification_delivery_not_found", "通知记录不存在或不可重试")
	default:
		api.logger.Error("notification request failed", "event", "notification_request_failed", "error", err)
		writeAPIError(w, http.StatusInternalServerError, "notification_request_failed", "通知服务暂时无法完成请求")
	}
}
