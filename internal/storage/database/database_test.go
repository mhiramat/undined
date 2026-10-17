package database

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"undined-go/internal/constants"
	"undined-go/internal/storage/maildir"
)

// TestLegacySchemaMigration verifies Rule 2:
// Starts with a legacy database schema lacking new columns and single UNIQUE(message_id) constraint,
// and verifies clean upgrade without error.
func TestLegacySchemaMigration(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "legacy_undined.db")

	// 1. Create a legacy database without imap_uid, flags, review_commit_id, has_notes, is_replied
	// and with single-column UNIQUE(message_id)
	rawDB, err := sql.Open("sqlite3", dbPath)
	if err != nil {
		t.Fatalf("failed to open raw sqlite: %v", err)
	}

	legacyDDL := `
	CREATE TABLE folders (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		name TEXT UNIQUE NOT NULL,
		last_synced_uid INTEGER DEFAULT 0,
		sync_enabled BOOLEAN DEFAULT 1
	);

	CREATE TABLE messages (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		message_id TEXT UNIQUE NOT NULL,
		folder_id INTEGER NOT NULL REFERENCES folders(id),
		file_path TEXT NOT NULL,
		subject TEXT,
		from_name TEXT,
		from_addr TEXT,
		to_addr TEXT,
		cc_addr TEXT,
		date TEXT,
		epoch_timestamp INTEGER,
		in_reply_to TEXT,
		references_header TEXT,
		body_plain TEXT,
		is_patch BOOLEAN DEFAULT 0,
		is_read BOOLEAN DEFAULT 0,
		created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP
	);

	INSERT INTO folders (id, name) VALUES (1, 'INBOX'), (2, 'Outbox');
	INSERT INTO messages (id, message_id, folder_id, file_path, subject, from_addr, body_plain)
	VALUES (1, 'legacy-1@kernel.org', 1, '/tmp/legacy-1.eml', 'Legacy Subject', 'dev@kernel.org', 'Legacy body content');
	`
	if _, err := rawDB.Exec(legacyDDL); err != nil {
		t.Fatalf("failed to initialize legacy schema: %v", err)
	}
	rawDB.Close()

	// 2. Open via NewDatabase, which runs safe migrations
	db, err := NewDatabase(dbPath)
	if err != nil {
		t.Fatalf("NewDatabase failed on legacy database: %v", err)
	}
	defer db.Close()

	// 3. Verify legacy message was migrated
	m, err := db.GetMessageByID(1)
	if err != nil {
		t.Fatalf("failed to fetch migrated message: %v", err)
	}
	if m.MessageID != "legacy-1@kernel.org" {
		t.Errorf("unexpected message_id: %s", m.MessageID)
	}
	if m.Subject != "Legacy Subject" {
		t.Errorf("unexpected subject: %s", m.Subject)
	}

	// 4. Verify new columns exist and can be updated
	if err := db.SetMessageFlags(1, "S"); err != nil {
		t.Errorf("failed to set flags on migrated table: %v", err)
	}
	if err := db.SetMessageReplied(1, true); err != nil {
		t.Errorf("failed to set is_replied on migrated table: %v", err)
	}
	if err := db.SetMessageNotes(1, true); err != nil {
		t.Errorf("failed to set has_notes on migrated table: %v", err)
	}
	if err := db.SetMessageReviewCommit("legacy-1@kernel.org", "abcdef123456"); err != nil {
		t.Errorf("failed to set review commit: %v", err)
	}

	// 5. Verify Outbox migrated to TODO
	outboxFolder, _ := db.GetFolderByName(constants.FolderOutbox)
	if outboxFolder != nil {
		t.Errorf("expected Outbox to be migrated, but still found")
	}
	todoFolder, err := db.GetFolderByName(constants.FolderTODO)
	if err != nil || todoFolder == nil {
		t.Errorf("expected TODO folder to exist after Outbox migration")
	}

	// 6. Verify composite unique constraint allows same message_id in a different folder
	folder2, _ := db.GetOrCreateFolder("b4-imports", false)
	_, err = db.DB.Exec(`
		INSERT INTO messages (message_id, folder_id, file_path, subject, from_addr, body_plain)
		VALUES ('legacy-1@kernel.org', ?, '/tmp/legacy-copy.eml', 'Copy in b4', 'dev@kernel.org', 'Copy');
	`, folder2.ID)
	if err != nil {
		t.Errorf("expected composite unique constraint to allow same message_id in different folder: %v", err)
	}
}

