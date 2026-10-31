package imap

import (
	"bufio"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
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
	t.Helper()
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

type mockSyncIMAPServer struct {
	listener   net.Listener
	port       int
	mu         sync.Mutex
	searchUIDs []uint32
	fetchMode  string // "ok", "error", "partial", "empty_header"
}

func startMockSyncIMAPServer(t *testing.T, uids []uint32) *mockSyncIMAPServer {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Failed to start mock IMAP server: %v", err)
	}

	s := &mockSyncIMAPServer{
		listener:   l,
		port:       l.Addr().(*net.TCPAddr).Port,
		searchUIDs: uids,
		fetchMode:  "ok",
	}

	go func() {
		for {
			conn, err := l.Accept()
			if err != nil {
				return
			}
			go s.handleConn(conn)
		}
	}()

	return s
}

func (s *mockSyncIMAPServer) handleConn(c net.Conn) {
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
		arg := ""
		if len(parts) > 2 {
			arg = parts[2]
		}

		switch cmd {
		case "CAPABILITY":
			fmt.Fprintf(c, "* CAPABILITY IMAP4rev1\r\n%s OK CAPABILITY completed\r\n", tag)
		case "LOGIN":
			fmt.Fprintf(c, "%s OK [CAPABILITY IMAP4rev1] LOGIN completed\r\n", tag)
		case "SELECT", "EXAMINE":
			fmt.Fprintf(c, "* %d EXISTS\r\n* 0 RECENT\r\n* FLAGS (\\Seen \\Answered)\r\n* OK [UIDVALIDITY 12345678] UIDs valid\r\n%s OK [READ-ONLY] %s completed\r\n", len(s.searchUIDs), tag, cmd)
		case "UID":
			s.handleUID(c, tag, arg)
		case "LOGOUT":
			fmt.Fprintf(c, "* BYE Mock IMAP logging out\r\n%s OK LOGOUT completed\r\n", tag)
			return
		default:
			fmt.Fprintf(c, "%s OK %s completed\r\n", tag, cmd)
		}
	}
}

