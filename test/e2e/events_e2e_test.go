package e2e

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"undined-go/internal/storage/database"
)

// TestE2E_ServerSentEvents verifies the full-stack SSE notification lifecycle:
// 1. Client connects to GET /api/events with Bearer authentication and receives connected frame.
// 2. State-changing actions (comment additions, IMAP sync) emit real-time event frames.
// 3. Client reconnection with Last-Event-ID replays missed events in sequence.
// 4. External database modification triggers system.resync_required.
func TestE2E_ServerSentEvents(t *testing.T) {
	env := setupE2EEnv(t)

	// Seed message
	inboxFolder, err := env.DB.GetOrCreateFolder("INBOX", true)
	if err != nil {
		t.Fatalf("failed to get INBOX: %v", err)
	}
	uid := env.IMAPServer.AddMessage("INBOX", sampleKernelPatch1)
	msg, err := env.Poller.FetchAndHydrateMessage("INBOX", inboxFolder.ID, uid, 0)
	if err != nil {
		t.Fatalf("failed to hydrate test message: %v", err)
	}

	// 1. Connect to SSE stream
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sseReq, err := http.NewRequestWithContext(ctx, "GET", env.BaseURL+"/api/events", nil)
	if err != nil {
		t.Fatalf("failed to create SSE request: %v", err)
	}
	sseReq.Header.Set("Authorization", "Bearer "+env.AuthToken)

	sseClient := &http.Client{}
	resp, err := sseClient.Do(sseReq)
	if err != nil {
		t.Fatalf("failed to connect to /api/events: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 from /api/events, got %d", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "text/event-stream" {
		t.Fatalf("expected text/event-stream, got %s", ct)
	}

	reader := bufio.NewReader(resp.Body)

	// Consume ": connected"
	connLine, err := reader.ReadString('\n')
	if err != nil {
		t.Fatalf("failed reading connected line: %v", err)
	}
	if !strings.HasPrefix(connLine, ": connected") {
		t.Fatalf("expected ': connected', got %q", connLine)
	}
	_, _ = reader.ReadString('\n') // newline

	// 2. Post a review comment and verify review.comment_added arrives over SSE
	cmtPayload := `{"file_path": "kernel/trace/trace_kprobe.c", "line_no": 42, "text": "SSE E2E comment", "author": "Kernel Developer"}`
	cmtResp, err := env.Request("POST", fmt.Sprintf("/api/messages/%d/comments", msg.ID), cmtPayload)
	if err != nil {
		t.Fatalf("POST comment failed: %v", err)
	}
	cmtResp.Body.Close()
	if cmtResp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 for comment post, got %d", cmtResp.StatusCode)
	}

	// Read review.comment_added event
	var eventType, dataStr, eventID string
	for {
		l, err := reader.ReadString('\n')
		if err != nil {
			t.Fatalf("failed reading SSE line: %v", err)
		}
		l = strings.TrimSpace(l)
		if strings.HasPrefix(l, "id: ") {
			eventID = strings.TrimPrefix(l, "id: ")
		} else if strings.HasPrefix(l, "event: ") {
			eventType = strings.TrimPrefix(l, "event: ")
		} else if strings.HasPrefix(l, "data: ") {
			dataStr = strings.TrimPrefix(l, "data: ")
			break
		}
	}

	if eventType != "review.comment_added" {
		t.Errorf("expected review.comment_added, got %q", eventType)
	}
	if eventID == "" {
		t.Errorf("expected event ID in frame, got empty")
	}
	var cmtData map[string]any
	if err := json.Unmarshal([]byte(dataStr), &cmtData); err != nil {
		t.Fatalf("failed to decode event data: %v", err)
	}
	if cmtData["author"] != "Kernel Developer" {
		t.Errorf("expected author 'Kernel Developer', got %v", cmtData["author"])
	}

	// 3. Test Last-Event-ID replay with a new connection
	cancel() // close first stream

	ctx2, cancel2 := context.WithCancel(context.Background())
	defer cancel2()

	replayReq, err := http.NewRequestWithContext(ctx2, "GET", env.BaseURL+"/api/events", nil)
	if err != nil {
		t.Fatalf("failed to create replay request: %v", err)
	}
	replayReq.Header.Set("Authorization", "Bearer "+env.AuthToken)
	replayReq.Header.Set("Last-Event-ID", "0") // replay from beginning

	replayResp, err := sseClient.Do(replayReq)
	if err != nil {
		t.Fatalf("failed to connect for replay: %v", err)
	}
	defer replayResp.Body.Close()

	replayReader := bufio.NewReader(replayResp.Body)
	// Consume ": connected"
	_, _ = replayReader.ReadString('\n')
	_, _ = replayReader.ReadString('\n')

	// Expect replayed event
	var replayedType string
	for {
		l, err := replayReader.ReadString('\n')
		if err != nil {
			t.Fatalf("failed reading replayed frame: %v", err)
		}
		l = strings.TrimSpace(l)
		if strings.HasPrefix(l, "event: ") {
			replayedType = strings.TrimPrefix(l, "event: ")
			break
		}
	}
	if replayedType != "review.comment_added" {
		t.Errorf("expected replayed event 'review.comment_added', got %q", replayedType)
	}

	// 4. Test external SQLite commit triggers system.resync_required via DataVersionWatcher
	env.APIServer.StartDataVersionWatcher(ctx2, 50*time.Millisecond)

	// External database modification simulating an out-of-process tool
	extDB, err := database.NewDatabase(env.Config.General.DBPath)
	if err != nil {
		t.Fatalf("failed to open external DB: %v", err)
	}
	defer extDB.Close()
	_, _ = extDB.GetOrCreateFolder("EXTERNAL_SYNC_FOLDER", false)

	// Stream should receive system.resync_required
	receivedResync := false
	timeout := time.After(2 * time.Second)
	for !receivedResync {
		select {
		case <-timeout:
			t.Fatal("timed out waiting for system.resync_required after external DB modification")
		default:
			l, err := replayReader.ReadString('\n')
			if err != nil {
				t.Fatalf("error reading resync event line: %v", err)
			}
			l = strings.TrimSpace(l)
			if l == "event: system.resync_required" {
				receivedResync = true
			}
		}
	}

	// 5. Test draft.created and draft.updated
	draftPayload1 := `{"to": "dev@example.com", "subject": "Test Draft", "body": "Draft body v1"}`
	draftResp1, err := env.Request("POST", "/api/composer/save-draft", draftPayload1)
	if err != nil {
		t.Fatalf("save-draft 1 failed: %v", err)
	}
	defer draftResp1.Body.Close()
	var draftRes1 map[string]any
	_ = json.NewDecoder(draftResp1.Body).Decode(&draftRes1)
	draftID := int64(draftRes1["db_id"].(float64))

	// Expect draft.created event
	receivedDraftCreated := false
	for !receivedDraftCreated {
		select {
		case <-timeout:
			t.Fatal("timed out waiting for draft.created")
		default:
			l, err := replayReader.ReadString('\n')
			if err != nil {
				t.Fatalf("error reading draft.created line: %v", err)
			}
			l = strings.TrimSpace(l)
			if l == "event: draft.created" {
				receivedDraftCreated = true
			}
		}
	}

	// Overwrite draft -> draft.updated
	draftPayload2 := fmt.Sprintf(`{"to": "dev@example.com", "subject": "Test Draft v2", "body": "Draft body v2", "draft_id": %d}`, draftID)
	draftResp2, err := env.Request("POST", "/api/composer/save-draft", draftPayload2)
	if err != nil {
		t.Fatalf("save-draft 2 failed: %v", err)
	}
	draftResp2.Body.Close()

	receivedDraftUpdated := false
	for !receivedDraftUpdated {
		select {
		case <-timeout:
			t.Fatal("timed out waiting for draft.updated")
		default:
			l, err := replayReader.ReadString('\n')
			if err != nil {
				t.Fatalf("error reading draft.updated line: %v", err)
			}
			l = strings.TrimSpace(l)
			if l == "event: draft.updated" {
				receivedDraftUpdated = true
			}
		}
	}

	// 6. Test patch.applied on abort
	abortPayload := fmt.Sprintf(`{"target_git_repo": %q, "branch_name": "test-branch"}`, env.Config.General.KernelGitDir)
	abortResp, err := env.Request("POST", "/api/patch/apply/abort", abortPayload)
	if err != nil {
		t.Fatalf("abort request failed: %v", err)
	}
	abortResp.Body.Close()

	receivedPatchAbort := false
	for !receivedPatchAbort {
		select {
		case <-timeout:
			t.Fatal("timed out waiting for patch.applied on abort")
		default:
			l, err := replayReader.ReadString('\n')
			if err != nil {
				t.Fatalf("error reading patch.applied line: %v", err)
			}
			l = strings.TrimSpace(l)
			if l == "event: patch.applied" {
				receivedPatchAbort = true
			}
		}
	}
}
