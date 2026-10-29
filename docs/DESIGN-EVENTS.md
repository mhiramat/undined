# DESIGN-EVENTS.md: Real-Time Event Notification Channel (SSE State Invalidation)

## 1. Overview & Objectives


This architecture strictly follows the **State Invalidation (Trigger-then-Fetch) Pattern**:
1. **Lightweight Push Triggers**: The SSE stream pushes tiny JSON notification payloads containing only the event type and target resource identifiers (e.g., `folder`, `message_id`, `root_message_id`)[cite: 1, 2]. Full email bodies or patch diffs are **never** transmitted over the SSE channel[cite: 1, 8].
2. **REST API Single Source of Truth**: Upon receiving an invalidation event, the client debounces incoming triggers and queries existing REST API endpoints (`GET /api/folders`, `GET /api/messages`, `GET /api/threads/{id}`, `GET /api/reviews/comments`) to fetch the updated state[cite: 1, 2].
3. **Zero External Dependencies**: Implemented purely using Go's standard library (`net/http`, `http.Flusher`, `sync`) to preserve UndineD's single-binary, minimal-footprint philosophy[cite: 8].

---

## 2. System Architecture & Data Flow

```text
[ Background Producers (Go Goroutines) ]
  ├─ IMAP Poller (New mail synced) ──────────┐
  ├─ b4 Runner (Thread hydration finished) ──┼──▶ [ internal/events.Hub (In-Memory Pub/Sub) ]
  ├─ Review API / MCP (Inline comment added) ┤              │
  └─ Patch Runner (git am / b4 am status) ───┘              │ 1. Push lightweight SSE frame
                                                            ▼
                                              GET /api/events (text/event-stream)
                                                            │
     ┌──────────────────────────────────────────────────────┴───────┐
     ▼                                                              ▼
[ Web UI (Browser EventSource) ]                      [ VS Code Extension (undine-vscode) ]
     │                                                              │
     └────── 2. Debounce & Fetch updated data via existing REST ────┘
             (GET /api/folders, GET /api/messages, GET /api/reviews/comments)

```

---

## 3. SSE Protocol & Event Specification

### 3.1 Endpoint Definition

* **Endpoint**: `GET /api/events`


* **Response Headers**:
* `Content-Type: text/event-stream`
* `Cache-Control: no-cache`
* `Connection: keep-alive`
* `X-Accel-Buffering: no` (Prevents buffering in reverse proxies)


* **Authentication**:
* Must pass UndineD's standard authentication middleware:


* **Web Browser (`EventSource`)**: Authenticated via the `HttpOnly; SameSite=Strict` session cookie established during initial bootstrap.


* **VS Code Extension / Mobile PWA / CLI**: Authenticated via `Authorization: Bearer <token>` HTTP header.






* **Keep-Alive Heartbeat**:
* The server emits an SSE comment (`: heartbeat\n\n`) every 30 seconds to prevent idle connection timeouts and detect dead TCP sockets.



### 3.2 Wire Format

Each event frame adheres to the W3C Server-Sent Events specification:

```http
id: 1042
event: mail.sync_completed
data: {"folder":"INBOX","new_count":3,"timestamp":1761722400}


```

### 3.3 Standard Event Catalog

| Event Name (`event:`) | Producer Component | JSON `data:` Payload Schema | Expected Client Action |
| --- | --- | --- | --- |
| `mail.sync_completed` | IMAP Poller | `{"folder": "string", "new_count": int, "timestamp": int64}` | Fetch `GET /api/folders`. If the active view matches `folder`, fetch `GET /api/messages?folder=<folder>`.

 |
| `message.updated` | Mail Store / Tag API | `{"message_id": "string", "folder": "string", "flags": ["string"]}` | Refresh message flags/tags in the active list or detail view.

 |
| `thread.hydrated` | `b4` Runner (`import-b4`) | `{"root_message_id": "string", "imported_count": int, "timestamp": int64}` | If viewing the affected thread or folder, fetch `GET /api/threads/{id}` and `GET /api/folders`.

 |
| `review.comment_added` | REST API / MCP (`post_review_comment`) | `{"message_id": "string", "file": "string", "line": int, "source": "string"}` | If viewing `message_id`, fetch `GET /api/reviews/comments?message_id=<id>` to render new inline comment bubbles.

 |
| `draft.created` | Composer / MCP (`create_review_draft`) | `{"draft_message_id": "string", "in_reply_to": "string"}` | Fetch `GET /api/folders` (update `Drafts` count) and notify user.

 |
| `patch.applied` | Patch Runner (`git am`) | `{"message_id": "string", "success": bool, "output": "string"}` | Display toast notification with patch application status.

 |
| `system.resync_required` | Event Hub (Replay Overflow) | `{"reason": "missed_events", "latest_id": uint64}` | Client missed events while disconnected; trigger full refresh of `GET /api/folders` and current view.

 |

---

## 4. Backend Implementation Specification (Go)

### 4.1 Package Location

Create a new package at `internal/events/hub.go` (and `internal/events/hub_test.go`).

