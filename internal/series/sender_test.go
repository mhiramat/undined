package series

import (
	"bufio"
	"fmt"
	"net"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"undined-go/internal/config"
	"undined-go/internal/constants"
	"undined-go/internal/events"
	"undined-go/internal/smtp"
	"undined-go/internal/storage/database"
	"undined-go/internal/storage/maildir"
)

type capturedSMTPMsg struct {
	Recipients []string
	Data       string
}

type mockSMTPServer struct {
	ln           net.Listener
	mu           sync.Mutex
	messages     []capturedSMTPMsg
	failOnNumber int // 1-based message count to fail with 550; 0 disables failure
	attemptCount int
}

func newMockSMTPServer(t *testing.T) *mockSMTPServer {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to listen for mock SMTP: %v", err)
	}
	s := &mockSMTPServer{ln: ln}
	go s.serve()
	t.Cleanup(func() { _ = ln.Close() })
	return s
}

func (s *mockSMTPServer) addr() (string, int) {
	a := s.ln.Addr().(*net.TCPAddr)
	return a.IP.String(), a.Port
}

func (s *mockSMTPServer) setFailOnNumber(n int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.failOnNumber = n
}

func (s *mockSMTPServer) getMessages() []capturedSMTPMsg {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]capturedSMTPMsg, len(s.messages))
	copy(out, s.messages)
	return out
}

func (s *mockSMTPServer) serve() {
	for {
		conn, err := s.ln.Accept()
		if err != nil {
			return
		}
		go s.handleConn(conn)
	}
}

func (s *mockSMTPServer) handleConn(conn net.Conn) {
	defer conn.Close()
	r := bufio.NewReader(conn)
	w := bufio.NewWriter(conn)
	writeLine := func(line string) {
		_, _ = w.WriteString(line + "\r\n")
		_ = w.Flush()
	}

	writeLine("220 mock-smtp.local ESMTP")
	var rcpts []string
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			return
		}
		trimmed := strings.TrimRight(line, "\r\n")
		upper := strings.ToUpper(trimmed)
		switch {
		case strings.HasPrefix(upper, "EHLO"), strings.HasPrefix(upper, "HELO"):
			writeLine("250-mock-smtp.local")
			writeLine("250 8BITMIME")
		case strings.HasPrefix(upper, "MAIL FROM:"):
			writeLine("250 OK")
		case strings.HasPrefix(upper, "RCPT TO:"):
			rcpts = append(rcpts, strings.TrimSpace(trimmed[len("RCPT TO:"):]))
			writeLine("250 OK")
		case upper == "DATA":
			s.mu.Lock()
			s.attemptCount++
			shouldFail := s.failOnNumber > 0 && s.attemptCount == s.failOnNumber
			s.mu.Unlock()
			if shouldFail {
				writeLine("550 Simulated SMTP failure")
				continue
			}
			writeLine("354 End data with <CR><LF>.<CR><LF>")
			var body strings.Builder
			for {
				dataLine, dErr := r.ReadString('\n')
				if dErr != nil {
					return
				}
				if strings.TrimRight(dataLine, "\r\n") == "." {
					break
				}
				body.WriteString(dataLine)
			}
			s.mu.Lock()
			s.messages = append(s.messages, capturedSMTPMsg{
				Recipients: append([]string(nil), rcpts...),
				Data:       body.String(),
			})
			s.mu.Unlock()
			writeLine("250 OK")
		case upper == "QUIT":
			writeLine("221 Bye")
			return
		default:
			writeLine("250 OK")
		}
	}
}

