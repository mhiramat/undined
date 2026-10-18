package imap

import (
	"os"
	"path/filepath"
	"testing"

	"undined-go/internal/config"
	"undined-go/internal/storage/database"
	"undined-go/internal/storage/maildir"
)

func TestPollerSyncStatusAndCancellation(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "undined-imap-test-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	dbPath := filepath.Join(tmpDir, "test.db")
	db, err := database.NewDatabase(dbPath)
	if err != nil {
		t.Fatalf("Failed to init database: %v", err)
	}
	defer db.Close()

	mailPath := filepath.Join(tmpDir, "Mail")
	store := maildir.NewMaildirStore(mailPath)

	cfg := config.NewDefaultConfig()
	poller := NewPoller(cfg, store, db)

	// Test default status
	st := poller.GetSyncStatus(1)
	if st.IsSyncing {
		t.Errorf("Expected IsSyncing=false for unstarted sync")
	}
	if st.Status != "idle" {
		t.Errorf("Expected status 'idle', got '%s'", st.Status)
	}

	// Test active status and cancellation
	poller.mu.Lock()
	poller.syncProgress[1] = &SyncStatus{
		FolderID:  1,
		IsSyncing: true,
		Status:    "syncing",
		Total:     10,
		Synced:    3,
	}
	poller.mu.Unlock()

	if !poller.IsFolderSyncing(1) {
		t.Errorf("Expected IsFolderSyncing=true")
	}

	cancelled := poller.CancelFolderSync(1)
	if !cancelled {
		t.Errorf("Expected CancelFolderSync to return true")
	}

	if !poller.isSyncCancelled(1) {
		t.Errorf("Expected isSyncCancelled=true")
	}
}

func TestPollerInsertHeaderSummaryAndHydration(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "undined-imap-test-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	dbPath := filepath.Join(tmpDir, "test.db")
	db, err := database.NewDatabase(dbPath)
	if err != nil {
		t.Fatalf("Failed to init database: %v", err)
	}
	defer db.Close()

	folder, err := db.GetOrCreateFolder("INBOX", true)
	if err != nil {
		t.Fatalf("Failed to create folder: %v", err)
	}

	headers := []byte("Message-ID: <patch-101@kernel.org>\r\n" +
		"Date: Sat, 17 Oct 2026 12:00:00 +0000\r\n" +
		"From: Kernel Developer <dev@example.com>\r\n" +
		"Subject: [PATCH] ftrace: Fix event ring buffer\r\n" +
		"In-Reply-To: <parent-100@kernel.org>\r\n\r\n")

	msgID, err := db.InsertHeaderSummary(folder.ID, 501, headers, false, "")
	if err != nil {
		t.Fatalf("InsertHeaderSummary failed: %v", err)
	}

	msg, err := db.GetMessageByID(msgID)
	if err != nil {
		t.Fatalf("GetMessageByID failed: %v", err)
	}

	if msg.Subject != "[PATCH] ftrace: Fix event ring buffer" {
		t.Errorf("Unexpected subject: %s", msg.Subject)
	}
	if msg.FilePath != "" {
		t.Errorf("Expected empty file_path before hydration, got %s", msg.FilePath)
	}
	if msg.BodyPlain != "" {
		t.Errorf("Expected empty body_plain before hydration")
	}

	// Now hydrate body
	fakePath := filepath.Join(tmpDir, "fake.eml")
	bodyPlain := "--- a/kernel/trace/trace.c\n+++ b/kernel/trace/trace.c\n@@ -1,3 +1,3 @@\n-old\n+new\n"
	err = db.HydrateMessageBody(msgID, fakePath, bodyPlain, true)
	if err != nil {
		t.Fatalf("HydrateMessageBody failed: %v", err)
	}

	hydrated, err := db.GetMessageByID(msgID)
	if err != nil {
		t.Fatalf("GetMessageByID failed: %v", err)
	}
	if hydrated.FilePath != fakePath {
		t.Errorf("Hydration failed to set file_path: %s", hydrated.FilePath)
	}
	if !hydrated.IsPatch {
		t.Errorf("Expected IsPatch=true after hydration")
	}
	if hydrated.BodyPlain != bodyPlain {
		t.Errorf("Unexpected bodyPlain after hydration")
	}

	// Verify FTS5 search finds the hydrated message content
	results, err := db.Search("ring buffer", 10)
	if err != nil {
		t.Fatalf("Search failed: %v", err)
	}
	if len(results) == 0 {
		t.Errorf("FTS5 search did not find hydrated message")
	}
}
