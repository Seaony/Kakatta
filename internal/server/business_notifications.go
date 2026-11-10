package server

import (
	"errors"
	"net/http"
	"net/url"

	"github.com/Seaony/Kakatta/internal/notification"
	"github.com/Seaony/Kakatta/internal/notify"
)

type notificationHandler struct {
	*businessHandler
	notifications *notification.Service
}

func notificationRoutes(server *Server, notifications *notification.Service) []businessRoute {
	b := &notificationHandler{businessHandler: &businessHandler{server: server}, notifications: notifications}
	for _, route := range []struct {
		pattern string
		handler http.HandlerFunc
	}{
		{"GET /api/v1/channels", b.listChannels},
		{"POST /api/v1/channels", b.createChannel},
		{"GET /api/v1/channels/{channel_id}", b.getChannel},
		{"PATCH /api/v1/channels/{channel_id}", b.updateChannel},
		{"DELETE /api/v1/channels/{channel_id}", b.deleteChannel},
		{"POST /api/v1/channels/{channel_id}/test", b.testChannel},
		{"GET /api/v1/channels/{channel_id}/deliveries", b.channelDeliveries},
		{"GET /api/v1/deliveries/{delivery_id}/items", b.deliveryItems},
	} {
		b.register(route.pattern, route.handler)
	}
	return b.routes
}

func (s *Server) RegisterNotifications(notifications *notification.Service) error {
	if notifications == nil {
		return errors.New("通知服务不能为空")
	}
	probe := &http.Request{Method: http.MethodGet, URL: &url.URL{Path: "/api/v1/channels"}}
	if _, pattern := s.mux.Handler(probe); pattern != "" && pattern != "/" {
		return errors.New("通知接口已经注册")
	}
	for _, route := range notificationRoutes(s, notifications) {
		s.RegisterProtected(route.pattern, route.handler)
	}
	return nil
}

func notificationRequestID(r *http.Request, key string, body bool) (int64, error) {
	if _, err := queryValues(r); err != nil {
		return 0, err
	}
	if !body {
		if err := noBody(r); err != nil {
			return 0, err
		}
	}
	return pathID(r, key)
}

func (b *notificationHandler) listChannels(w http.ResponseWriter, r *http.Request) {
	_, err := queryValues(r)
	if err == nil {
		err = noBody(r)
	}
	if err != nil {
		b.failure(w, r, err)
		return
	}
	result, err := b.notifications.ListChannels(r.Context())
	if err == nil {
		for index := range result {
			result[index], err = channelToHTTP(result[index])
			if err != nil {
				break
			}
		}
	}
	b.reply(w, r, http.StatusOK, result, err)
}

func (b *notificationHandler) createChannel(w http.ResponseWriter, r *http.Request) {
	if _, err := queryValues(r); err != nil {
		b.failure(w, r, err)
		return
	}
	patch, err := readPatch(w, r)
	if err != nil {
		b.failure(w, r, err)
		return
	}
	patch, err = channelPatchFromHTTP(notification.ChannelPatch(patch), "")
	if err != nil {
		b.failure(w, r, err)
		return
	}
	result, err := b.notifications.CreateChannel(r.Context(), notification.ChannelPatch(patch))
	if err == nil {
		result, err = channelToHTTP(result)
	}
	b.reply(w, r, http.StatusCreated, result, err)
}

func (b *notificationHandler) getChannel(w http.ResponseWriter, r *http.Request) {
	id, err := notificationRequestID(r, "channel_id", false)
	if err != nil {
		b.failure(w, r, err)
		return
	}
	result, err := b.notifications.GetChannel(r.Context(), id)
	if err == nil {
		result, err = channelToHTTP(result)
	}
	b.reply(w, r, http.StatusOK, result, err)
}

func (b *notificationHandler) updateChannel(w http.ResponseWriter, r *http.Request) {
	id, err := notificationRequestID(r, "channel_id", true)
	if err != nil {
		b.failure(w, r, err)
		return
	}
	patch, err := readPatch(w, r)
	if err != nil {
		b.failure(w, r, err)
		return
	}
	var currentType notify.Type
	if _, present := patch["type"]; !present {
		current, err := b.notifications.GetChannel(r.Context(), id)
		if err != nil {
			b.failure(w, r, err)
			return
		}
		currentType = current.Type
	}
	patch, err = channelPatchFromHTTP(notification.ChannelPatch(patch), currentType)
	if err != nil {
		b.failure(w, r, err)
		return
	}
	result, err := b.notifications.UpdateChannel(r.Context(), id, notification.ChannelPatch(patch))
	if err == nil {
		result, err = channelToHTTP(result)
	}
	b.reply(w, r, http.StatusOK, result, err)
}

func (b *notificationHandler) deleteChannel(w http.ResponseWriter, r *http.Request) {
	id, err := notificationRequestID(r, "channel_id", false)
	if err != nil {
		b.failure(w, r, err)
		return
	}
	result, err := b.notifications.DeleteChannel(r.Context(), id)
	b.reply(w, r, http.StatusOK, result, err)
}

func (b *notificationHandler) testChannel(w http.ResponseWriter, r *http.Request) {
	id, err := notificationRequestID(r, "channel_id", false)
	if err != nil {
		b.failure(w, r, err)
		return
	}
	result, err := b.notifications.TestChannel(r.Context(), id)
	b.reply(w, r, http.StatusOK, result, err)
}

func (b *notificationHandler) channelDeliveries(w http.ResponseWriter, r *http.Request) {
	id, err := notificationRequestID(r, "channel_id", false)
	if err != nil {
		b.failure(w, r, err)
		return
	}
	result, err := b.notifications.ChannelDeliveries(r.Context(), id)
	b.reply(w, r, http.StatusOK, result, err)
}

func (b *notificationHandler) deliveryItems(w http.ResponseWriter, r *http.Request) {
	id, err := notificationRequestID(r, "delivery_id", false)
	if err != nil {
		b.failure(w, r, err)
		return
	}
	result, err := b.notifications.DeliveryItems(r.Context(), id)
	b.reply(w, r, http.StatusOK, result, err)
}