func stageSampleSeriesForTest(t *testing.T, mgr *Manager, count int) string {
	t.Helper()
	draftFolder, err := mgr.DB.GetOrCreateFolder(constants.FolderDrafts, false)
	if err != nil {
		t.Fatalf("GetOrCreateFolder failed: %v", err)
	}
	seriesID := "ser_send_test"
	now := time.Now().UTC()
	items := make([]*database.PatchSeriesItem, 0, count)
	rootMsgID := smtp.GenerateGitSendEmailMessageID(now, 12345, 1, "dev@kernel.org")

	for i := 0; i < count; i++ {
		msgID := smtp.GenerateGitSendEmailMessageID(now, 12345, i+1, "dev@kernel.org")
		subj := fmt.Sprintf("[PATCH %d/%d] fprobe: step %d", i, count-1, i)
		body := fmt.Sprintf("Body for patch %d.\n\nSigned-off-by: Kernel Developer <dev@kernel.org>\n---\n kernel/trace/fprobe.c | 2 +-\n\ndiff --git a/kernel/trace/fprobe.c b/kernel/trace/fprobe.c\n--- a/kernel/trace/fprobe.c\n+++ b/kernel/trace/fprobe.c\n@@ -1,2 +1,2 @@\n-\told_%d();\n+\tnew_%d();\n", i, i, i)
		if i == 0 {
			body = "Cover letter overview.\n"
		}
		inReply := ""
		refs := ""
		if i > 0 {
			inReply = rootMsgID
			refs = rootMsgID
		}
		raw, _ := mgr.SMTP.BuildPlainTextMessage(smtp.PlainTextMessageOptions{
			To:         "linux-kernel@vger.kernel.org",
			Cc:         "linux-trace-kernel@vger.kernel.org",
			Subject:    subj,
			Body:       body,
			InReplyTo:  inReply,
			References: refs,
			MessageID:  msgID,
			ExtraHeaders: []smtp.HeaderField{
				{Name: "X-Undine-Series-ID", Value: seriesID},
				{Name: "X-Undine-Series-Index", Value: strconv.Itoa(i)},
				{Name: "X-Undine-Series-Total", Value: strconv.Itoa(count)},
			},
		})
		savedPath, err := mgr.Maildir.AddMessage(constants.FolderDrafts, []byte(raw), constants.FlagDraft)
		if err != nil {
			t.Fatalf("AddMessage failed: %v", err)
		}
		dbID, err := mgr.DB.IndexEmailFile(draftFolder.ID, savedPath)
		if err != nil {
			t.Fatalf("IndexEmailFile failed: %v", err)
		}
		items = append(items, &database.PatchSeriesItem{
			SeriesID:      seriesID,
			SeqIndex:      i,
			IsCoverLetter: i == 0,
			DraftMsgID:    strconv.FormatInt(dbID, 10),
			RFCMessageID:  msgID,
			Subject:       subj,
			Status:        database.SeriesItemStatusDraft,
		})
	}

	series := &database.PatchSeriesDraft{
		SeriesID:     seriesID,
		SeriesKey:    "topic/test-send:v1",
		Branch:       "topic/test-send",
		Version:      1,
		ToAddrs:      []string{"linux-kernel@vger.kernel.org"},
		CcAddrs:      []string{"linux-trace-kernel@vger.kernel.org"},
		Status:       database.SeriesStatusDraft,
		SendDelaySec: 1,
	}
	if err := mgr.DB.CreatePatchSeriesDraft(series, items); err != nil {
		t.Fatalf("CreatePatchSeriesDraft failed: %v", err)
	}
	return seriesID
}

