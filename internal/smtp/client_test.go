package smtp

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
	"undined-go/internal/constants"
	"undined-go/internal/storage/database"
	"undined-go/internal/storage/maildir"
)

func TestBuildMessageStrictHeaders(t *testing.T) {
	cfg := config.NewDefaultConfig()
	cfg.SMTP.FromName = "Kernel Developer"
	cfg.SMTP.FromEmail = "dev@example.com"

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

	if !strings.Contains(raw, "From: Kernel Developer <dev@example.com>") {
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
	cfg.SMTP.FromName = "Kernel\r\nDeveloper"
	cfg.SMTP.FromEmail = "dev@example.com\r\nBcc: evil@attacker.com"

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
	t.Helper()
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
	cfg.SMTP.FromEmail = "dev@example.com"

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

func startRecordingMockSMTPServer(t *testing.T, advertiseSMTPUTF8 bool) (net.Listener, int, func() []string) {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Failed to start mock SMTP server: %v", err)
	}

	var mu sync.Mutex
	var commands []string

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
					mu.Lock()
					commands = append(commands, line)
					mu.Unlock()

					upper := strings.ToUpper(line)
					if strings.HasPrefix(upper, "EHLO") || strings.HasPrefix(upper, "HELO") {
						if advertiseSMTPUTF8 {
							fmt.Fprintf(c, "250-mock.undined.local\r\n250-8BITMIME\r\n250-SMTPUTF8\r\n250 HELP\r\n")
						} else {
							fmt.Fprintf(c, "250-mock.undined.local\r\n250-8BITMIME\r\n250 HELP\r\n")
						}
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
	getCommands := func() []string {
		mu.Lock()
		defer mu.Unlock()
		cp := make([]string, len(commands))
		copy(cp, commands)
		return cp
	}
	return l, addr.Port, getCommands
}

func TestSMTPUTF8NotSentByDefault(t *testing.T) {
	listener, port, getCommands := startRecordingMockSMTPServer(t, true)
	defer listener.Close()

	cfg := config.NewDefaultConfig()
	cfg.SMTP.Host = "127.0.0.1"
	cfg.SMTP.Port = port
	cfg.SMTP.UseSSL = false
	cfg.SMTP.UseSTARTTLS = false
	cfg.SMTP.FromEmail = "dev@example.com"
	cfg.SMTP.UseSMTPUTF8 = false

	client := NewClient(cfg, nil, nil)
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
		t.Fatalf("Expected success, got false: %s", msg)
	}

	cmds := getCommands()
	var mailFromCmd string
	for _, cmd := range cmds {
		if strings.HasPrefix(strings.ToUpper(cmd), "MAIL FROM:") {
			mailFromCmd = cmd
			break
		}
	}

	if mailFromCmd == "" {
		t.Fatalf("No MAIL FROM command recorded: %v", cmds)
	}
	if strings.Contains(mailFromCmd, "SMTPUTF8") {
		t.Errorf("MAIL FROM should not include SMTPUTF8 by default, got: %s", mailFromCmd)
	}
	if !strings.Contains(mailFromCmd, "BODY=8BITMIME") {
		t.Errorf("MAIL FROM should include BODY=8BITMIME, got: %s", mailFromCmd)
	}
}

func TestSMTPUTF8SentWhenConfigured(t *testing.T) {
	listener, port, getCommands := startRecordingMockSMTPServer(t, true)
	defer listener.Close()

	cfg := config.NewDefaultConfig()
	cfg.SMTP.Host = "127.0.0.1"
	cfg.SMTP.Port = port
	cfg.SMTP.UseSSL = false
	cfg.SMTP.UseSTARTTLS = false
	cfg.SMTP.FromEmail = "dev@example.com"
	cfg.SMTP.UseSMTPUTF8 = true

	client := NewClient(cfg, nil, nil)
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
		t.Fatalf("Expected success, got false: %s", msg)
	}

	cmds := getCommands()
	var mailFromCmd string
	for _, cmd := range cmds {
		if strings.HasPrefix(strings.ToUpper(cmd), "MAIL FROM:") {
			mailFromCmd = cmd
			break
		}
	}

	if mailFromCmd == "" {
		t.Fatalf("No MAIL FROM command recorded: %v", cmds)
	}
	if !strings.Contains(mailFromCmd, "SMTPUTF8") {
		t.Errorf("MAIL FROM should include SMTPUTF8 when configured, got: %s", mailFromCmd)
	}
}

