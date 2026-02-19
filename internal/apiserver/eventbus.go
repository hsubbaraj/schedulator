package apiserver

import (
	"sync"
	"time"
)

// Event is a scheduler event published to SSE subscribers.
type Event struct {
	Type      string      `json:"type"`
	Timestamp time.Time   `json:"timestamp"`
	Data      interface{} `json:"data"`
}

// EventBus is a simple pub-sub for SSE streaming.
type EventBus struct {
	mu          sync.RWMutex
	subscribers map[int]chan Event
	nextID      int
}

// NewEventBus creates a new EventBus.
func NewEventBus() *EventBus {
	return &EventBus{
		subscribers: make(map[int]chan Event),
	}
}

// Subscribe returns a channel that receives events and a function to unsubscribe.
func (eb *EventBus) Subscribe() (<-chan Event, func()) {
	eb.mu.Lock()
	defer eb.mu.Unlock()

	id := eb.nextID
	eb.nextID++
	ch := make(chan Event, 32)
	eb.subscribers[id] = ch

	return ch, func() {
		eb.mu.Lock()
		defer eb.mu.Unlock()
		delete(eb.subscribers, id)
		close(ch)
	}
}

// Publish sends an event to all subscribers. Non-blocking: slow subscribers
// have their events dropped.
func (eb *EventBus) Publish(e Event) {
	eb.mu.RLock()
	defer eb.mu.RUnlock()

	for _, ch := range eb.subscribers {
		select {
		case ch <- e:
		default:
			// Drop event for slow subscriber.
		}
	}
}