func (s *mockSyncIMAPServer) handleUID(c net.Conn, tag, arg string) {
	subParts := strings.SplitN(arg, " ", 2)
	subCmd := strings.ToUpper(subParts[0])
	subArg := ""
	if len(subParts) > 1 {
		subArg = subParts[1]
	}

	s.mu.Lock()
	mode := s.fetchMode
	uids := make([]uint32, len(s.searchUIDs))
	copy(uids, s.searchUIDs)
	s.mu.Unlock()

	switch subCmd {
	case "SEARCH":
		var minUID uint32
		if idx := strings.Index(strings.ToUpper(subArg), "UID "); idx >= 0 {
			rangeStr := strings.TrimSpace(subArg[idx+4:])
			if colonIdx := strings.Index(rangeStr, ":"); colonIdx >= 0 {
				if val, err := strconv.ParseUint(rangeStr[:colonIdx], 10, 32); err == nil {
					minUID = uint32(val)
				}
			}
		}

		var matched []string
		for _, u := range uids {
			if u >= minUID {
				matched = append(matched, strconv.FormatUint(uint64(u), 10))
			}
		}
		if len(matched) > 0 {
			fmt.Fprintf(c, "* SEARCH %s\r\n", strings.Join(matched, " "))
		} else {
			fmt.Fprintf(c, "* SEARCH\r\n")
		}
		fmt.Fprintf(c, "%s OK UID SEARCH completed\r\n", tag)

	case "FETCH":
		switch mode {
		case "error":
			fmt.Fprintf(c, "%s NO UID FETCH failed: simulated header fetch error\r\n", tag)
		case "empty_header":
			for i, u := range uids {
				fmt.Fprintf(c, "* %d FETCH (UID %d FLAGS ())\r\n", i+1, u)
			}
			fmt.Fprintf(c, "%s OK UID FETCH completed\r\n", tag)
		case "partial":
			if len(uids) > 0 {
				u := uids[0]
				hdr := fmt.Sprintf("Message-ID: <patch-%d@example.com>\r\n"+
					"Date: Sat, 24 Oct 2026 12:00:00 +0000\r\n"+
					"From: Kernel Developer <dev@example.com>\r\n"+
					"Subject: [PATCH] test patch %d\r\n\r\n", u, u)
				fmt.Fprintf(c, "* 1 FETCH (UID %d FLAGS () BODY[HEADER.FIELDS (Message-ID Date From To Cc Subject In-Reply-To References Review-Commit-ID)] {%d}\r\n%s)\r\n",
					u, len(hdr), hdr)
			}
			fmt.Fprintf(c, "%s NO UID FETCH failed: partial failure on subsequent UIDs\r\n", tag)
		default: // "ok"
			fields := strings.Fields(subArg)
			var targetUIDs []uint32
			if len(fields) > 0 {
				seqStr := fields[0]
				if strings.Contains(seqStr, ":") {
					parts := strings.Split(seqStr, ":")
					minU, _ := strconv.ParseUint(parts[0], 10, 32)
					maxU, _ := strconv.ParseUint(parts[1], 10, 32)
					for _, u := range uids {
						if u >= uint32(minU) && (maxU == 0 || u <= uint32(maxU)) {
							targetUIDs = append(targetUIDs, u)
						}
					}
				} else if single, err := strconv.ParseUint(seqStr, 10, 32); err == nil {
					targetUIDs = append(targetUIDs, uint32(single))
				} else {
					targetUIDs = uids
				}
			} else {
				targetUIDs = uids
			}

			for i, u := range targetUIDs {
				hdr := fmt.Sprintf("Message-ID: <patch-%d@example.com>\r\n"+
					"Date: Sat, 24 Oct 2026 12:00:00 +0000\r\n"+
					"From: Kernel Developer <dev@example.com>\r\n"+
					"Subject: [PATCH] test patch %d\r\n\r\n", u, u)
				fmt.Fprintf(c, "* %d FETCH (UID %d FLAGS () BODY[HEADER.FIELDS (Message-ID Date From To Cc Subject In-Reply-To References Review-Commit-ID)] {%d}\r\n%s)\r\n",
					i+1, u, len(hdr), hdr)
			}
			fmt.Fprintf(c, "%s OK UID FETCH completed\r\n", tag)
		}
	default:
		fmt.Fprintf(c, "%s OK %s completed\r\n", tag, subCmd)
	}
}

func TestSyncFullFolder_HeaderFetchFailureDoesNotAdvanceCheckpoint(t *testing.T) {
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

	folder, err := db.GetOrCreateFolder("INBOX", true)
	if err != nil {
		t.Fatalf("Failed to create folder: %v", err)
	}

	server := startMockSyncIMAPServer(t, []uint32{100, 101, 102})
	defer server.listener.Close()

	cfg := config.NewDefaultConfig()
	cfg.IMAP.Host = "127.0.0.1"
	cfg.IMAP.Port = server.port
	cfg.IMAP.UseSSL = false
	cfg.IMAP.Username = "dev@example.com"
	cfg.IMAP.Password = "secret"
	cfg.IMAP.SyncFolders = []string{"INBOX"}

	poller := NewPoller(cfg, store, db)

	// Step 1: Set fetchMode to "error"
	server.mu.Lock()
	server.fetchMode = "error"
	server.mu.Unlock()

	// Step 2: Trigger SyncFullFolder; it finds 3 messages, but UID FETCH fails
	synced, err := poller.SyncFullFolder(folder.ID, "INBOX")
	if err == nil {
		t.Fatalf("Expected SyncFullFolder to fail on UID FETCH error, but got nil (synced=%d)", synced)
	}

	// Step 3: Verify sync progress reports error, NOT completed
	st := poller.GetSyncStatus(folder.ID)
	if st.Status != "error" {
		t.Errorf("Expected sync status 'error', got '%s'", st.Status)
	}
	if st.IsSyncing {
		t.Errorf("Expected IsSyncing=false after failure")
	}
	if st.Error == "" {
		t.Errorf("Expected non-empty error in sync status")
	}

	// Step 4: Verify sync checkpoint (last_synced_uid) was NOT advanced to 102
	fAfter, err := db.GetFolderByID(folder.ID)
	if err != nil {
		t.Fatalf("GetFolderByID failed: %v", err)
	}
	if fAfter.LastSyncedUID != 0 {
		t.Errorf("Expected last_synced_uid=0 after failed fetch, but got %d", fAfter.LastSyncedUID)
	}

	// Step 5: Verify subsequent automatic polling (SyncFolder) does NOT skip messages
	server.mu.Lock()
	server.fetchMode = "ok"
	server.mu.Unlock()

	clientConn, err := poller.Connect()
	if err != nil {
		t.Fatalf("Connect failed: %v", err)
	}
	defer clientConn.Logout()

	maxUID, err := poller.SyncFolder(clientConn, folder.ID, "INBOX", fAfter.LastSyncedUID)
	if err != nil {
		t.Fatalf("SyncFolder failed: %v", err)
	}
	if maxUID != 102 {
		t.Errorf("Expected SyncFolder maxUID=102, got %d", maxUID)
	}

	// Verify all 3 messages are now in the DB
	uids, err := db.GetFolderIMAPUIDs(folder.ID)
	if err != nil {
		t.Fatalf("GetFolderIMAPUIDs failed: %v", err)
	}
	if len(uids) != 3 {
		t.Errorf("Expected 3 messages synced, got %d (uids: %v)", len(uids), uids)
	}
}

