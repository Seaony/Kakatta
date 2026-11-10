package server

import (
	"context"
	"net/http"
	"time"
)

func (s *Server) events(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodHead {
		w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		return
	}
	lastID := r.Header.Get("Last-Event-ID")
	if len(r.Header.Values("Last-Event-ID")) > 1 || len(lastID) > 128 {
		lastID = "invalid"
	}
	replay, subscriber, err := s.broker.subscribe(lastID)
	if err != nil {
		WriteError(w, r, http.StatusServiceUnavailable, message("http.unavailable"), nil)
		return
	}
	defer s.broker.unsubscribe(subscriber)
	identity := currentIdentity(r)
	validate := func() error {
		ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
		defer cancel()
		return s.auth.Validate(ctx, identity)
	}
	if err := validate(); err != nil {
		s.writeFailure(w, r, err)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
	w.Header().Set("X-Accel-Buffering", "no")
	controller := http.NewResponseController(w)
	write := func(value string) bool {
		if err := controller.SetWriteDeadline(time.Now().Add(5 * time.Second)); err != nil {
			return false
		}
		defer controller.SetWriteDeadline(time.Time{})
		if _, err := w.Write([]byte(value)); err != nil {
			return false
		}
		return controller.Flush() == nil
	}
	if !write(": connected\n\n") {
		return
	}
	alive := func() bool {
		select {
		case <-r.Context().Done():
			return false
		case <-s.closed:
			return false
		case <-subscriber.done:
			return false
		default:
			return true
		}
	}
	for _, event := range replay {
		if !alive() || validate() != nil || !write(encodeEvent(event)) {
			return
		}
	}
	ticker := time.NewTicker(s.heartbeat)
	defer ticker.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case <-s.closed:
			return
		case <-subscriber.done:
			return
		default:
		}
		select {
		case <-r.Context().Done():
			return
		case <-s.closed:
			return
		case <-subscriber.done:
			return
		case event := <-subscriber.queue:
			if !alive() || validate() != nil || !write(encodeEvent(event)) {
				return
			}
		case <-ticker.C:
			if !alive() || validate() != nil || !write(": keepalive\n\n") {
				return
			}
		}
	}
}
