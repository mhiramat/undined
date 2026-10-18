package smtp

import (
	"bufio"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"undined-go/internal/config"
	"undined-go/internal/constants"
	"undined-go/internal/storage/database"
	"undined-go/internal/storage/maildir"
)

func TestBuildMessageStrictHeaders(t *testing.T) {
	cfg := config.NewDefaultConfig()
	cfg.SMTP.FromName = "Masami Hiramatsu"
	cfg.SMTP.FromEmail = "mhiramat@kernel.org"

	client := NewClient(cfg, nil, nil)

	body := "This is a clean plaintext patch review response.\nNo HTML here."
	raw := client.BuildMessage(
		"linux-trace-kernel@vger.kernel.org",
		"[PATCH] tracing: Fix probe handler",
		body,
		"rostedt@goodmis.org",
		"20261017.abc@kernel.org",
		"<20261017.abc@kernel.org>",
		"",
	)

	if !strings.Contains(raw, "From: Masami Hiramatsu <mhiramat@kernel.org>") {
		t.Errorf("Missing From: %s", raw)
	}
	if !strings.Contains(raw, "To: linux-trace-kernel@vger.kernel.org") {
		t.Errorf("Missing To: %s", raw)
	}
	if !strings.Contains(raw, "Cc: rostedt@goodmis.org") {
		t.Errorf("Missing Cc: %s", raw)
	}
	if !strings.Contains(raw, "Subject: [PATCH] tracing: Fix probe handler") {
		t.Errorf("Missing Subject: %s", raw)
	}
	if !strings.Contains(raw, "X-Mailer: "+constants.DefaultXMailer) {
		t.Errorf("Missing X-Mailer: %s", raw)
	}
	if !strings.Contains(raw, "In-Reply-To: <20261017.abc@kernel.org>") {
		t.Errorf("Missing In-Reply-To: %s", raw)
	}
	if !strings.Contains(raw, "Content-Type: text/plain; charset=utf-8") {
		t.Errorf("Missing Content-Type: %s", raw)
	}
	if !strings.Contains(raw, "Content-Transfer-Encoding: 8bit") {
		t.Errorf("Missing Content-Transfer-Encoding: %s", raw)
	}
	if !strings.Contains(raw, body) {
		t.Errorf("Missing body: %s", raw)
	}
}

func TestBuildMessageHeaderInjectionSanitization(t *testing.T) {
	cfg := config.NewDefaultConfig()
	cfg.SMTP.FromName = "Masami\r\nHiramatsu"
	cfg.SMTP.FromEmail = "mhiramat@kernel.org\r\nBcc: evil@attacker.com"

	client := NewClient(cfg, nil, nil)

	raw := client.BuildMessage(
		"to@example.com\r\nBcc: evil-to@attacker.com",
		"Test Subject\r\nBcc: evil-subj@attacker.com",
		"Test body",
		"cc@example.com\r\nBcc: evil-cc@attacker.com",
		"msgid123\r\nInjected: Header",
		"<refs>\r\nInjected: Refs",
		"",
	)

	// Verify no CRLF injection in headers
	headersPart := raw
	if idx := strings.Index(raw, "\r\n\r\n"); idx != -1 {
		headersPart = raw[:idx]
	}

	for _, line := range strings.Split(headersPart, "\r\n") {
		if strings.HasPrefix(strings.ToLower(line), "bcc:") {
			t.Errorf("found injected Bcc header: %s", line)
		}
		if strings.HasPrefix(strings.ToLower(line), "injected:") {
			t.Errorf("found injected header: %s", line)
		}
	}
}

func startMockSMTPServer(t *testing.T) (net.Listener, int) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Failed to start mock SMTP server: %v", err)
	}

	go func() {
		for {
			conn, err := l.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer c.Close()
				r := bufio.NewReader(c)
				fmt.Fprintf(c, "220 mock.undined.local SMTP Service Ready\r\n")
				inData := false
				for {
					line, err := r.ReadString('\n')
					if err != nil {
						return
					}
					line = strings.TrimRight(line, "\r\n")
					if inData {
						if line == "." {
							inData = false
							fmt.Fprintf(c, "250 2.0.0 OK: message queued\r\n")
						}
						continue
					}
					upper := strings.ToUpper(line)
					if strings.HasPrefix(upper, "EHLO") || strings.HasPrefix(upper, "HELO") {
						fmt.Fprintf(c, "250-mock.undined.local\r\n250 HELP\r\n")
					} else if strings.HasPrefix(upper, "MAIL FROM:") {
						fmt.Fprintf(c, "250 2.1.0 OK\r\n")
					} else if strings.HasPrefix(upper, "RCPT TO:") {
						fmt.Fprintf(c, "250 2.1.5 OK\r\n")
					} else if upper == "DATA" {
						inData = true
						fmt.Fprintf(c, "354 Start mail input; end with <CRLF>.<CRLF>\r\n")
					} else if upper == "QUIT" {
						fmt.Fprintf(c, "221 2.0.0 Bye\r\n")
						return
					} else {
						fmt.Fprintf(c, "500 Syntax error, command unrecognized\r\n")
					}
				}
			}(conn)
		}
	}()

	addr := l.Addr().(*net.TCPAddr)
	return l, addr.Port
}

func TestSendRawMessageWithMockSMTP(t *testing.T) {
	listener, port := startMockSMTPServer(t)
	defer listener.Close()

	tmpDir, err := os.MkdirTemp("", "undined-smtp-test-*")
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
	cfg.SMTP.Host = "127.0.0.1"
	cfg.SMTP.Port = port
	cfg.SMTP.UseSSL = false
	cfg.SMTP.UseSTARTTLS = false
	cfg.SMTP.FromEmail = "mhiramat@kernel.org"

	client := NewClient(cfg, store, db)

	ok, msg, err := client.SendMessage(
		"linux-trace-kernel@vger.kernel.org",
		"Test Subject",
		"Hello world",
		"",
		"",
		"",
	)
	if err != nil {
		t.Fatalf("SendMessage failed: %v", err)
	}
	if !ok {
		t.Errorf("Expected success, got false: %s", msg)
	}

	// Verify message saved to Sent folder
	sentFolder, err := db.GetFolderByName(constants.FolderSent)
	if err != nil || sentFolder == nil {
		t.Fatalf("Sent folder not found in database: %v", err)
	}

	sentMsgs, err := db.ListMessages(&sentFolder.ID, 10, 0)
	if err != nil || len(sentMsgs) == 0 {
		t.Fatalf("Expected email in Sent folder, got %d msgs", len(sentMsgs))
	}

	if sentMsgs[0].Subject != "Test Subject" {
		t.Errorf("Expected Subject 'Test Subject', got %s", sentMsgs[0].Subject)
	}
}