func TestSyncFullFolder_PartialFetchAdvancesOnlyToSafeCheckpoint(t *testing.T) {
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

	folder, err := db.GetOrCreateFolder("INBOX", true)
	if err != nil {
		t.Fatalf("Failed to create folder: %v", err)
	}

	server := startMockSyncIMAPServer(t, []uint32{100, 101, 102})
	defer server.listener.Close()

	cfg := config.NewDefaultConfig()
	cfg.IMAP.Host = "127.0.0.1"
	cfg.IMAP.Port = server.port
	cfg.IMAP.UseSSL = false
	cfg.IMAP.Username = "dev@example.com"
	cfg.IMAP.Password = "secret"
	cfg.IMAP.SyncFolders = []string{"INBOX"}

	poller := NewPoller(cfg, store, db)

	// Step 1: Mode partial returns UID 100, but fails on 101
	server.mu.Lock()
	server.fetchMode = "partial"
	server.mu.Unlock()

	synced, err := poller.SyncFullFolder(folder.ID, "INBOX")
	if err == nil {
		t.Fatalf("Expected SyncFullFolder to fail on partial fetch error, but got nil (synced=%d)", synced)
	}

	st := poller.GetSyncStatus(folder.ID)
	if st.Status != "error" {
		t.Errorf("Expected status 'error', got '%s'", st.Status)
	}

	// Verify checkpoint only advanced to 100, not 102
	fAfter, err := db.GetFolderByID(folder.ID)
	if err != nil {
		t.Fatalf("GetFolderByID failed: %v", err)
	}
	if fAfter.LastSyncedUID != 100 {
		t.Errorf("Expected last_synced_uid=100, got %d", fAfter.LastSyncedUID)
	}

	// Step 2: Now heal server to "ok" and run incremental sync
	server.mu.Lock()
	server.fetchMode = "ok"
	server.mu.Unlock()

	clientConn, err := poller.Connect()
	if err != nil {
		t.Fatalf("Connect failed: %v", err)
	}
	defer clientConn.Logout()

	maxUID, err := poller.SyncFolder(clientConn, folder.ID, "INBOX", fAfter.LastSyncedUID)
	if err != nil {
		t.Fatalf("SyncFolder failed: %v", err)
	}
	if maxUID != 102 {
		t.Errorf("Expected SyncFolder maxUID=102, got %d", maxUID)
	}

	uids, err := db.GetFolderIMAPUIDs(folder.ID)
	if err != nil {
		t.Fatalf("GetFolderIMAPUIDs failed: %v", err)
	}
	if len(uids) != 3 {
		t.Errorf("Expected 3 messages synced in total, got %d (uids: %v)", len(uids), uids)
	}
}

