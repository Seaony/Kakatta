package server

import (
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"unicode/utf8"

	"github.com/Seaony/Kakatta/internal/locales"
)

const (
	eventHistory = 256
	eventQueue   = 32
	maxEventSize = 64 << 10
)

var errEventsClosed = errors.New("事件流已关闭")

type Event struct {
	Type   string           `json:"type"`
	Reason *locales.Message `json:"reason,omitempty"`
	Data   json.RawMessage  `json:"data,omitempty"`
}

type wireEvent struct {
	id, kind string
	data     []byte
}

type eventSubscriber struct {
	queue chan wireEvent
	done  chan struct{}
}

type eventBroker struct {
	mu          sync.Mutex
	epoch       string
	sequence    uint64
	history     []wireEvent
	subscribers map[*eventSubscriber]struct{}
	closed      bool
}

func newEventBroker() *eventBroker {
	return &eventBroker{epoch: rand.Text(), subscribers: map[*eventSubscriber]struct{}{}}
}

func (s *Server) Publish(event Event) error { return s.broker.publish(event) }

func (b *eventBroker) publish(event Event) error {
	if len(event.Type) == 0 || len(event.Type) > 64 {
		return errors.New("事件类型无效")
	}
	for _, char := range event.Type {
		if char != '_' && char != '-' && char != '.' && (char < 'a' || char > 'z') && (char < 'A' || char > 'Z') && (char < '0' || char > '9') {
			return errors.New("事件类型无效")
		}
	}
	if len(event.Data) != 0 && (!utf8.Valid(event.Data) || !json.Valid(event.Data)) {
		return errors.New("事件数据无效")
	}
	data, err := json.Marshal(event)
	if err != nil || len(data) > maxEventSize {
		return errors.New("事件数据无效或过大")
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		return errEventsClosed
	}
	b.sequence++
	wire := wireEvent{id: b.id(), kind: event.Type, data: data}
	b.history = append(b.history, wire)
	if len(b.history) > eventHistory {
		copy(b.history, b.history[1:])
		b.history[eventHistory] = wireEvent{}
		b.history = b.history[:eventHistory]
	}
	for subscriber := range b.subscribers {
		select {
		case subscriber.queue <- wire:
		default:
			close(subscriber.done)
			delete(b.subscribers, subscriber)
		}
	}
	return nil
}

func (b *eventBroker) id() string { return b.epoch + "." + strconv.FormatUint(b.sequence, 10) }

func (b *eventBroker) subscribe(lastID string) ([]wireEvent, *eventSubscriber, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		return nil, nil, errEventsClosed
	}
	subscriber := &eventSubscriber{queue: make(chan wireEvent, eventQueue), done: make(chan struct{})}
	b.subscribers[subscriber] = struct{}{}
	replay := []wireEvent{}
	if lastID == "" {
		return replay, subscriber, nil
	}
	epoch, raw, found := strings.Cut(lastID, ".")
	sequence, err := strconv.ParseUint(raw, 10, 64)
	valid := found && epoch == b.epoch && err == nil && raw == strconv.FormatUint(sequence, 10) && sequence <= b.sequence
	if len(b.history) != 0 && sequence < b.sequence-uint64(len(b.history)) {
		valid = false
	}
	if !valid {
		data, _ := json.Marshal(Event{Type: "reset", Reason: &locales.Message{Code: "events.reset"}})
		return []wireEvent{{id: b.id(), kind: "reset", data: data}}, subscriber, nil
	}
	for index, event := range b.history {
		if b.sequence-uint64(len(b.history))+uint64(index)+1 > sequence {
			replay = append(replay, event)
		}
	}
	return replay, subscriber, nil
}

func (b *eventBroker) unsubscribe(subscriber *eventSubscriber) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if _, exists := b.subscribers[subscriber]; exists {
		delete(b.subscribers, subscriber)
		close(subscriber.done)
	}
}

func (b *eventBroker) close() {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		return
	}
	b.closed = true
	for subscriber := range b.subscribers {
		close(subscriber.done)
		delete(b.subscribers, subscriber)
	}
}

func encodeEvent(event wireEvent) string {
	return fmt.Sprintf("id: %s\nevent: %s\ndata: %s\n\n", event.id, event.kind, event.data)
}
