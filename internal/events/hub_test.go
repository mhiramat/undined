package events

import (
	"bufio"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestHub_PublishAndBroadcast(t *testing.T) {
	hub := NewHub()
	hub.SetHeartbeatInterval(10 * time.Second)

	server := httptest.NewServer(http.HandlerFunc(hub.ServeHTTP))
	defer server.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, "GET", server.URL, nil)
	if err != nil {
		t.Fatalf("failed to create request: %v", err)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("failed to connect to SSE server: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected status 200, got %d", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "text/event-stream" {
		t.Fatalf("expected Content-Type text/event-stream, got %s", ct)
	}

	reader := bufio.NewReader(resp.Body)

	// Read initial ": connected" line
	line, err := reader.ReadString('\n')
	if err != nil {
		t.Fatalf("failed to read connected line: %v", err)
	}
	if !strings.HasPrefix(line, ": connected") {
		t.Fatalf("expected ': connected', got %q", line)
	}
	// Consume empty line after comment
	_, _ = reader.ReadString('\n')

	// Publish test event
	testPayload := map[string]any{"folder": "INBOX", "new_count": 5}
	hub.Publish("mail.sync_completed", testPayload)

	// Read event frame
	var idLine, eventLine, dataLine string
	for {
		l, err := reader.ReadString('\n')
		if err != nil {
			t.Fatalf("failed to read event line: %v", err)
		}
		l = strings.TrimSpace(l)
		if strings.HasPrefix(l, "id: ") {
			idLine = l
		} else if strings.HasPrefix(l, "event: ") {
			eventLine = l
		} else if strings.HasPrefix(l, "data: ") {
			dataLine = l
			break
		}
	}

	if idLine != "id: 1" {
		t.Errorf("expected 'id: 1', got %q", idLine)
	}
	if eventLine != "event: mail.sync_completed" {
		t.Errorf("expected 'event: mail.sync_completed', got %q", eventLine)
	}
	var received map[string]any
	if err := json.Unmarshal([]byte(strings.TrimPrefix(dataLine, "data: ")), &received); err != nil {
		t.Fatalf("failed to parse event data: %v", err)
	}
	if received["folder"] != "INBOX" || received["new_count"] != float64(5) {
		t.Errorf("unexpected event payload: %v", received)
	}
}

func TestHub_NonBlockingChannelOverflow(t *testing.T) {
	hub := NewHub()

	// Fill ring buffer and clients
	ch := make(chan Event, 2)
	hub.mu.Lock()
	hub.clients[ch] = struct{}{}
	hub.mu.Unlock()

	// Publishing 10 events should not block even though channel only buffers 2
	done := make(chan bool)
	go func() {
		for i := 0; i < 10; i++ {
			hub.Publish("test.event", map[string]int{"i": i})
		}
		done <- true
	}()

	select {
	case <-done:
		// Succeeded without hanging
	case <-time.After(1 * time.Second):
		t.Fatal("Publish blocked on full client channel!")
	}

	hub.mu.Lock()
	delete(hub.clients, ch)
	hub.mu.Unlock()
}

func TestHub_LastEventID_Replay(t *testing.T) {
	hub := NewHub()

	// Publish 10 events (IDs 1 to 10)
	for i := 1; i <= 10; i++ {
		hub.Publish("item.created", map[string]int{"num": i})
	}

	server := httptest.NewServer(http.HandlerFunc(hub.ServeHTTP))
	defer server.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Request with Last-Event-ID: 7 (should replay 8, 9, 10)
	req, err := http.NewRequestWithContext(ctx, "GET", server.URL, nil)
	if err != nil {
		t.Fatalf("failed to create request: %v", err)
	}
	req.Header.Set("Last-Event-ID", "7")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("failed to connect to SSE server: %v", err)
	}
	defer resp.Body.Close()

	reader := bufio.NewReader(resp.Body)

	// Consume ": connected"
	_, _ = reader.ReadString('\n')
	_, _ = reader.ReadString('\n')

	// Expect events 8, 9, 10 in order
	expectedIDs := []string{"id: 8", "id: 9", "id: 10"}
	for _, expectedID := range expectedIDs {
		var idLine string
		for {
			l, err := reader.ReadString('\n')
			if err != nil {
				t.Fatalf("error reading replay line: %v", err)
			}
			l = strings.TrimSpace(l)
			if strings.HasPrefix(l, "id: ") {
				idLine = l
				break
			}
		}
		if idLine != expectedID {
			t.Errorf("expected %s, got %s", expectedID, idLine)
		}
	}
}

func TestHub_LastEventID_OverflowResync(t *testing.T) {
	hub := NewHub()

	// Publish 150 events to overflow the 128-event ring buffer
	for i := 1; i <= 150; i++ {
		hub.Publish("bulk.event", map[string]int{"index": i})
	}

	server := httptest.NewServer(http.HandlerFunc(hub.ServeHTTP))
	defer server.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Request with Last-Event-ID: 5 (which has been evicted from the 128-sized ring buffer)
	req, err := http.NewRequestWithContext(ctx, "GET", server.URL, nil)
	if err != nil {
		t.Fatalf("failed to create request: %v", err)
	}
	req.Header.Set("Last-Event-ID", "5")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("failed to connect: %v", err)
	}
	defer resp.Body.Close()

	reader := bufio.NewReader(resp.Body)

	// Consume ": connected"
	_, _ = reader.ReadString('\n')
	_, _ = reader.ReadString('\n')

	// Expect system.resync_required event
	var eventLine, dataLine string
	for {
		l, err := reader.ReadString('\n')
		if err != nil {
			t.Fatalf("error reading overflow event line: %v", err)
		}
		l = strings.TrimSpace(l)
		if strings.HasPrefix(l, "event: ") {
			eventLine = l
		} else if strings.HasPrefix(l, "data: ") {
			dataLine = l
			break
		}
	}

	if eventLine != "event: system.resync_required" {
		t.Errorf("expected 'event: system.resync_required', got %q", eventLine)
	}
	if !strings.Contains(dataLine, "missed_events") {
		t.Errorf("expected data to contain 'missed_events', got %q", dataLine)
	}
}

