package mocks

import (
	"bufio"
	"fmt"
	"net"
	"net/mail"
	"strings"
	"sync"
)

// CapturedEmail represents an email captured by MockSMTPServer.
type CapturedEmail struct {
	From       string
	To         []string
	Raw        string
	Subject    string
	MessageID  string
	InReplyTo  string
	References string
	Body       string
}

// MockSMTPServer is an in-memory TCP SMTP server for E2E testing.
type MockSMTPServer struct {
	listener net.Listener
	port     int
	mu       sync.Mutex
	emails   []CapturedEmail
	closed   bool
}

// NewMockSMTPServer starts a mock SMTP server on a random loopback port.
func NewMockSMTPServer() (*MockSMTPServer, error) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, fmt.Errorf("failed to bind mock SMTP listener: %w", err)
	}

	s := &MockSMTPServer{
		listener: l,
		port:     l.Addr().(*net.TCPAddr).Port,
	}

	go s.serve()
	return s, nil
}

func (s *MockSMTPServer) serve() {
	for {
		conn, err := s.listener.Accept()
		if err != nil {
			return
		}
		go s.handleConn(conn)
	}
}

func (s *MockSMTPServer) handleConn(c net.Conn) {
	defer c.Close()
	r := bufio.NewReader(c)

	fmt.Fprintf(c, "220 mock.undined.local ESMTP Mock Service Ready\r\n")

	var currentFrom string
	var currentTo []string
	var dataLines []string
	inData := false

	for {
		line, err := r.ReadString('\n')
		if err != nil {
			return
		}
		trimmed := strings.TrimRight(line, "\r\n")

		if inData {
			if trimmed == "." {
				inData = false
				rawContent := strings.Join(dataLines, "\r\n")
				captured := s.parseCaptured(currentFrom, currentTo, rawContent)

				s.mu.Lock()
				s.emails = append(s.emails, captured)
				s.mu.Unlock()

				currentFrom = ""
				currentTo = nil
				dataLines = nil

				fmt.Fprintf(c, "250 2.0.0 OK: queued as 12345\r\n")
			} else {
				// Handle byte stuffing
				if strings.HasPrefix(trimmed, "..") {
					trimmed = trimmed[1:]
				}
				dataLines = append(dataLines, trimmed)
			}
			continue
		}

		upper := strings.ToUpper(trimmed)
		if strings.HasPrefix(upper, "EHLO") || strings.HasPrefix(upper, "HELO") {
			fmt.Fprintf(c, "250-mock.undined.local\r\n250-8BITMIME\r\n250 HELP\r\n")
		} else if strings.HasPrefix(upper, "MAIL FROM:") {
			raw := strings.TrimSpace(trimmed[10:])
			if start := strings.Index(raw, "<"); start >= 0 {
				if end := strings.Index(raw[start:], ">"); end >= 0 {
					raw = raw[start+1 : start+end]
				}
			} else {
				fields := strings.Fields(raw)
				if len(fields) > 0 {
					raw = fields[0]
				}
			}
			currentFrom = strings.TrimSpace(raw)
			fmt.Fprintf(c, "250 2.1.0 Sender <%s> OK\r\n", currentFrom)
		} else if strings.HasPrefix(upper, "RCPT TO:") {
			raw := strings.TrimSpace(trimmed[8:])
			if start := strings.Index(raw, "<"); start >= 0 {
				if end := strings.Index(raw[start:], ">"); end >= 0 {
					raw = raw[start+1 : start+end]
				}
			} else {
				fields := strings.Fields(raw)
				if len(fields) > 0 {
					raw = fields[0]
				}
			}
			to := strings.TrimSpace(raw)
			currentTo = append(currentTo, to)
			fmt.Fprintf(c, "250 2.1.5 Recipient <%s> OK\r\n", to)
		} else if upper == "DATA" {
			inData = true
			dataLines = nil
			fmt.Fprintf(c, "354 Start mail input; end with <CRLF>.<CRLF>\r\n")
		} else if upper == "RSET" {
			currentFrom = ""
			currentTo = nil
			dataLines = nil
			inData = false
			fmt.Fprintf(c, "250 2.0.0 Reset OK\r\n")
		} else if upper == "NOOP" {
			fmt.Fprintf(c, "250 2.0.0 OK\r\n")
		} else if upper == "QUIT" {
			fmt.Fprintf(c, "221 2.0.0 Bye\r\n")
			return
		} else {
			fmt.Fprintf(c, "500 Syntax error, command unrecognized\r\n")
		}
	}
}

func (s *MockSMTPServer) parseCaptured(from string, to []string, raw string) CapturedEmail {
	msg, err := mail.ReadMessage(strings.NewReader(raw))
	subject := ""
	msgID := ""
	inReplyTo := ""
	references := ""
	body := ""

	if err == nil {
		subject = msg.Header.Get("Subject")
		msgID = msg.Header.Get("Message-ID")
		inReplyTo = msg.Header.Get("In-Reply-To")
		references = msg.Header.Get("References")
	}

	// Extract body
	if idx := strings.Index(raw, "\r\n\r\n"); idx >= 0 {
		body = raw[idx+4:]
	} else if idx := strings.Index(raw, "\n\n"); idx >= 0 {
		body = raw[idx+2:]
	}

	return CapturedEmail{
		From:       from,
		To:         to,
		Raw:        raw,
		Subject:    subject,
		MessageID:  msgID,
		InReplyTo:  inReplyTo,
		References: references,
		Body:       body,
	}
}

// Port returns the listening TCP port.
func (s *MockSMTPServer) Port() int {
	return s.port
}

// Addr returns the listening host:port address.
func (s *MockSMTPServer) Addr() string {
	return fmt.Sprintf("127.0.0.1:%d", s.port)
}

// GetCapturedEmails returns a copy of all emails received by the server.
func (s *MockSMTPServer) GetCapturedEmails() []CapturedEmail {
	s.mu.Lock()
	defer s.mu.Unlock()
	res := make([]CapturedEmail, len(s.emails))
	copy(res, s.emails)
	return res
}

// Reset clears the captured email history.
func (s *MockSMTPServer) Reset() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.emails = nil
}

// Close terminates the mock SMTP listener.
func (s *MockSMTPServer) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil
	}
	s.closed = true
	return s.listener.Close()
}
