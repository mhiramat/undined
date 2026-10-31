package review

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"undined-go/internal/config"
	"undined-go/internal/constants"
	"undined-go/internal/events"
	"undined-go/internal/storage/database"
	"undined-go/internal/storage/maildir"
)

func setupTestReviewRunner(t *testing.T) (*Runner, *database.Database, *maildir.MaildirStore, string) {
	t.Helper()
	tmpDir, err := os.MkdirTemp("", "undined-review-test-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}

	dbPath := filepath.Join(tmpDir, "test.db")
	db, err := database.NewDatabase(dbPath)
	if err != nil {
		t.Fatalf("Failed to initialize database: %v", err)
	}

	mailPath := filepath.Join(tmpDir, "Mail")
	store := maildir.NewMaildirStore(mailPath)

	inbox, err := db.GetOrCreateFolder("INBOX", true)
	if err != nil {
		t.Fatalf("Failed to create folder: %v", err)
	}

	patch1 := "Subject: [PATCH] trace: fix buffer\r\nMessage-ID: <20261017-test-msg@example.com>\r\nFrom: Alice <alice@example.com>\r\n\r\n--- a/f.c\n+++ b/f.c\n@@ -1 +1 @@\n-old\n+new\n"
	p1, _ := store.AddMessage("INBOX", []byte(patch1), constants.FlagSeen)
	_, _ = db.IndexEmailFile(inbox.ID, p1)

	patch2 := "Subject: [PATCH] trace: cancel test\r\nMessage-ID: <msg-cancel@example.com>\r\nFrom: Bob <bob@example.com>\r\n\r\n--- a/f.c\n+++ b/f.c\n@@ -1 +1 @@\n-old\n+new\n"
	p2, _ := store.AddMessage("INBOX", []byte(patch2), constants.FlagSeen)
	_, _ = db.IndexEmailFile(inbox.ID, p2)

	cfg := config.NewDefaultConfig()
	cfg.Review.Command = "echo 'AI Review Output for' {commit_id}"

	runner := NewRunner(cfg, store, db)
	return runner, db, store, tmpDir
}

func TestReviewRunnerTriggerAndComplete(t *testing.T) {
	runner, db, _, tmpDir := setupTestReviewRunner(t)
	defer os.RemoveAll(tmpDir)
	defer db.Close()

	res, err := runner.TriggerReview("20261017-test-msg@example.com", "abc123456789")
	if err != nil {
		t.Fatalf("TriggerReview failed: %v", err)
	}

	if !res.Success {
		t.Errorf("Expected success, got false")
	}

	// Poll until completed
	deadline := time.Now().Add(5 * time.Second)
	var status StatusResponse
	for time.Now().Before(deadline) {
		status = runner.GetStatus()
		if !status.IsRunning && status.Status == "completed" {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}

	if status.Status != "completed" {
		t.Fatalf("Expected review to complete, got status=%s, err=%s", status.Status, status.Error)
	}

	if !strings.Contains(status.Output, "AI Review Output for abc123456789") {
		t.Errorf("Unexpected output: %s", status.Output)
	}

	// Check AI-reviews mailbox in database
	aiFolder, err := db.GetFolderByName(constants.FolderAIReviews)
	if err != nil || aiFolder == nil {
		t.Fatalf("AI-reviews folder not found in database: %v", err)
	}

	msgs, err := db.ListMessages(&aiFolder.ID, 10, 0)
	if err != nil || len(msgs) == 0 {
		t.Fatalf("Expected review message in AI-reviews folder, got %d msgs, err=%v", len(msgs), err)
	}

	if !strings.Contains(msgs[0].Subject, "AI review:") {
		t.Errorf("Expected AI review: in subject, got %s", msgs[0].Subject)
	}
}

func TestReviewRunnerDuplicateAndCancel(t *testing.T) {
	runner, db, _, tmpDir := setupTestReviewRunner(t)
	defer os.RemoveAll(tmpDir)
	defer db.Close()

	// Use sleep command directly without subshell
	runner.Config.Review.Command = "sleep 2"

	res, err := runner.TriggerReview("msg-cancel@example.com", "commit-cancel-1")
	if err != nil {
		t.Fatalf("TriggerReview failed: %v", err)
	}
	if !res.Success {
		t.Fatalf("Expected TriggerReview success")
	}

	// Immediate duplicate trigger must fail
	_, err = runner.TriggerReview("msg-cancel@example.com", "commit-cancel-1")
	if err == nil {
		t.Fatalf("Expected duplicate trigger to fail")
	}

	// Cancel review
	cancelled := runner.CancelReview("msg-cancel@example.com")
	if !cancelled {
		t.Fatalf("Expected CancelReview to return true")
	}

	// Wait for worker to finish
	time.Sleep(100 * time.Millisecond)
	status := runner.GetStatus()
	if status.Status != "cancelled" {
		t.Errorf("Expected status to be cancelled, got %s", status.Status)
	}
}

func TestReviewRunnerValidation(t *testing.T) {
	runner, db, _, tmpDir := setupTestReviewRunner(t)
	defer os.RemoveAll(tmpDir)
	defer db.Close()

	// 1. Invalid message ID format with shell metacharacters
	invalidMsgIDs := []string{
		"msg$(whoami)@example.com",
		"msg`id`@example.com",
		"msg;rm -rf /@example.com",
		"msg|cat@example.com",
		"msg name@example.com",
	}
	for _, id := range invalidMsgIDs {
		_, err := runner.TriggerReview(id, "abc123456789")
		if err == nil {
			t.Errorf("expected error for malicious message ID %q, got nil", id)
		}
	}

	// 2. Non-existent message ID in database
	_, err := runner.TriggerReview("non-existent-msg@example.com", "abc123456789")
	if err == nil || !strings.Contains(err.Error(), "not found in local mailbox") {
		t.Errorf("expected not found error for non-existent message, got %v", err)
	}

	// 3. Invalid commit ID starting with '-' or containing shell chars
	_, err = runner.TriggerReview("20261017-test-msg@example.com", "-b")
	if err == nil {
		t.Errorf("expected error for invalid commit ID '-b', got nil")
	}
	_, err = runner.TriggerReview("20261017-test-msg@example.com", "commit$(id)")
	if err == nil {
		t.Errorf("expected error for commit with command substitution, got nil")
	}
}

func TestReviewRunnerWrapperScript(t *testing.T) {
	runner, db, _, tmpDir := setupTestReviewRunner(t)
	defer os.RemoveAll(tmpDir)
	defer db.Close()

	// Create a test wrapper script
	scriptPath := filepath.Join(tmpDir, "test-wrapper.sh")
	scriptContent := `#!/bin/sh
echo "Wrapper called with MSGID=$1 COMMIT=$2"
if [ ! -t 0 ]; then
    echo "STDIN: $(cat)"
fi
`
	if err := os.WriteFile(scriptPath, []byte(scriptContent), 0755); err != nil {
		t.Fatalf("failed to write test wrapper script: %v", err)
	}

	runner.Config.Review.WrapperScript = scriptPath

	res, err := runner.TriggerReview("20261017-test-msg@example.com", "abc123456789")
	if err != nil {
		t.Fatalf("TriggerReview with wrapper failed: %v", err)
	}
	if !res.Success {
		t.Errorf("expected success")
	}

	deadline := time.Now().Add(5 * time.Second)
	var status StatusResponse
	for time.Now().Before(deadline) {
		status = runner.GetStatus()
		if !status.IsRunning && status.Status == "completed" {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}

	if status.Status != "completed" {
		t.Fatalf("expected completed, got status=%s, err=%s", status.Status, status.Error)
	}

	if !strings.Contains(status.Output, "Wrapper called with MSGID=20261017-test-msg@example.com COMMIT=abc123456789") {
		t.Errorf("expected wrapper output, got:\n%s", status.Output)
	}
	if !strings.Contains(status.Output, "STDIN:") {
		t.Errorf("expected prompt via STDIN, got:\n%s", status.Output)
	}
}

func TestReviewRunnerStatusUpdatedSSEEvent(t *testing.T) {
	runner, db, _, tmpDir := setupTestReviewRunner(t)
	defer os.RemoveAll(tmpDir)
	defer db.Close()

	hub := events.NewHub()
	runner.SetEvents(hub)

	res, err := runner.TriggerReview("20261017-test-msg@example.com", "abc123456789")
	if err != nil {
		t.Fatalf("TriggerReview failed: %v", err)
	}
	if !res.Success {
		t.Fatalf("expected success")
	}

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		st := runner.GetStatus()
		if !st.IsRunning && st.Status == "completed" {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}

	// Verify events were published to the hub ring buffer
	if hub.RingSize() == 0 {
		t.Fatalf("expected SSE events to be published to Hub, got 0")
	}

	eventsList := hub.EventsSince(0)
	var foundStatusUpdated bool
	var foundCompleted bool
	for _, ev := range eventsList {
		if ev.Type == "review.status_updated" {
			foundStatusUpdated = true
			if st, ok := ev.Payload.(StatusResponse); ok {
				if st.Status == "completed" {
					foundCompleted = true
				}
			}
		}
	}

	if !foundStatusUpdated {
		t.Errorf("expected at least one 'review.status_updated' event")
	}
	if !foundCompleted {
		t.Errorf("expected a 'review.status_updated' event with Status=completed")
	}
}