func TestSyncFullFolder_EmptyHeaderFailsAndDoesNotAdvanceCheckpoint(t *testing.T) {
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

	folder, err := db.GetOrCreateFolder("INBOX", true)
	if err != nil {
		t.Fatalf("Failed to create folder: %v", err)
	}

	server := startMockSyncIMAPServer(t, []uint32{100})
	defer server.listener.Close()

	cfg := config.NewDefaultConfig()
	cfg.IMAP.Host = "127.0.0.1"
	cfg.IMAP.Port = server.port
	cfg.IMAP.UseSSL = false
	cfg.IMAP.Username = "dev@example.com"
	cfg.IMAP.Password = "secret"

	poller := NewPoller(cfg, store, db)

	server.mu.Lock()
	server.fetchMode = "empty_header"
	server.mu.Unlock()

	synced, err := poller.SyncFullFolder(folder.ID, "INBOX")
	if err == nil {
		t.Fatalf("Expected SyncFullFolder to fail on empty header body, got nil (synced=%d)", synced)
	}

	st := poller.GetSyncStatus(folder.ID)
	if st.Status != "error" {
		t.Errorf("Expected status 'error', got '%s'", st.Status)
	}

	fAfter, err := db.GetFolderByID(folder.ID)
	if err != nil {
		t.Fatalf("GetFolderByID failed: %v", err)
	}
	if fAfter.LastSyncedUID != 0 {
		t.Errorf("Expected last_synced_uid=0, got %d", fAfter.LastSyncedUID)
	}
}

func TestSyncFolder_HeaderFetchFailureDoesNotAdvanceCheckpoint(t *testing.T) {
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

	folder, err := db.GetOrCreateFolder("INBOX", true)
	if err != nil {
		t.Fatalf("Failed to create folder: %v", err)
	}

	server := startMockSyncIMAPServer(t, []uint32{200, 201})
	defer server.listener.Close()

	cfg := config.NewDefaultConfig()
	cfg.IMAP.Host = "127.0.0.1"
	cfg.IMAP.Port = server.port
	cfg.IMAP.UseSSL = false
	cfg.IMAP.Username = "dev@example.com"
	cfg.IMAP.Password = "secret"
	cfg.IMAP.SyncFolders = []string{"INBOX"}

	poller := NewPoller(cfg, store, db)

	// Step 1: Mode error
	server.mu.Lock()
	server.fetchMode = "error"
	server.mu.Unlock()

	clientConn, err := poller.Connect()
	if err != nil {
		t.Fatalf("Connect failed: %v", err)
	}

	maxUID, err := poller.SyncFolder(clientConn, folder.ID, "INBOX", 0)
	clientConn.Logout()
	if err == nil {
		t.Fatalf("Expected SyncFolder to fail on UID FETCH error, got nil (maxUID=%d)", maxUID)
	}

	fAfter, err := db.GetFolderByID(folder.ID)
	if err != nil {
		t.Fatalf("GetFolderByID failed: %v", err)
	}
	if fAfter.LastSyncedUID != 0 {
		t.Errorf("Expected last_synced_uid=0 after failed fetch, but got %d", fAfter.LastSyncedUID)
	}

	// Step 2: PollOnce error propagation
	if err := poller.PollOnce(); err == nil {
		t.Errorf("Expected PollOnce to return error when server fetch fails")
	}

	// Step 3: Mode ok succeeds and advances
	server.mu.Lock()
	server.fetchMode = "ok"
	server.mu.Unlock()

	clientConn2, err := poller.Connect()
	if err != nil {
		t.Fatalf("Connect failed: %v", err)
	}
	defer clientConn2.Logout()

	maxUID, err = poller.SyncFolder(clientConn2, folder.ID, "INBOX", fAfter.LastSyncedUID)
	if err != nil {
		t.Fatalf("SyncFolder failed on retry: %v", err)
	}
	if maxUID != 201 {
		t.Errorf("Expected maxUID=201, got %d", maxUID)
	}

	fAfter2, err := db.GetFolderByID(folder.ID)
	if err != nil {
		t.Fatalf("GetFolderByID failed: %v", err)
	}
	if fAfter2.LastSyncedUID != 201 {
		t.Errorf("Expected last_synced_uid=201, got %d", fAfter2.LastSyncedUID)
	}
}

