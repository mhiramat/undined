package imap

import (
	"bufio"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
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

func startMockIMAPServer(t *testing.T) (net.Listener, int, *[]string) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Failed to start mock IMAP server: %v", err)
	}

	var commands []string
	var mu sync.Mutex

	go func() {
		for {
			conn, err := l.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer c.Close()
				fmt.Fprintf(c, "* OK [CAPABILITY IMAP4rev1] Mock IMAP Server Ready\r\n")
				r := bufio.NewReader(c)
				for {
					line, err := r.ReadString('\n')
					if err != nil {
						return
					}
					line = strings.TrimRight(line, "\r\n")
					parts := strings.SplitN(line, " ", 3)
					if len(parts) < 2 {
						continue
					}
					tag := parts[0]
					cmd := strings.ToUpper(parts[1])

					mu.Lock()
					commands = append(commands, line)
					mu.Unlock()

					switch cmd {
					case "CAPABILITY":
						fmt.Fprintf(c, "* CAPABILITY IMAP4rev1\r\n%s OK CAPABILITY completed\r\n", tag)
					case "LOGIN":
						fmt.Fprintf(c, "%s OK [CAPABILITY IMAP4rev1] LOGIN completed\r\n", tag)
					case "SELECT":
						fmt.Fprintf(c, "* 10 EXISTS\r\n* 0 RECENT\r\n* FLAGS (\\Seen \\Answered $cl_1 $cl_2)\r\n* OK [PERMANENTFLAGS (\\Seen \\Answered $cl_1 $cl_2 \\*)] Flags permitted.\r\n* OK [UIDVALIDITY 12345678] UIDs valid\r\n%s OK [READ-WRITE] SELECT completed\r\n", tag)
					case "UID":
						arg := ""
						if len(parts) > 2 {
							arg = parts[2]
						}
						fmt.Fprintf(c, "* 1 FETCH (UID 501 FLAGS (%s))\r\n%s OK UID STORE completed\r\n", arg, tag)
					case "LOGOUT":
						fmt.Fprintf(c, "* BYE Mock IMAP logging out\r\n%s OK LOGOUT completed\r\n", tag)
						return
					default:
						fmt.Fprintf(c, "%s OK %s completed\r\n", tag, cmd)
					}
				}
			}(conn)
		}
	}()

	return l, l.Addr().(*net.TCPAddr).Port, &commands
}

func TestUpdateIMAPMessageFlagsAndCustomTags(t *testing.T) {
	listener, port, commands := startMockIMAPServer(t)
	defer listener.Close()

	cfg := config.NewDefaultConfig()
	cfg.IMAP.Host = "127.0.0.1"
	cfg.IMAP.Port = port
	cfg.IMAP.UseSSL = false
	cfg.IMAP.Username = "dev@example.com"
	cfg.IMAP.Password = "secret"

	poller := NewPoller(cfg, nil, nil)

	// 1. Test MarkIMAPMessageRead (true -> \Seen)
	if err := poller.MarkIMAPMessageRead("INBOX", 501, true); err != nil {
		t.Fatalf("MarkIMAPMessageRead(true) failed: %v", err)
	}

	// 2. Test MarkIMAPMessageRead (false -> -\Seen)
	if err := poller.MarkIMAPMessageRead("INBOX", 501, false); err != nil {
		t.Fatalf("MarkIMAPMessageRead(false) failed: %v", err)
	}

	// 3. Test UpdateIMAPMessageCustomTags ($cl_1 added, $cl_2 removed)
	if err := poller.UpdateIMAPMessageCustomTags("INBOX", 501, []string{"$cl_1"}, []string{"$cl_2"}); err != nil {
		t.Fatalf("UpdateIMAPMessageCustomTags failed: %v", err)
	}

	// 4. Test MarkIMAPMessageReplied (true -> \Answered)
	if err := poller.MarkIMAPMessageReplied("INBOX", 501, true); err != nil {
		t.Fatalf("MarkIMAPMessageReplied(true) failed: %v", err)
	}

	// Verify commands sent to IMAP server
	var seenAdd, seenRemove, tagAdd, tagRemove, answeredAdd bool
	for _, cmd := range *commands {
		if strings.Contains(cmd, "UID STORE 501 +FLAGS.SILENT (\\Seen)") {
			seenAdd = true
		}
		if strings.Contains(cmd, "UID STORE 501 -FLAGS.SILENT (\\Seen)") {
			seenRemove = true
		}
		if strings.Contains(cmd, "UID STORE 501 +FLAGS.SILENT ($cl_1)") {
			tagAdd = true
		}
		if strings.Contains(cmd, "UID STORE 501 -FLAGS.SILENT ($cl_2)") {
			tagRemove = true
		}
		if strings.Contains(cmd, "UID STORE 501 +FLAGS.SILENT (\\Answered)") {
			answeredAdd = true
		}
	}

	if !seenAdd {
		t.Errorf("expected UID STORE 501 +FLAGS.SILENT (\\Seen) command, got: %v", *commands)
	}
	if !seenRemove {
		t.Errorf("expected UID STORE 501 -FLAGS.SILENT (\\Seen) command, got: %v", *commands)
	}
	if !tagAdd {
		t.Errorf("expected UID STORE 501 +FLAGS.SILENT ($cl_1) command, got: %v", *commands)
	}
	if !tagRemove {
		t.Errorf("expected UID STORE 501 -FLAGS.SILENT ($cl_2) command, got: %v", *commands)
	}
	if !answeredAdd {
		t.Errorf("expected UID STORE 501 +FLAGS.SILENT (\\Answered) command, got: %v", *commands)
	}
}