### 4.2 Core Data Structures & Ring Buffer Replay

To handle brief network drops or laptop suspend/resume cycles gracefully, `Hub` maintains a small fixed-size ring buffer (e.g., last 128 events). If a reconnecting client provides the `Last-Event-ID` HTTP header:

* If the requested ID is still in the ring buffer, replay the missed events in order.
* If the requested ID is older than the earliest event in the buffer, emit a single `system.resync_required` event so the client performs a full REST refresh.

```go
package events

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"sync"
	"sync/atomic"
	"time"
)

const (
	ringBufferSize    = 128
	clientChanBuffer  = 32
	heartbeatInterval = 30 * time.Second
)

// Event represents a single state-invalidation notification.
type Event struct {
	ID      uint64      `json:"id"`
	Type    string      `json:"type"`
	Payload interface{} `json:"payload"`
}

// Hub manages active SSE client subscriptions and broadcasts events non-blockingly.
type Hub struct {
	mu      sync.RWMutex
	clients map[chan Event]struct{}
	ring    []Event
	seq     uint64
}

func NewHub() *Hub {
	return &Hub{
		clients: make(map[chan Event]struct{}),
		ring:    make([]Event, 0, ringBufferSize),
	}
}

// Publish broadcasts a lightweight notification event to all connected clients.
// It is thread-safe and guaranteed never to block the calling producer goroutine.
func (h *Hub) Publish(eventType string, payload interface{}) Event {
	id := atomic.AddUint64(&h.seq, 1)
	ev := Event{
		ID:      id,
		Type:    eventType,
		Payload: payload,
	}

	h.mu.Lock()
	if len(h.ring) >= ringBufferSize {
		h.ring = append(h.ring[1:], ev)
	} else {
		h.ring = append(h.ring, ev)
	}

	for clientChan := range h.clients {
		select {
		case clientChan <- ev:
		default:
			// Drop event for slow client rather than blocking daemon workers
		}
	}
	h.mu.Unlock()

	return ev
}

// ServeHTTP handles GET /api/events SSE connections.
func (h *Hub) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

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
	missedEvents, resyncNeeded := h.collectMissedEventsLocked(r.Header.Get("Last-Event-ID"))
	h.mu.Unlock()

	defer func() {
		h.mu.Lock()
		delete(h.clients, clientChan)
		close(clientChan)
		h.mu.Unlock()
	}()

	// Initial connection comment
	fmt.Fprintf(w, ": connected\n\n")
	flusher.Flush()

	// Handle Last-Event-ID replay or resync trigger
	if resyncNeeded {
		writeSSEFrame(w, Event{
			ID:      atomic.LoadUint64(&h.seq),
			Type:    "system.resync_required",
			Payload: map[string]interface{}{"reason": "missed_events"},
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

	ticker := time.NewTicker(heartbeatInterval)
	defer ticker.Stop()

	ctx := r.Context()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			fmt.Fprintf(w, ": heartbeat\n\n")
			flusher.Flush()
		case ev := <-clientChan:
			writeSSEFrame(w, ev)
			flusher.Flush()
		}
	}
}

func (h *Hub) collectMissedEventsLocked(lastIDStr string) ([]Event, bool) {
	if lastIDStr == "" {
		return nil, false
	}
	lastID, err := strconv.ParseUint(lastIDStr, 10, 64)
	if err != nil || len(h.ring) == 0 {
		return nil, false
	}
	oldestID := h.ring[0].ID
	if lastID < oldestID-1 {
		return nil, true // Overflow: client missed more events than ring buffer holds
	}
	var missed []Event
	for _, ev := range h.ring {
		if ev.ID > lastID {
			missed = append(missed, ev)
		}
	}
	return missed, false
}

func writeSSEFrame(w http.ResponseWriter, ev Event) {
	data, err := json.Marshal(ev.Payload)
	if err != nil {
		return
	}
	fmt.Fprintf(w, "id: %d\nevent: %s\ndata: %s\n\n", ev.ID, ev.Type, data)
}

```

### 4.3 Hook Points in UndineD Backend

Inject `*events.Hub` into the daemon's core services and invoke `hub.Publish(...)` at the following completion points:

1. **IMAP Poller (`internal/imap`)**:
* After syncing a folder and indexing new messages into SQLite/Maildir (only when `newCount > 0`), call:
`hub.Publish("mail.sync_completed", map[string]any{"folder": folder, "new_count": newCount, "timestamp": time.Now().Unix()})`




2. **`b4` Hydration Bridge (`internal/b4` or API handler)**:
* After `b4 mbox` imports messages into Maildir and rebuilds the thread tree, call:
`hub.Publish("thread.hydrated", map[string]any{"root_message_id": msgID, "imported_count": count, "timestamp": time.Now().Unix()})`




