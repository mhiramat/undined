// Package events implements a lightweight in-memory pub/sub Event Hub and SSE transport for UndineD.
package events

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"sync"
	"sync/atomic"
	"time"
)

const (
	defaultRingBufferSize   = 128
	clientChanBuffer        = 32
	defaultHeartbeatInterval = 30 * time.Second
)

// Event represents an invalidation or notification message broadcast over SSE.
type Event struct {
	ID      uint64 `json:"id"`
	Type    string `json:"type"`
	Payload any    `json:"payload"`
}

// Hub manages pub/sub broadcasting and replay buffering for SSE clients.
type Hub struct {
	mu                sync.RWMutex
	clients           map[chan Event]struct{}
	ring              []Event
	seq               uint64
	maxRingSize       int
	heartbeatInterval time.Duration
}

// NewHub initializes and returns a new Event Hub.
func NewHub() *Hub {
	return &Hub{
		clients:           make(map[chan Event]struct{}),
		ring:              make([]Event, 0, defaultRingBufferSize),
		maxRingSize:       defaultRingBufferSize,
		heartbeatInterval: defaultHeartbeatInterval,
	}
}

// SetHeartbeatInterval overrides the default heartbeat ticker interval (useful in tests).
func (h *Hub) SetHeartbeatInterval(d time.Duration) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.heartbeatInterval = d
}

// ClientCount returns the number of currently connected SSE clients.
func (h *Hub) ClientCount() int {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return len(h.clients)
}

// RingSize returns the number of events currently cached in the replay buffer.
func (h *Hub) RingSize() int {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return len(h.ring)
}

// Publish increments sequence counter, stores event in the replay buffer, and fans out to clients.
func (h *Hub) Publish(eventType string, payload any) {
	newSeq := atomic.AddUint64(&h.seq, 1)
	ev := Event{
		ID:      newSeq,
		Type:    eventType,
		Payload: payload,
	}

	h.mu.Lock()
	if len(h.ring) >= h.maxRingSize {
		h.ring = append(h.ring[1:], ev)
	} else {
		h.ring = append(h.ring, ev)
	}

	for ch := range h.clients {
		select {
		case ch <- ev:
		default:
			// Client channel full; skip to prevent slow clients from blocking
		}
	}
	h.mu.Unlock()
}

// ServeHTTP serves the SSE endpoint GET /api/events.
func (h *Hub) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "Streaming unsupported", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")

	clientChan := make(chan Event, clientChanBuffer)

	h.mu.Lock()
	h.clients[clientChan] = struct{}{}
	hbInterval := h.heartbeatInterval

	lastEventID := r.Header.Get("Last-Event-ID")
	if lastEventID == "" {
		lastEventID = r.URL.Query().Get("last_event_id")
	}
	missedEvents, resyncNeeded := h.collectMissedEventsLocked(lastEventID)
	h.mu.Unlock()

	defer func() {
		h.mu.Lock()
		delete(h.clients, clientChan)
		close(clientChan)
		h.mu.Unlock()
	}()

	// Send initial connection comment
	_, _ = fmt.Fprintf(w, ": connected\n\n")
	flusher.Flush()

	// Handle replay or overflow resync trigger
	if resyncNeeded {
		writeSSEFrame(w, Event{
			ID:      atomic.LoadUint64(&h.seq),
			Type:    "system.resync_required",
			Payload: map[string]any{"reason": "missed_events"},
		})
		flusher.Flush()
	} else {
		for _, ev := range missedEvents {
			writeSSEFrame(w, ev)
		}
		if len(missedEvents) > 0 {
			flusher.Flush()
		}
	}

	ticker := time.NewTicker(hbInterval)
	defer ticker.Stop()

	ctx := r.Context()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			_, _ = fmt.Fprintf(w, ": heartbeat\n\n")
			flusher.Flush()
		case ev, ok := <-clientChan:
			if !ok {
				return
			}
			writeSSEFrame(w, ev)
			flusher.Flush()
		}
	}
}

func (h *Hub) collectMissedEventsLocked(lastIDStr string) ([]Event, bool) {
	missed := make([]Event, 0)
	if lastIDStr == "" {
		return missed, false
	}
	lastID, err := strconv.ParseUint(lastIDStr, 10, 64)
	if err != nil || len(h.ring) == 0 {
		return missed, false
	}
	oldestID := h.ring[0].ID
	if lastID < oldestID-1 {
		return missed, true // Overflow: client missed more events than ring buffer holds
	}
	for _, ev := range h.ring {
		if ev.ID > lastID {
			missed = append(missed, ev)
		}
	}
	return missed, false
}

func writeSSEFrame(w io.Writer, ev Event) {
	data, err := json.Marshal(ev.Payload)
	if err != nil {
		return
	}
	_, _ = fmt.Fprintf(w, "id: %d\nevent: %s\ndata: %s\n\n", ev.ID, ev.Type, data)
}