func TestSyncFolder_EmptyHeaderStopsAndDoesNotAdvanceCheckpoint(t *testing.T) {
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

	folder, err := db.GetOrCreateFolder("INBOX", true)
	if err != nil {
		t.Fatalf("Failed to create folder: %v", err)
	}

	server := startMockSyncIMAPServer(t, []uint32{200, 201})
	defer server.listener.Close()

	cfg := config.NewDefaultConfig()
	cfg.IMAP.Host = "127.0.0.1"
	cfg.IMAP.Port = server.port
	cfg.IMAP.UseSSL = false
	cfg.IMAP.Username = "dev@example.com"
	cfg.IMAP.Password = "secret"
	cfg.IMAP.SyncFolders = []string{"INBOX"}

	poller := NewPoller(cfg, store, db)

	server.mu.Lock()
	server.fetchMode = "empty_header"
	server.mu.Unlock()

	clientConn, err := poller.Connect()
	if err != nil {
		t.Fatalf("Connect failed: %v", err)
	}
	defer clientConn.Logout()

	maxUID, err := poller.SyncFolder(clientConn, folder.ID, "INBOX", 0)
	if err == nil {
		t.Fatalf("Expected SyncFolder to fail on empty header, got nil (maxUID=%d)", maxUID)
	}

	fAfter, err := db.GetFolderByID(folder.ID)
	if err != nil {
		t.Fatalf("GetFolderByID failed: %v", err)
	}
	if fAfter.LastSyncedUID != 0 {
		t.Errorf("Expected last_synced_uid=0, got %d", fAfter.LastSyncedUID)
	}
}

func TestSyncFullFolder_RemovesPurgedMessagesAndDeletesMaildir(t *testing.T) {
	tmpDir := t.TempDir()

	dbPath := filepath.Join(tmpDir, "test.db")
	db, err := database.NewDatabase(dbPath)
	if err != nil {
		t.Fatalf("Failed to init database: %v", err)
	}
	defer db.Close()

	mailPath := filepath.Join(tmpDir, "Mail")
	store := maildir.NewMaildirStore(mailPath)

	folder, err := db.GetOrCreateFolder("INBOX", true)
	if err != nil {
		t.Fatalf("Failed to create folder: %v", err)
	}

	// Create 3 maildir files
	f1, err := store.AddMessage("INBOX", []byte("Message-ID: <msg1@example.com>\r\nSubject: Msg 1\r\n\r\nBody 1"), "")
	if err != nil {
		t.Fatalf("Failed to add message 1: %v", err)
	}
	f2, err := store.AddMessage("INBOX", []byte("Message-ID: <msg2@example.com>\r\nSubject: Msg 2\r\n\r\nBody 2"), "")
	if err != nil {
		t.Fatalf("Failed to add message 2: %v", err)
	}
	f3, err := store.AddMessage("INBOX", []byte("Message-ID: <msg3@example.com>\r\nSubject: Msg 3\r\n\r\nBody 3"), "")
	if err != nil {
		t.Fatalf("Failed to add message 3: %v", err)
	}

	// Insert into DB with UIDs 101, 102, 103
	_, err = db.DB.Exec(`
		INSERT INTO messages (message_id, folder_id, imap_uid, file_path, subject, from_name, from_addr, date, epoch_timestamp)
		VALUES ('msg1@example.com', ?, 101, ?, 'Msg 1', 'Kernel Developer', 'dev@example.com', 'Sat, 17 Oct 2026 12:00:00 +0000', 100),
		       ('msg2@example.com', ?, 102, ?, 'Msg 2', 'Kernel Developer', 'dev@example.com', 'Sat, 17 Oct 2026 12:01:00 +0000', 101),
		       ('msg3@example.com', ?, 103, ?, 'Msg 3', 'Kernel Developer', 'dev@example.com', 'Sat, 17 Oct 2026 12:02:00 +0000', 102);
	`, folder.ID, f1, folder.ID, f2, folder.ID, f3)
	if err != nil {
		t.Fatalf("Failed to insert test messages: %v", err)
	}

	// Set folder last_synced_uid to 103
	_ = db.UpdateFolderLastSyncedUID(folder.ID, 103)

	// Start mock IMAP server with ONLY UID 102 (101 and 103 were removed on server side)
	server := startMockSyncIMAPServer(t, []uint32{102})
	defer server.listener.Close()

	cfg := config.NewDefaultConfig()
	cfg.IMAP.Host = "127.0.0.1"
	cfg.IMAP.Port = server.port
	cfg.IMAP.UseSSL = false
	cfg.IMAP.Username = "dev@example.com"
	cfg.IMAP.Password = "secret"
	cfg.IMAP.SyncFolders = []string{"INBOX"}

	poller := NewPoller(cfg, store, db)

	synced, err := poller.SyncFullFolder(folder.ID, "INBOX")
	if err != nil {
		t.Fatalf("SyncFullFolder failed: %v", err)
	}
	if synced != 0 {
		t.Errorf("expected 0 newly synced, got %d", synced)
	}

	st := poller.GetSyncStatus(folder.ID)
	if st.Removed != 2 {
		t.Errorf("expected 2 removed messages in sync status, got %d", st.Removed)
	}

	// Verify local DB only contains UID 102
	uids, err := db.GetFolderIMAPUIDs(folder.ID)
	if err != nil {
		t.Fatalf("GetFolderIMAPUIDs failed: %v", err)
	}
	if len(uids) != 1 || uids[0] != 102 {
		t.Errorf("expected only UID 102 remaining, got %v", uids)
	}

	// Verify Maildir files for 101 and 103 were deleted, while 102 remains
	if _, err := os.Stat(f1); !os.IsNotExist(err) {
		t.Errorf("expected file %s to be deleted, but it exists", f1)
	}
	if _, err := os.Stat(f3); !os.IsNotExist(err) {
		t.Errorf("expected file %s to be deleted, but it exists", f3)
	}
	if _, err := os.Stat(f2); err != nil {
		t.Errorf("expected file %s to exist, err: %v", f2, err)
	}
}