func TestHub_Heartbeat(t *testing.T) {
	hub := NewHub()
	// Set heartbeat interval very short for test
	hub.SetHeartbeatInterval(25 * time.Millisecond)

	server := httptest.NewServer(http.HandlerFunc(hub.ServeHTTP))
	defer server.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, "GET", server.URL, nil)
	if err != nil {
		t.Fatalf("failed to create request: %v", err)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("failed to connect: %v", err)
	}
	defer resp.Body.Close()

	reader := bufio.NewReader(resp.Body)

	// Consume ": connected"
	_, _ = reader.ReadString('\n')
	_, _ = reader.ReadString('\n')

	// Next should be heartbeat comment
	line, err := reader.ReadString('\n')
	if err != nil {
		t.Fatalf("failed to read heartbeat line: %v", err)
	}
	line = strings.TrimSpace(line)
	if line != ": heartbeat" {
		t.Fatalf("expected ': heartbeat', got %q", line)
	}
}

func TestHub_ConcurrentClientsAndDisconnect(t *testing.T) {
	hub := NewHub()
	server := httptest.NewServer(http.HandlerFunc(hub.ServeHTTP))
	defer server.Close()

	const numClients = 5
	var wg sync.WaitGroup
	cancels := make([]context.CancelFunc, numClients)

	for i := 0; i < numClients; i++ {
		wg.Add(1)
		ctx, cancel := context.WithCancel(context.Background())
		cancels[i] = cancel

		go func(clientNum int, c context.Context) {
			defer wg.Done()
			req, _ := http.NewRequestWithContext(c, "GET", server.URL, nil)
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				return
			}
			defer resp.Body.Close()

			reader := bufio.NewReader(resp.Body)
			_, _ = reader.ReadString('\n')
			// Wait until context canceled
			<-c.Done()
		}(i, ctx)
	}

	// Give clients time to connect
	time.Sleep(50 * time.Millisecond)

	if hub.ClientCount() != numClients {
		t.Errorf("expected %d connected clients, got %d", numClients, hub.ClientCount())
	}

	// Cancel all clients
	for _, c := range cancels {
		c()
	}

	wg.Wait()
	time.Sleep(50 * time.Millisecond)

	if hub.ClientCount() != 0 {
		t.Errorf("expected 0 connected clients after disconnect, got %d", hub.ClientCount())
	}
}