func TestDatabaseIndexingAndSearch(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test_undined.db")

	db, err := NewDatabase(dbPath)
	if err != nil {
		t.Fatalf("failed to create database: %v", err)
	}
	defer db.Close()

	// 1. Create a test email in maildir
	store := maildir.NewMaildirStore(tmpDir)
	raw := `From: "Masami Hiramatsu" <mhiramat@kernel.org>
To: linux-trace-kernel@vger.kernel.org
Subject: [PATCH] ftrace: tracepoint kprobe optimization
Date: Mon, 12 Oct 2026 10:00:00 +0900
Message-ID: <patch-1234@kernel.org>
Content-Type: text/plain; charset=utf-8

This patch optimizes tracepoint handling for kprobes.
It significantly reduces instruction cache misses.
---
diff --git a/kernel/trace/trace_kprobe.c b/kernel/trace/trace_kprobe.c
`
	msgPath, err := store.AddMessage("b4-imports", []byte(raw), constants.FlagSeen)
	if err != nil {
		t.Fatalf("failed to add message to maildir: %v", err)
	}

	folder, err := db.GetFolderByName("b4-imports")
	if err != nil {
		t.Fatalf("failed to get folder: %v", err)
	}

	// 2. Index file
	id, err := db.IndexEmailFile(folder.ID, msgPath)
	if err != nil {
		t.Fatalf("failed to index email file: %v", err)
	}

	msg, err := db.GetMessageByID(id)
	if err != nil {
		t.Fatalf("failed to get message by id: %v", err)
	}
	if msg.Subject != "[PATCH] ftrace: tracepoint kprobe optimization" {
		t.Errorf("unexpected subject: %s", msg.Subject)
	}
	if !msg.IsRead {
		t.Errorf("expected message to be read")
	}
	if !msg.IsPatch {
		t.Errorf("expected message to be detected as patch")
	}

	// 3. FTS5 Search
	results, err := db.Search("tracepoint kprobe", 10)
	if err != nil {
		t.Fatalf("search failed: %v", err)
	}
	if len(results) == 0 {
		t.Fatalf("expected at least 1 search match, got 0")
	}
	if results[0].MessageID != "patch-1234@kernel.org" {
		t.Errorf("unexpected match message_id: %s", results[0].MessageID)
	}
}

func TestPatchComments(t *testing.T) {
	tmpDir := t.TempDir()
	db, err := NewDatabase(filepath.Join(tmpDir, "comments.db"))
	if err != nil {
		t.Fatalf("failed to init db: %v", err)
	}
	defer db.Close()

	// Save comment
	comment := &PatchComment{
		MessageID: "test-patch-1@kernel.org",
		FilePath:  "kernel/trace/ftrace.c",
		LineNo:    42,
		Side:      "new",
		Text:      "Check return code",
		State:     "draft",
		Author:    "user",
	}

	saved, err := db.SavePatchComment(comment)
	if err != nil {
		t.Fatalf("failed to save comment: %v", err)
	}
	if saved.ID == 0 {
		t.Fatalf("expected positive comment ID")
	}

	// Fetch comments
	comments, err := db.GetMessageComments("test-patch-1@kernel.org")
	if err != nil {
		t.Fatalf("failed to fetch comments: %v", err)
	}
	if len(comments) != 1 || comments[0].LineNo != 42 {
		t.Fatalf("unexpected comments: %v", comments)
	}

	// Discard draft comments
	discarded, err := db.DiscardDraftComments("test-patch-1@kernel.org")
	if err != nil {
		t.Fatalf("failed to discard drafts: %v", err)
	}
	if discarded != 1 {
		t.Fatalf("expected 1 discarded draft comment, got %d", discarded)
	}

	commentsAfter, _ := db.GetMessageComments("test-patch-1@kernel.org")
	if len(commentsAfter) != 0 {
		t.Fatalf("expected 0 comments after discard, got %d", len(commentsAfter))
	}
}