func TestBuildMessageRFC2047Encoding(t *testing.T) {
	cfg := config.NewDefaultConfig()
	cfg.SMTP.FromName = "Reviewer Björn"
	cfg.SMTP.FromEmail = "dev@example.com"

	client := NewClient(cfg, nil, nil)

	body := "Plain text body with UTF-8: こんにちは"
	raw := client.BuildMessage(
		"Recipient <dev@example.org>",
		"[PATCH] tracing: Fix 日本語 issue",
		body,
		"",
		"",
		"",
		"",
	)

	// From display name should be RFC 2047 encoded
	if !strings.Contains(raw, "From: =?utf-8?q?Reviewer_Bj=C3=B6rn?= <dev@example.com>") {
		t.Errorf("From header not properly RFC 2047 encoded: %s", raw)
	}
	// Subject should be RFC 2047 encoded
	if !strings.Contains(raw, "=?utf-8?q?") || !strings.Contains(raw, "Subject:") {
		t.Errorf("Subject header not RFC 2047 encoded: %s", raw)
	}
	// Raw UTF-8 body must be preserved without distortion
	if !strings.Contains(raw, body) {
		t.Errorf("Body content was altered: %s", raw)
	}
}

func TestSendMessageQuotedCommaInDisplayName(t *testing.T) {
	listener, port, getCommands := startRecordingMockSMTPServer(t, false)
	defer listener.Close()

	cfg := config.NewDefaultConfig()
	cfg.SMTP.Host = "127.0.0.1"
	cfg.SMTP.Port = port
	cfg.SMTP.UseSSL = false
	cfg.SMTP.UseSTARTTLS = false
	cfg.SMTP.FromEmail = "dev@example.com"

	client := NewClient(cfg, nil, nil)
	ok, _, err := client.SendMessage(
		`"Developer, Kernel" <dev@example.com>, second@example.com`,
		"Test Quoted Comma",
		"Body",
		`"Maintainer, Linux" <maint@example.com>`,
		"",
		"",
	)
	if err != nil || !ok {
		t.Fatalf("SendMessage with quoted comma failed: %v", err)
	}

	var rcptCmds []string
	for _, cmd := range getCommands() {
		if strings.HasPrefix(strings.ToUpper(cmd), "RCPT TO:") {
			rcptCmds = append(rcptCmds, cmd)
		}
	}
	if len(rcptCmds) != 3 {
		t.Fatalf("expected 3 RCPT TO commands, got %d: %v", len(rcptCmds), rcptCmds)
	}
	if !strings.Contains(rcptCmds[0], "<dev@example.com>") ||
		!strings.Contains(rcptCmds[1], "<second@example.com>") ||
		!strings.Contains(rcptCmds[2], "<maint@example.com>") {
		t.Errorf("unexpected RCPT TO commands: %v", rcptCmds)
	}
}