func TestSequentialSendFailureAndDuplicateFreeResume(t *testing.T) {
	tmpDir := t.TempDir()
	db, err := database.NewDatabase(filepath.Join(tmpDir, "test.db"))
	if err != nil {
		t.Fatalf("NewDatabase failed: %v", err)
	}
	defer db.Close()

	store := maildir.NewMaildirStore(filepath.Join(tmpDir, "maildir"))
	mockSMTP := newMockSMTPServer(t)
	host, port := mockSMTP.addr()

	cfg := config.NewDefaultConfig()
	cfg.SMTP.Host = host
	cfg.SMTP.Port = port
	cfg.SMTP.UseSSL = false
	cfg.SMTP.UseSTARTTLS = false
	cfg.SMTP.FromName = "Kernel Developer"
	cfg.SMTP.FromEmail = "dev@kernel.org"

	smtpClient := smtp.NewClient(cfg, store, db)
	hub := events.NewHub()
	mgr := NewManager(cfg, db, store, smtpClient, hub)
	fastDelay := 10 * time.Millisecond
	mgr.delayOverride = &fastDelay

	seriesID := stageSampleSeriesForTest(t, mgr, 4) // 0/3, 1/3, 2/3, 3/3

	// Inject SMTP failure on the 3rd transmission (item seq_index 2).
	mockSMTP.setFailOnNumber(3)
	if err := mgr.StartSend(seriesID); err != nil {
		t.Fatalf("StartSend failed: %v", err)
	}
	mgr.Wait()

	afterFail, err := db.GetPatchSeriesDraft(seriesID)
	if err != nil || afterFail == nil {
		t.Fatalf("GetPatchSeriesDraft after fail: %v", err)
	}
	if afterFail.Status != database.SeriesStatusFailed {
		t.Errorf("series status = %q, want %q", afterFail.Status, database.SeriesStatusFailed)
	}
	wantStatuses := []string{
		database.SeriesItemStatusSent,
		database.SeriesItemStatusSent,
		database.SeriesItemStatusFailed,
		database.SeriesItemStatusDraft,
	}
	for i, want := range wantStatuses {
		if afterFail.Items[i].Status != want {
			t.Errorf("item[%d].Status = %q, want %q", i, afterFail.Items[i].Status, want)
		}
	}
	if len(mockSMTP.getMessages()) != 2 {
		t.Fatalf("sent count after failure = %d, want 2", len(mockSMTP.getMessages()))
	}

	// Resume with mock SMTP healthy: only items 2 and 3 must be transmitted (total 4, zero duplicates).
	mockSMTP.setFailOnNumber(0)
	if err := mgr.StartSend(seriesID); err != nil {
		t.Fatalf("Resume StartSend failed: %v", err)
	}
	mgr.Wait()

	finalSeries, err := db.GetPatchSeriesDraft(seriesID)
	if err != nil || finalSeries == nil {
		t.Fatalf("GetPatchSeriesDraft after resume: %v", err)
	}
	if finalSeries.Status != database.SeriesStatusSent {
		t.Errorf("final series status = %q, want %q", finalSeries.Status, database.SeriesStatusSent)
	}
	for i, it := range finalSeries.Items {
		if it.Status != database.SeriesItemStatusSent {
			t.Errorf("item[%d].Status = %q, want sent", i, it.Status)
		}
	}

	sentMsgs := mockSMTP.getMessages()
	if len(sentMsgs) != 4 {
		t.Fatalf("total SMTP transmissions = %d, want 4 (zero duplicates)", len(sentMsgs))
	}
	for i, sm := range sentMsgs {
		wantSubj := fmt.Sprintf("Subject: [PATCH %d/3]", i)
		if !strings.Contains(sm.Data, wantSubj) {
			t.Errorf("sent message %d missing %q", i, wantSubj)
		}
		if strings.Contains(sm.Data, "X-Undine-Series-") {
			t.Errorf("sent message %d leaked internal X-Undine-Series-* header", i)
		}
		if i > 0 && !strings.Contains(sm.Data, "\tnew_") {
			t.Errorf("sent message %d lost raw tab indentation", i)
		}
	}
}

func TestValidatePatchDiffWarnings(t *testing.T) {
	broken := "No signoff here.\n---\ndiff --git a/kernel/foo.c b/kernel/foo.c\n--- a/kernel/foo.c\n+++ b/kernel/foo.c\n@@ -1,2 +1,5 @@\n-old();\n+        new();\n"
	warnings := ValidatePatchDiff(1, broken)
	codes := make(map[string]bool)
	for _, w := range warnings {
		codes[w.Code] = true
	}
	for _, want := range []string{"missing_signed_off_by", "diff_hunk_mismatch", "spaces_instead_of_tabs"} {
		if !codes[want] {
			t.Errorf("missing expected warning code %q in %+v", want, warnings)
		}
	}
}