func TestThreadingCycleDetection(t *testing.T) {
	m1 := &Message{ID: 1, MessageID: "msg-1@kernel.org", EpochTimestamp: 100}
	m2 := &Message{ID: 2, MessageID: "msg-2@kernel.org", InReplyTo: "<msg-1@kernel.org>", EpochTimestamp: 200}
	m3 := &Message{ID: 3, MessageID: "msg-3@kernel.org", InReplyTo: "<msg-2@kernel.org>", EpochTimestamp: 300}
	// Create cycle: m1 replies to m3
	m1.InReplyTo = "<msg-3@kernel.org>"

	messages := []*Message{m1, m2, m3}
	threaded := ThreadMessages(messages, "date", "desc")
	if len(threaded) != 3 {
		t.Fatalf("expected 3 threaded messages, got %d", len(threaded))
	}
}

func TestThreadMessagesHierarchyAndPrefixes(t *testing.T) {
	root := &Message{ID: 1, MessageID: "root@kernel.org", Subject: "RFC: feature", EpochTimestamp: 100}
	reply1 := &Message{ID: 2, MessageID: "reply1@kernel.org", InReplyTo: "<root@kernel.org>", Subject: "Re: RFC: feature", EpochTimestamp: 200}
	reply2 := &Message{ID: 3, MessageID: "reply2@kernel.org", InReplyTo: "<root@kernel.org>", Subject: "Re: RFC: feature", EpochTimestamp: 300}
	nested := &Message{ID: 4, MessageID: "nested@kernel.org", InReplyTo: "<reply1@kernel.org>", Subject: "Re: Re: RFC: feature", EpochTimestamp: 250}

	msgs := []*Message{root, reply1, reply2, nested}
	threaded := ThreadMessages(msgs, "date", "asc")

	if len(threaded) != 4 {
		t.Fatalf("expected 4 threaded messages, got %d", len(threaded))
	}

	// Root should be first
	if threaded[0].ID != 1 || threaded[0].ThreadDepth != 0 || !threaded[0].HasChildren || threaded[0].ThreadPrefix != "" {
		t.Fatalf("unexpected root node: %+v", threaded[0])
	}

	// reply1 is followed by nested
	if threaded[1].ID != 2 || threaded[1].ThreadDepth != 1 || threaded[1].ThreadPrefix != "├─ " {
		t.Fatalf("unexpected reply1 node: %+v", threaded[1])
	}

	if threaded[2].ID != 4 || threaded[2].ThreadDepth != 2 || threaded[2].ThreadPrefix != "│  └─ " {
		t.Fatalf("unexpected nested node: %+v", threaded[2])
	}

	// reply2 is last child
	if threaded[3].ID != 3 || threaded[3].ThreadDepth != 1 || threaded[3].ThreadPrefix != "└─ " {
		t.Fatalf("unexpected reply2 node: %+v", threaded[3])
	}
}

func TestListMessagesWithOptions(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test_options.db")
	db, err := NewDatabase(dbPath)
	if err != nil {
		t.Fatalf("failed to init db: %v", err)
	}
	defer db.Close()

	folder, err := db.GetOrCreateFolder("INBOX", true)
	if err != nil || folder == nil {
		t.Fatalf("failed to get or create INBOX folder: %v", err)
	}

	// Insert 3 messages with different timestamps and subjects
	m1ID, _ := db.InsertHeaderSummary(folder.ID, 1, []byte("Message-ID: <m1@example.com>\r\nSubject: Banana\r\nDate: Mon, 12 Oct 2026 10:00:00 +0000\r\n\r\n"), true, "")
	m2ID, _ := db.InsertHeaderSummary(folder.ID, 2, []byte("Message-ID: <m2@example.com>\r\nIn-Reply-To: <m1@example.com>\r\nSubject: Re: Banana\r\nDate: Mon, 12 Oct 2026 11:00:00 +0000\r\n\r\n"), true, "")
	m3ID, _ := db.InsertHeaderSummary(folder.ID, 3, []byte("Message-ID: <m3@example.com>\r\nSubject: Apple\r\nDate: Mon, 12 Oct 2026 09:00:00 +0000\r\n\r\n"), false, "$cl_1")

	if m1ID == 0 || m2ID == 0 || m3ID == 0 {
		t.Fatalf("failed inserting headers")
	}

	// Test 1: Flat list sorted by subject asc
	flat, err := db.ListMessagesWithOptions(&folder.ID, 0, 0, "subject", "asc", false)
	if err != nil {
		t.Fatalf("ListMessagesWithOptions flat failed: %v", err)
	}
	if len(flat) != 3 {
		t.Fatalf("expected 3 messages, got %d", len(flat))
	}
	if flat[0].Subject != "Apple" {
		t.Fatalf("expected Apple first in subject asc sort, got %s", flat[0].Subject)
	}

	// Test 2: Threaded list
	th, err := db.ListMessagesWithOptions(&folder.ID, 0, 0, "date", "desc", true)
	if err != nil {
		t.Fatalf("ListMessagesWithOptions threaded failed: %v", err)
	}
	if len(th) != 3 {
		t.Fatalf("expected 3 threaded messages, got %d", len(th))
	}
	// Banana thread had reply at 11:00, while Apple was 09:00. So Banana thread should be first in date desc.
	if th[0].Subject != "Banana" || !th[0].HasChildren {
		t.Fatalf("expected Banana thread with children first, got %s (has_children: %v)", th[0].Subject, th[0].HasChildren)
	}
	if th[1].Subject != "Re: Banana" || th[1].ThreadDepth != 1 {
		t.Fatalf("expected Re: Banana as child, got %+v", th[1])
	}
}