3. **Review Comments & MCP Server (`internal/api` & `internal/mcp`)**:
* Note: If MCP runs in `--mcp-stdio` mode as a separate OS process, STDIO processes cannot directly access the HTTP daemon's in-memory `Hub` unless they either (a) forward the comment creation via the local HTTP REST API (`POST /api/reviews/comments`) or (b) the HTTP daemon watches the SQLite `PRAGMA data_version` / WAL file.


* **Requirement**: Ensure MCP tools that mutate state (`post_review_comment`, `create_review_draft`, `import_patch_series_b4`) either proxy through the running local daemon's HTTP REST API if reachable, or add a lightweight SQLite `PRAGMA data_version` check (every 2 seconds) in the daemon to emit invalidation events when external CLI/MCP processes modify `undine.db`.





---

## 5. Frontend Implementation Specification (`frontend/`)

### 5.1 Event Stream Manager & Debouncer

In the Web UI (`frontend/`), replace any `setInterval` polling with an `EventSource` subscriber that debounces consecutive notifications into single REST API calls.

```javascript
// Debounce utility to coalesce burst notifications (e.g., multiple emails or comments)
function debounce(fn, delayMs = 300) {
  let timer = null;
  return (...args) => {
    clearTimeout(timer);
    timer = setTimeout(() => fn(...args), delayMs);
  };
}

export class NotificationChannel {
  constructor(appStore) {
    this.store = appStore;
    this.es = null;

    this.debouncedSyncFolders = debounce(async (targetFolder) => {
      await this.store.fetchFolders();
      if (!targetFolder || this.store.currentFolder === targetFolder) {
        await this.store.fetchMessages(this.store.currentFolder);
      }
    }, 300);

    this.debouncedSyncThread = debounce(async (rootMsgId) => {
      await this.store.fetchFolders();
      if (this.store.currentThreadId === rootMsgId || this.store.currentMessageId) {
        await this.store.refreshCurrentThread();
      }
    }, 300);

    this.debouncedSyncComments = debounce(async (msgId) => {
      if (this.store.currentMessageId === msgId) {
        await this.store.fetchReviewComments(msgId);
      }
    }, 200);
  }

  connect() {
    if (this.es) {
      this.es.close();
    }

    // Cookies (HttpOnly session) are sent automatically by browser EventSource
    this.es = new EventSource("/api/events");

    this.es.addEventListener("mail.sync_completed", (e) => {
      const data = JSON.parse(e.data);
      this.debouncedSyncFolders(data.folder);
    });

    this.es.addEventListener("message.updated", (e) => {
      const data = JSON.parse(e.data);
      this.debouncedSyncFolders(data.folder);
    });

    this.es.addEventListener("thread.hydrated", (e) => {
      const data = JSON.parse(e.data);
      this.debouncedSyncThread(data.root_message_id);
    });

    this.es.addEventListener("review.comment_added", (e) => {
      const data = JSON.parse(e.data);
      this.debouncedSyncComments(data.message_id);
    });

    this.es.addEventListener("system.resync_required", () => {
      this.debouncedSyncFolders(this.store.currentFolder);
      if (this.store.currentMessageId) {
        this.debouncedSyncComments(this.store.currentMessageId);
      }
    });

    this.es.onerror = () => {
      console.warn("[UndineD SSE] Connection interrupted; browser will auto-reconnect.");
    };
  }

  disconnect() {
    if (this.es) {
      this.es.close();
      this.es = null;
    }
  }
}

```

---

## 6. Step-by-Step Implementation Tasks for Coding Agent

Execute the following steps sequentially and verify with `make test` after each step:

1. **Step 1: Implement `internal/events/hub.go` & Unit Tests**
* Create `internal/events/hub.go` implementing `Hub`, `Publish`, `ServeHTTP`, heartbeat ticker, and `Last-Event-ID` ring-buffer replay.
* Create `internal/events/hub_test.go` testing:
* Single and multiple concurrent client subscriptions.
* Non-blocking publish when a client channel is full.
* `Last-Event-ID` replay for missed events and `system.resync_required` when the ring buffer overflows.
* Proper cleanup when client `context.Context` is canceled.




2. **Step 2: Wire `GET /api/events` into API Router**
* Instantiate `events.NewHub()` in `cmd/undined` (or the server initialization struct).


* Register `GET /api/events` behind the existing authentication middleware in `internal/api`.




3. **Step 3: Connect Producers (IMAP, b4, Review API, MCP)**
* Pass the `*events.Hub` reference to the IMAP poller, `b4` runner, and REST API handlers.


* Emit `mail.sync_completed`, ` message.updated`, `thread.hydrated`, `review.comment_added`, `draft.created`, and `patch.applied` at their respective completion points.


* Add a lightweight SQLite change detector (checking `PRAGMA data_version` or table timestamps) or HTTP loopback notification so mutations made by standalone `--mcp-stdio` processes also trigger SSE events in the main daemon.




4. **Step 4: Integrate SSE Client in `frontend/**`
* Implement `NotificationChannel` in the frontend JavaScript code.


* Connect it on application initialization and remove any legacy polling timers.
* Update `docs/REST_API.md` to document the `GET /api/events` endpoint and event schemas.


