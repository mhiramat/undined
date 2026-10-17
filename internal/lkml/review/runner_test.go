package review

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"undined-go/internal/config"
	"undined-go/internal/constants"
	"undined-go/internal/storage/database"
	"undined-go/internal/storage/maildir"
)

func setupTestReviewRunner(t *testing.T) (*Runner, *database.Database, *maildir.MaildirStore, string) {
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

	// Use a sleep command to test active job cancellation
	runner.Config.Review.Command = "sleep 2 && echo done"

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