func TestFindReviewedMessage(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test_review_link.db")
	db, err := NewDatabase(dbPath)
	if err != nil {
		t.Fatalf("failed to init db: %v", err)
	}
	defer db.Close()

	// 1. Create a patch message in b4-imports
	b4Folder, err := db.GetOrCreateFolder("b4-imports", true)
	if err != nil {
		t.Fatalf("failed creating b4 folder: %v", err)
	}

	origID, err := db.InsertHeaderSummary(
		b4Folder.ID,
		101,
		[]byte("Message-ID: <patch-123@kernel.org>\r\nSubject: [PATCH] trace: fix race in ring buffer\r\nFrom: Dev <dev@kernel.org>\r\nDate: Sat, 17 Oct 2026 12:00:00 +0000\r\n\r\n"),
		true,
		"",
	)
	if err != nil {
		t.Fatalf("failed inserting patch message: %v", err)
	}

	// Case 1: InReplyTo by MessageID
	aiRev1 := &Message{
		Subject:   "AI review: [PATCH] trace: fix race in ring buffer",
		InReplyTo: "<patch-123@kernel.org>",
	}
	rev1, err := db.FindReviewedMessage(aiRev1)
	if err != nil || rev1 == nil {
		t.Fatalf("Case 1 (InReplyTo msgid) failed: %v", err)
	}
	if rev1.ID != origID || rev1.FolderName != "b4-imports" || rev1.Subject != "[PATCH] trace: fix race in ring buffer" {
		t.Errorf("Case 1 unexpected result: %+v", rev1)
	}

	// Case 2: InReplyTo by id-<num>
	aiRev2 := &Message{
		Subject:   "AI review: [PATCH] trace: fix race in ring buffer",
		InReplyTo: fmt.Sprintf("<id-%d>", origID),
	}
	rev2, err := db.FindReviewedMessage(aiRev2)
	if err != nil || rev2 == nil {
		t.Fatalf("Case 2 (InReplyTo id-num) failed: %v", err)
	}
	if rev2.ID != origID {
		t.Errorf("Case 2 unexpected id: %d, expected %d", rev2.ID, origID)
	}

	// Case 3: Header fallback via raw file
	aiRevFile := filepath.Join(tmpDir, "ai_rev_msg.eml")
	fileContent := fmt.Sprintf("From: AI review\r\nSubject: AI review (failed): [PATCH] trace: fix race in ring buffer\r\nX-Reviewed-Message-DB-ID: %d\r\n\r\nQueued\r\n", origID)
	_ = os.WriteFile(aiRevFile, []byte(fileContent), 0644)

	aiRev3 := &Message{
		Subject:  "AI review (failed): [PATCH] trace: fix race in ring buffer",
		FilePath: aiRevFile,
	}
	rev3, err := db.FindReviewedMessage(aiRev3)
	if err != nil || rev3 == nil {
		t.Fatalf("Case 3 (X-Reviewed header) failed: %v", err)
	}
	if rev3.ID != origID {
		t.Errorf("Case 3 unexpected id: %d", rev3.ID)
	}

	// Case 4: Subject prefix fallback
	aiRev4 := &Message{
		Subject: "AI review: [PATCH] trace: fix race in ring buffer",
	}
	rev4, err := db.FindReviewedMessage(aiRev4)
	if err != nil || rev4 == nil {
		t.Fatalf("Case 4 (subject fallback) failed: %v", err)
	}
	if rev4.ID != origID {
		t.Errorf("Case 4 unexpected id: %d", rev4.ID)
	}
}

