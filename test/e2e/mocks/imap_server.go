package mocks

import (
	"bufio"
	"fmt"
	"net"
	"strconv"
	"strings"
	"sync"
)

// MockIMAPMessage represents an email stored inside the MockIMAPServer.
type MockIMAPMessage struct {
	UID     uint32
	Flags   []string
	Raw     string
	Headers string
}

// MockIMAPServer is a lightweight TCP IMAP4rev1 server for E2E tests.
type MockIMAPServer struct {
	listener  net.Listener
	port      int
	mu        sync.Mutex
	mailboxes map[string][]*MockIMAPMessage
	nextUID   uint32
	closed    bool
}

// NewMockIMAPServer starts a mock IMAP server on a random loopback port.
func NewMockIMAPServer() (*MockIMAPServer, error) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, fmt.Errorf("failed to bind mock IMAP listener: %w", err)
	}

	s := &MockIMAPServer{
		listener:  l,
		port:      l.Addr().(*net.TCPAddr).Port,
		mailboxes: make(map[string][]*MockIMAPMessage),
		nextUID:   100,
	}

	s.mailboxes["INBOX"] = make([]*MockIMAPMessage, 0)
	go s.serve()
	return s, nil
}

func (s *MockIMAPServer) serve() {
	for {
		conn, err := s.listener.Accept()
		if err != nil {
			return
		}
		go s.handleConn(conn)
	}
}

// AddMessage appends a raw RFC822 email to the specified mailbox and returns its UID.
func (s *MockIMAPServer) AddMessage(mailbox string, rawRFC822 string) uint32 {
	s.mu.Lock()
	defer s.mu.Unlock()

	uid := s.nextUID
	s.nextUID++

	// Extract headers
	headers := rawRFC822
	if idx := strings.Index(rawRFC822, "\r\n\r\n"); idx >= 0 {
		headers = rawRFC822[:idx]
	} else if idx := strings.Index(rawRFC822, "\n\n"); idx >= 0 {
		headers = rawRFC822[:idx]
	}

	// Normalize header CRLF and ensure ending with \r\n\r\n
	headers = strings.ReplaceAll(headers, "\r\n", "\n")
	headers = strings.TrimRight(headers, "\n") + "\n\n"
	headers = strings.ReplaceAll(headers, "\n", "\r\n")

	msg := &MockIMAPMessage{
		UID:     uid,
		Flags:   []string{},
		Raw:     rawRFC822,
		Headers: headers,
	}

	mb := strings.ToUpper(mailbox)
	s.mailboxes[mb] = append(s.mailboxes[mb], msg)
	return uid
}

// RemoveMessage removes a message with the specified UID from the mailbox.
func (s *MockIMAPServer) RemoveMessage(mailbox string, uid uint32) bool {
	s.mu.Lock()
	defer s.mu.Unlock()

	mb := strings.ToUpper(mailbox)
	msgs := s.mailboxes[mb]
	for i, m := range msgs {
		if m.UID == uid {
			s.mailboxes[mb] = append(msgs[:i], msgs[i+1:]...)
			return true
		}
	}
	return false
}

func (s *MockIMAPServer) handleConn(c net.Conn) {
	defer c.Close()
	r := bufio.NewReader(c)

	fmt.Fprintf(c, "* OK [CAPABILITY IMAP4rev1] Mock IMAP Server Ready\r\n")

	selectedMailbox := "INBOX"

	for {
		line, err := r.ReadString('\n')
		if err != nil {
			return
		}
		trimmed := strings.TrimRight(line, "\r\n")
		parts := strings.SplitN(trimmed, " ", 3)
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

		case "SELECT":
			mbName := strings.Trim(arg, "\"")
			selectedMailbox = strings.ToUpper(mbName)

			s.mu.Lock()
			msgs := s.mailboxes[selectedMailbox]
			count := len(msgs)
			s.mu.Unlock()

			fmt.Fprintf(c, "* %d EXISTS\r\n* 0 RECENT\r\n* FLAGS (\\Seen \\Answered \\Draft \\Flagged $cl_1 $cl_2)\r\n* OK [UIDVALIDITY 12345678] UIDs valid\r\n%s OK [READ-WRITE] SELECT completed\r\n", count, tag)

		case "UID":
			s.handleUIDCommand(c, tag, arg, selectedMailbox)

		case "NOOP":
			fmt.Fprintf(c, "%s OK NOOP completed\r\n", tag)

		case "LOGOUT":
			fmt.Fprintf(c, "* BYE Mock IMAP logging out\r\n%s OK LOGOUT completed\r\n", tag)
			return

		default:
			fmt.Fprintf(c, "%s OK %s completed\r\n", tag, cmd)
		}
	}
}