func TestBuildMessageReceiverDisplayNameQuoting(t *testing.T) {
	cfg := config.NewDefaultConfig()
	cfg.SMTP.FromName = "Kernel Developer"
	cfg.SMTP.FromEmail = "dev@example.com"

	client := NewClient(cfg, nil, nil)

	tests := []struct {
		name       string
		to         string
		cc         string
		expectedTo string
		expectedCc string
	}{
		{
			name:       "unquoted comma in To display name",
			to:         "kernel, developer <dev@kernel.org>",
			cc:         "",
			expectedTo: `To: "kernel, developer" <dev@kernel.org>`,
			expectedCc: "",
		},
		{
			name:       "parenthesis in Cc display name",
			to:         "dev@example.com",
			cc:         "developer (test) <dev@kernel.org>",
			expectedTo: "To: dev@example.com",
			expectedCc: `Cc: "developer (test)" <dev@kernel.org>`,
		},
		{
			name:       "MixedMultipleReceivers",
			to:         "kernel, developer <dev@kernel.org>, Normal Developer <norm@example.com>",
			cc:         "developer (test) <test@example.com>, plain@example.com",
			expectedTo: `To: "kernel, developer" <dev@kernel.org>, Normal Developer <norm@example.com>`,
			expectedCc: `Cc: "developer (test)" <test@example.com>, plain@example.com`,
		},
		{
			name:       "AlreadyQuotedCommaDisplayName",
			to:         `"kernel, developer" <dev@kernel.org>`,
			cc:         `"developer (test)" <test@example.com>`,
			expectedTo: `To: "kernel, developer" <dev@kernel.org>`,
			expectedCc: `Cc: "developer (test)" <test@example.com>`,
		},
		{
			name:       "PlainDisplayNamePreserved",
			to:         "Kernel Developer <dev@kernel.org>",
			cc:         "Reviewer <reviewer@kernel.org>",
			expectedTo: "To: Kernel Developer <dev@kernel.org>",
			expectedCc: "Cc: Reviewer <reviewer@kernel.org>",
		},
		{
			name:       "DisplayNameParenthesesAndCommas",
			to:         "Developer, Kernel (Google) <dev@kernel.org>",
			cc:         "",
			expectedTo: `To: "Developer, Kernel (Google)" <dev@kernel.org>`,
			expectedCc: "",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			raw := client.BuildMessage(tc.to, "Test Subject", "Test Body", tc.cc, "", "", "")
			if tc.expectedTo != "" && !strings.Contains(raw, tc.expectedTo) {
				t.Errorf("expected To header %q, raw message:\n%s", tc.expectedTo, raw)
			}
			if tc.expectedCc != "" && !strings.Contains(raw, tc.expectedCc) {
				t.Errorf("expected Cc header %q, raw message:\n%s", tc.expectedCc, raw)
			}
		})
	}
}

func TestSendMessageUnquotedCommaInDisplayName(t *testing.T) {
	listener, port, getCommands := startRecordingMockSMTPServer(t, false)
	defer listener.Close()

	cfg := config.NewDefaultConfig()
	cfg.SMTP.Host = "127.0.0.1"
	cfg.SMTP.Port = port
	cfg.SMTP.UseSSL = false
	cfg.SMTP.UseSTARTTLS = false
	cfg.SMTP.FromEmail = "dev@example.com"

	client := NewClient(cfg, nil, nil)
	ok, _, err := client.SendMessage(
		"kernel, developer <dev@kernel.org>, second@example.com",
		"Test Unquoted Comma",
		"Body",
		"developer (test) <maint@example.com>",
		"",
		"",
	)
	if err != nil || !ok {
		t.Fatalf("SendMessage with unquoted comma failed: %v", err)
	}

	var rcptCmds []string
	for _, cmd := range getCommands() {
		if strings.HasPrefix(strings.ToUpper(cmd), "RCPT TO:") {
			rcptCmds = append(rcptCmds, cmd)
		}
	}
	if len(rcptCmds) != 3 {
		t.Fatalf("expected 3 RCPT TO commands, got %d: %v", len(rcptCmds), rcptCmds)
	}
	if !strings.Contains(rcptCmds[0], "<dev@kernel.org>") ||
		!strings.Contains(rcptCmds[1], "<second@example.com>") ||
		!strings.Contains(rcptCmds[2], "<maint@example.com>") {
		t.Errorf("unexpected RCPT TO commands: %v", rcptCmds)
	}
}