func TestSyncFullFolder_AllMessagesRemovedResetsCheckpoint(t *testing.T) {
	tmpDir := t.TempDir()

	dbPath := filepath.Join(tmpDir, "test.db")
	db, err := database.NewDatabase(dbPath)
	if err != nil {
		t.Fatalf("Failed to init database: %v", err)
	}
	defer db.Close()

	mailPath := filepath.Join(tmpDir, "Mail")
	store := maildir.NewMaildirStore(mailPath)

	folder, err := db.GetOrCreateFolder("INBOX", true)
	if err != nil {
		t.Fatalf("Failed to create folder: %v", err)
	}

	f1, err := store.AddMessage("INBOX", []byte("Message-ID: <msg1@example.com>\r\nSubject: Msg 1\r\n\r\nBody 1"), "")
	if err != nil {
		t.Fatalf("Failed to add message 1: %v", err)
	}

	_, err = db.DB.Exec(`
		INSERT INTO messages (message_id, folder_id, imap_uid, file_path, subject, from_name, from_addr, date, epoch_timestamp)
		VALUES ('msg1@example.com', ?, 101, ?, 'Msg 1', 'Kernel Developer', 'dev@example.com', 'Sat, 17 Oct 2026 12:00:00 +0000', 100);
	`, folder.ID, f1)
	if err != nil {
		t.Fatalf("Failed to insert test message: %v", err)
	}
	_ = db.UpdateFolderLastSyncedUID(folder.ID, 101)

	// Server has NO messages
	server := startMockSyncIMAPServer(t, []uint32{})
	defer server.listener.Close()

	cfg := config.NewDefaultConfig()
	cfg.IMAP.Host = "127.0.0.1"
	cfg.IMAP.Port = server.port
	cfg.IMAP.UseSSL = false
	cfg.IMAP.Username = "dev@example.com"
	cfg.IMAP.Password = "secret"
	cfg.IMAP.SyncFolders = []string{"INBOX"}

	poller := NewPoller(cfg, store, db)

	synced, err := poller.SyncFullFolder(folder.ID, "INBOX")
	if err != nil {
		t.Fatalf("SyncFullFolder failed: %v", err)
	}
	if synced != 0 {
		t.Errorf("expected 0 newly synced, got %d", synced)
	}

	st := poller.GetSyncStatus(folder.ID)
	if st.Removed != 1 {
		t.Errorf("expected 1 removed message, got %d", st.Removed)
	}

	fAfter, err := db.GetFolderByID(folder.ID)
	if err != nil {
		t.Fatalf("GetFolderByID failed: %v", err)
	}
	if fAfter.LastSyncedUID != 0 {
		t.Errorf("expected last_synced_uid to be reset to 0, got %d", fAfter.LastSyncedUID)
	}
}