func (s *MockIMAPServer) handleUIDCommand(c net.Conn, tag, arg, selectedMailbox string) {
	subParts := strings.SplitN(arg, " ", 2)
	subCmd := strings.ToUpper(subParts[0])
	subArg := ""
	if len(subParts) > 1 {
		subArg = subParts[1]
	}

	s.mu.Lock()
	msgs := s.mailboxes[selectedMailbox]
	s.mu.Unlock()

	switch subCmd {
	case "SEARCH":
		var uids []string
		for _, m := range msgs {
			uids = append(uids, strconv.Itoa(int(m.UID)))
		}
		if len(uids) > 0 {
			fmt.Fprintf(c, "* SEARCH %s\r\n", strings.Join(uids, " "))
		} else {
			fmt.Fprintf(c, "* SEARCH\r\n")
		}
		fmt.Fprintf(c, "%s OK UID SEARCH completed\r\n", tag)

	case "FETCH":
		// subArg format: <seqset> (FLAGS BODY[HEADER.FIELDS (...)]) or (FLAGS BODY[]) or (BODY.PEEK[])
		upperArg := strings.ToUpper(subArg)
		isHeaderFetch := strings.Contains(upperArg, "HEADER")
		isFullBodyFetch := strings.Contains(upperArg, "BODY[]") || strings.Contains(upperArg, "BODY.PEEK[]") || strings.Contains(upperArg, "BODY[") || strings.Contains(upperArg, "RFC822")

		// Extract target UID if specified
		var targetUID uint32
		fields := strings.Fields(subArg)
		if len(fields) > 0 {
			if num, err := strconv.ParseUint(fields[0], 10, 32); err == nil {
				targetUID = uint32(num)
			}
		}

		for seq, m := range msgs {
			if targetUID > 0 && m.UID != targetUID {
				continue
			}
			flagsStr := strings.Join(m.Flags, " ")
			if isHeaderFetch {
				hdrLen := len(m.Headers)
				fmt.Fprintf(c, "* %d FETCH (UID %d FLAGS (%s) BODY[HEADER.FIELDS (Message-ID Date From To Cc Subject In-Reply-To References Review-Commit-ID)] {%d}\r\n%s)\r\n",
					seq+1, m.UID, flagsStr, hdrLen, m.Headers)
			} else if isFullBodyFetch {
				bodyLen := len(m.Raw)
				fmt.Fprintf(c, "* %d FETCH (UID %d FLAGS (%s) BODY[] {%d}\r\n%s)\r\n",
					seq+1, m.UID, flagsStr, bodyLen, m.Raw)
			} else {
				fmt.Fprintf(c, "* %d FETCH (UID %d FLAGS (%s))\r\n", seq+1, m.UID, flagsStr)
			}
		}
		fmt.Fprintf(c, "%s OK UID FETCH completed\r\n", tag)

	case "STORE":
		fmt.Fprintf(c, "%s OK UID STORE completed\r\n", tag)

	default:
		fmt.Fprintf(c, "%s OK UID %s completed\r\n", tag, subCmd)
	}
}

// Port returns the listening TCP port.
func (s *MockIMAPServer) Port() int {
	return s.port
}

// Addr returns the listening host:port address.
func (s *MockIMAPServer) Addr() string {
	return fmt.Sprintf("127.0.0.1:%d", s.port)
}

// Close terminates the mock IMAP listener.
func (s *MockIMAPServer) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil
	}
	s.closed = true
	return s.listener.Close()
}
