// Package smtp implements strict plain-text SMTP delivery conforming to LKML requirements.
package smtp

import (
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"log"
	"mime"
	"net"
	"net/http"
	"net/smtp"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"undined-go/internal/config"
	"undined-go/internal/constants"
	"undined-go/internal/storage/database"
	"undined-go/internal/storage/maildir"
	"undined-go/internal/storage/parser"
)

// MaxAttachmentTotalBytes limits the combined raw size of attachments on a single message (25 MiB).
const MaxAttachmentTotalBytes = 25 * 1024 * 1024

// Attachment represents a file to be MIME-encoded and attached to a draft or outgoing email.
type Attachment struct {
	FileName    string
	ContentType string
	Data        []byte
}

// DetectAttachmentContentType determines a safe MIME content type from a filename and payload.
func DetectAttachmentContentType(filename string, data []byte) string {
	ext := strings.ToLower(filepath.Ext(filename))
	switch ext {
	case ".patch", ".diff":
		return "text/x-patch"
	case ".md", ".markdown":
		return "text/markdown"
	case ".log", ".txt":
		return "text/plain"
	case ".json":
		return "application/json"
	case ".yaml", ".yml":
		return "text/yaml"
	case ".csv":
		return "text/csv"
	}
	if ext != "" {
		if detected := mime.TypeByExtension(ext); detected != "" {
			if mediaType, _, err := mime.ParseMediaType(detected); err == nil && mediaType != "" {
				return mediaType
			}
			return detected
		}
	}
	if len(data) > 0 {
		detected := http.DetectContentType(data)
		if mediaType, _, err := mime.ParseMediaType(detected); err == nil && mediaType != "" {
			return mediaType
		}
	}
	return "application/octet-stream"
}

func sanitizeContentType(ct, filename string, data []byte) string {
	ct = sanitizeHeaderValue(ct)
	if ct != "" {
		if mediaType, _, err := mime.ParseMediaType(ct); err == nil && mediaType != "" {
			return strings.ToLower(mediaType)
		}
	}
	return DetectAttachmentContentType(filename, data)
}

func wrapBase64CRLF(data []byte) string {
	encoded := base64.StdEncoding.EncodeToString(data)
	if len(encoded) <= 76 {
		return encoded
	}
	var b strings.Builder
	b.Grow(len(encoded) + (len(encoded)/76)*2)
	for i := 0; i < len(encoded); i += 76 {
		if i > 0 {
			b.WriteString("\r\n")
		}
		end := i + 76
		if end > len(encoded) {
			end = len(encoded)
		}
		b.WriteString(encoded[i:end])
	}
	return b.String()
}

// Client handles outgoing email delivery via strict plaintext SMTP.
type Client struct {
	Config  *config.Config
	Maildir *maildir.MaildirStore
	DB      *database.Database
}

// NewClient creates a new SMTP Client.
func NewClient(cfg *config.Config, store *maildir.MaildirStore, db *database.Database) *Client {
	return &Client{
		Config:  cfg,
		Maildir: store,
		DB:      db,
	}
}

// sanitizeHeaderValue strips CR and LF characters to prevent SMTP/RFC822 header injection.
func sanitizeHeaderValue(val string) string {
	val = strings.ReplaceAll(val, "\r", "")
	val = strings.ReplaceAll(val, "\n", "")
	return strings.TrimSpace(val)
}

// encodeHeaderValue encodes a header value using RFC 2047 Q-encoding if it contains non-ASCII characters.
func encodeHeaderValue(val string) string {
	val = sanitizeHeaderValue(val)
	return mime.QEncoding.Encode("utf-8", val)
}

// splitAddressList splits a comma-separated RFC 5322 address list while respecting
// double quotes, angle brackets, and unquoted display names with commas.
func splitAddressList(addrs string) []string {
	return parser.SplitAddressList(addrs)
}

// encodeAddressList sanitizes an address list, encodes non-ASCII display names per RFC 2047,
// and quotes display names containing RFC 5322 specials (such as ',' or '(') per RFC 5322.
func encodeAddressList(addrs string) string {
	addrs = sanitizeHeaderValue(addrs)
	if addrs == "" {
		return ""
	}
	parts := splitAddressList(addrs)
	encodedParts := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if idx := strings.LastIndex(p, "<"); idx > 0 {
			name := strings.TrimSpace(p[:idx])
			addr := strings.TrimSpace(p[idx:])
			hasNonASCII := false
			for i := 0; i < len(name); i++ {
				if name[i] >= 0x80 {
					hasNonASCII = true
					break
				}
			}
			if hasNonASCII {
				unquoted := strings.Trim(name, `"`)
				encodedParts = append(encodedParts, fmt.Sprintf("%s %s", mime.QEncoding.Encode("utf-8", unquoted), addr))
			} else {
				quotedName := parser.QuoteDisplayName(name)
				encodedParts = append(encodedParts, fmt.Sprintf("%s %s", quotedName, addr))
			}
		} else {
			encodedParts = append(encodedParts, p)
		}
	}
	return strings.Join(encodedParts, ", ")
}

// HeaderField represents an additional RFC 5322 header name/value pair.
type HeaderField struct {
	Name  string
	Value string
}

// PlainTextMessageOptions configures BuildPlainTextMessage for strict LKML plain-text emails.
type PlainTextMessageOptions struct {
	To           string
	Cc           string
	Subject      string
	Body         string
	InReplyTo    string
	References   string
	CustomFrom   string
	MessageID    string
	Date         time.Time
	ExtraHeaders []HeaderField
}

// GenerateGitSendEmailMessageID generates a deterministic git-send-email style Message-ID:
// <YYYYMMDDHHMMSS.<token>-<seq>-<fromEmail>>.
func GenerateGitSendEmailMessageID(ts time.Time, token int, seq int, fromEmail string) string {
	if ts.IsZero() {
		ts = time.Now().UTC()
	}
	if seq <= 0 {
		seq = 1
	}
	if token < 0 {
		token = -token
	}
	addr := sanitizeHeaderValue(fromEmail)
	if addr == "" || !strings.Contains(addr, "@") {
		addr = "dev@undined.local"
	}
	return fmt.Sprintf("<%s.%05d-%d-%s>", ts.UTC().Format("20060102150405"), token%100000, seq, addr)
}

// RandomSeriesToken returns a cryptographically random 5-digit integer in [10000, 99999]
// suitable for git-send-email style Message-IDs.
func RandomSeriesToken() int {
	var b [4]byte
	// crypto/rand.Read never returns an error on supported platforms (Go 1.24+).
	_, _ = rand.Read(b[:])
	return 10000 + int(binary.BigEndian.Uint32(b[:])%90000)
}

// BuildPlainTextMessage constructs a strict 8-bit UTF-8 plain-text RFC 5322 email
// preserving tabs and line breaks verbatim, returning the raw message and its Message-ID.
func (c *Client) BuildPlainTextMessage(opts PlainTextMessageOptions) (string, string) {
	fromEmail := ""
	fromName := ""
	if c != nil && c.Config != nil {
		fromEmail = sanitizeHeaderValue(c.Config.SMTP.FromEmail)
		fromName = sanitizeHeaderValue(c.Config.SMTP.FromName)
	}
	if fromEmail == "" {
		fromEmail = constants.DefaultFromEmail
	}

	fromHdr := opts.CustomFrom
	if fromHdr == "" {
		if fromName != "" {
			fromHdr = encodeAddressList(fmt.Sprintf("%s <%s>", fromName, fromEmail))
		} else {
			fromHdr = fromEmail
		}
	} else {
		fromHdr = encodeAddressList(fromHdr)
	}

	toAddr := encodeAddressList(opts.To)
	ccAddr := encodeAddressList(opts.Cc)
	subject := encodeHeaderValue(opts.Subject)
	inReplyTo := sanitizeHeaderValue(opts.InReplyTo)
	references := sanitizeHeaderValue(opts.References)

	dateVal := opts.Date
	if dateVal.IsZero() {
		dateVal = time.Now()
	}
	dateStr := dateVal.Format("Mon, 02 Jan 2006 15:04:05 -0700")

	msgID := sanitizeHeaderValue(opts.MessageID)
	if cleaned := parser.CleanMsgID(msgID); cleaned != "" {
		msgID = "<" + cleaned + ">"
	} else {
		msgID = GenerateGitSendEmailMessageID(dateVal, RandomSeriesToken(), 1, fromEmail)
	}

	lines := []string{
		fmt.Sprintf("From: %s", fromHdr),
		fmt.Sprintf("To: %s", toAddr),
	}
	if strings.TrimSpace(ccAddr) != "" {
		lines = append(lines, fmt.Sprintf("Cc: %s", ccAddr))
	}
	lines = append(lines,
		fmt.Sprintf("Subject: %s", subject),
		fmt.Sprintf("Date: %s", dateStr),
		fmt.Sprintf("Message-ID: %s", msgID),
		fmt.Sprintf("X-Mailer: %s", constants.DefaultXMailer),
	)

	if strings.TrimSpace(inReplyTo) != "" {
		cleanedInReply := parser.CleanMsgID(inReplyTo)
		if cleanedInReply != "" {
			lines = append(lines, fmt.Sprintf("In-Reply-To: <%s>", cleanedInReply))
		}
	}
	if strings.TrimSpace(references) != "" {
		lines = append(lines, fmt.Sprintf("References: %s", strings.TrimSpace(references)))
	}

	for _, hdr := range opts.ExtraHeaders {
		name := sanitizeHeaderValue(hdr.Name)
		val := sanitizeHeaderValue(hdr.Value)
		if name == "" || strings.Contains(name, ":") {
			continue
		}
		lines = append(lines, fmt.Sprintf("%s: %s", name, val))
	}

	lines = append(lines,
		"MIME-Version: 1.0",
		"Content-Type: text/plain; charset=utf-8",
		"Content-Transfer-Encoding: 8bit",
		"",
	)

	headerBlock := strings.Join(lines, "\r\n") + "\r\n"
	body := opts.Body
	if !strings.HasSuffix(body, "\n") {
		body += "\n"
	}
	return headerBlock + body, msgID
}

// BuildMessage constructs an RFC822 email enforcing LKML plaintext rules and project identity.
func (c *Client) BuildMessage(
	toAddr, subject, bodyText string,
	ccAddr, inReplyTo, references, customFrom string,
) string {
	return c.BuildMessageWithAttachments(toAddr, subject, bodyText, ccAddr, inReplyTo, references, customFrom, nil)
}

// BuildMessageWithAttachments constructs an RFC822 email. When attachments is empty,
// it emits a strict 8-bit text/plain email; when attachments are present, it emits an
// RFC 2046 multipart/mixed email with a UTF-8 8-bit text/plain part followed by
// base64-encoded attachment parts.
func (c *Client) BuildMessageWithAttachments(
	toAddr, subject, bodyText string,
	ccAddr, inReplyTo, references, customFrom string,
	attachments []Attachment,
) string {
	fromHdr := customFrom
	if fromHdr == "" {
		fromName := sanitizeHeaderValue(c.Config.SMTP.FromName)
		fromEmail := sanitizeHeaderValue(c.Config.SMTP.FromEmail)
		if fromName != "" {
			fromHdr = encodeAddressList(fmt.Sprintf("%s <%s>", fromName, fromEmail))
		} else {
			fromHdr = fromEmail
		}
	} else {
		fromHdr = encodeAddressList(fromHdr)
	}

	toAddr = encodeAddressList(toAddr)
	ccAddr = encodeAddressList(ccAddr)
	subject = encodeHeaderValue(subject)
	inReplyTo = sanitizeHeaderValue(inReplyTo)
	references = sanitizeHeaderValue(references)

	dateStr := time.Now().Format("Mon, 02 Jan 2006 15:04:05 -0700")

	// Generate unique Message-ID if not provided
	rnd := make([]byte, 8)
	_, _ = rand.Read(rnd)
	host := c.Config.General.Host
	if host == "" || host == "127.0.0.1" || host == "localhost" {
		host = "undined.local"
	}
	msgID := fmt.Sprintf("<undine-%d-%s@%s>", time.Now().UnixNano(), hex.EncodeToString(rnd), host)

	var lines []string
	lines = append(lines,
		fmt.Sprintf("From: %s", fromHdr),
		fmt.Sprintf("To: %s", toAddr),
	)
	if strings.TrimSpace(ccAddr) != "" {
		lines = append(lines, fmt.Sprintf("Cc: %s", ccAddr))
	}
	lines = append(lines,
		fmt.Sprintf("Subject: %s", subject),
		fmt.Sprintf("Date: %s", dateStr),
		fmt.Sprintf("Message-ID: %s", msgID),
		fmt.Sprintf("X-Mailer: %s", constants.DefaultXMailer),
	)

	if strings.TrimSpace(inReplyTo) != "" {
		cleanedInReply := parser.CleanMsgID(inReplyTo)
		if cleanedInReply != "" {
			lines = append(lines, fmt.Sprintf("In-Reply-To: <%s>", cleanedInReply))
		}
	}
	if strings.TrimSpace(references) != "" {
		lines = append(lines, fmt.Sprintf("References: %s", strings.TrimSpace(references)))
	}

	if len(attachments) == 0 {
		lines = append(lines,
			"MIME-Version: 1.0",
			"Content-Type: text/plain; charset=utf-8",
			"Content-Transfer-Encoding: 8bit",
			"",
			bodyText,
		)
		return strings.Join(lines, "\r\n") + "\r\n"
	}

	bndBytes := make([]byte, 16)
	_, _ = rand.Read(bndBytes) // crypto/rand.Read never returns an error in Go 1.24+
	boundary := fmt.Sprintf("----=_UndinePart_%s", hex.EncodeToString(bndBytes))

	normalizedBody := strings.ReplaceAll(bodyText, "\r\n", "\n")
	normalizedBody = strings.ReplaceAll(normalizedBody, "\n", "\r\n")

	lines = append(lines,
		"MIME-Version: 1.0",
		fmt.Sprintf(`Content-Type: multipart/mixed; boundary="%s"`, boundary),
		"",
		"This is a multi-part message in MIME format.",
		"--"+boundary,
		"Content-Type: text/plain; charset=utf-8",
		"Content-Transfer-Encoding: 8bit",
		"",
		normalizedBody,
	)

	for idx, att := range attachments {
		contentType := sanitizeContentType(att.ContentType, att.FileName, att.Data)
		safeName := parser.SanitizeAttachmentFileName(att.FileName, idx, contentType)
		partContentType := mime.FormatMediaType(contentType, map[string]string{"name": safeName})
		if partContentType == "" {
			partContentType = fmt.Sprintf(`%s; name="%s"`, contentType, safeName)
		}
		disposition := mime.FormatMediaType("attachment", map[string]string{"filename": safeName})
		if disposition == "" {
			disposition = fmt.Sprintf(`attachment; filename="%s"`, safeName)
		}

		lines = append(lines,
			"--"+boundary,
			"Content-Type: "+partContentType,
			"Content-Disposition: "+disposition,
			"Content-Transfer-Encoding: base64",
			"",
			wrapBase64CRLF(att.Data),
		)
	}

	lines = append(lines, "--"+boundary+"--")
	return strings.Join(lines, "\r\n") + "\r\n"
}

func (c *Client) getTLSConfig() *tls.Config {
	cfg := c.Config.SMTP
	tlsConf := &tls.Config{
		ServerName:         cfg.Host,
		InsecureSkipVerify: !cfg.VerifyCert,
	}

	if cfg.VerifyCert && cfg.CACertPath != "" {
		caPath := config.ExpandPath(cfg.CACertPath)
		caCert, err := os.ReadFile(caPath)
		if err == nil {
			caCertPool := x509.NewCertPool()
			caCertPool.AppendCertsFromPEM(caCert)
			tlsConf.RootCAs = caCertPool
		}
	}

	return tlsConf
}

// mailFrom issues a MAIL FROM command. Unlike Go's standard net/smtp.Client.Mail(), which
// unconditionally appends the SMTPUTF8 parameter whenever the server advertises it,
// mailFrom only appends SMTPUTF8 if explicitly configured (use_smtputf8 = true).
// This prevents outgoing mail from being bounced by intermediate relays (e.g., Postfix
// bouncing with "SMTPUTF8 is required, but was not offered by host") when forwarding LKML
// patches to downstream servers (such as vger.kernel.org) that do not support RFC 6531 SMTPUTF8.
func (c *Client) mailFrom(smtpClient *smtp.Client, fromAddr string) error {
	fromAddr = sanitizeHeaderValue(fromAddr)
	if strings.ContainsAny(fromAddr, "\r\n") {
		return errors.New("smtp: A line must not contain CR or LF")
	}

	cleanFrom := strings.Trim(strings.TrimSpace(fromAddr), "<>")

	cmdStr := fmt.Sprintf("MAIL FROM:<%s>", cleanFrom)
	if ok, _ := smtpClient.Extension("8BITMIME"); ok {
		cmdStr += " BODY=8BITMIME"
	}
	if c.Config != nil && c.Config.SMTP.UseSMTPUTF8 {
		if ok, _ := smtpClient.Extension("SMTPUTF8"); ok {
			cmdStr += " SMTPUTF8"
		}
	}

	id, err := smtpClient.Text.Cmd("%s", cmdStr)
	if err != nil {
		return err
	}
	smtpClient.Text.StartResponse(id)
	defer smtpClient.Text.EndResponse(id)
	_, _, err = smtpClient.Text.ReadResponse(250)
	return err
}

// SendRawMessage sends raw RFC822 bytes to SMTP recipients and saves a copy into Sent.
func (c *Client) SendRawMessage(rawEmail string, recipients []string) (bool, string, error) {
	cfg := c.Config.SMTP
	addr := net.JoinHostPort(cfg.Host, strconv.Itoa(cfg.Port))
	tlsConf := c.getTLSConfig()
	password := cfg.GetPassword()

	var smtpClient *smtp.Client
	var err error

	useSSL := cfg.UseSSL || (cfg.Port == 465 && !cfg.UseSTARTTLS)
	if useSSL {
		conn, dialErr := tls.Dial("tcp", addr, tlsConf)
		if dialErr != nil {
			return false, "", fmt.Errorf("TLS connection to %s failed: %w", addr, dialErr)
		}
		smtpClient, err = smtp.NewClient(conn, cfg.Host)
		if err != nil {
			conn.Close()
			return false, "", fmt.Errorf("SMTP handshake failed: %w", err)
		}
	} else {
		conn, dialErr := net.DialTimeout("tcp", addr, 15*time.Second)
		if dialErr != nil {
			return false, "", fmt.Errorf("connection to %s failed: %w", addr, dialErr)
		}
		smtpClient, err = smtp.NewClient(conn, cfg.Host)
		if err != nil {
			conn.Close()
			return false, "", fmt.Errorf("SMTP client initialization failed: %w", err)
		}

		if cfg.UseSTARTTLS {
			if ok, _ := smtpClient.Extension("STARTTLS"); ok {
				if err := smtpClient.StartTLS(tlsConf); err != nil {
					smtpClient.Close()
					return false, "", fmt.Errorf("STARTTLS negotiation failed: %w", err)
				}
			}
		}
	}
	defer smtpClient.Close()

	// Authenticate if credentials provided
	if cfg.Username != "" && password != "" {
		auth := smtp.PlainAuth("", cfg.Username, password, cfg.Host)
		if err := smtpClient.Auth(auth); err != nil {
			return false, "", fmt.Errorf("SMTP authentication failed for user %s: %w", cfg.Username, err)
		}
	}

	// Set From
	fromAddr := cfg.FromEmail
	if err := c.mailFrom(smtpClient, fromAddr); err != nil {
		return false, "", fmt.Errorf("SMTP MAIL FROM failed: %w", err)
	}

	// Set Recipients
	for _, rcpt := range recipients {
		cleanRcpt := sanitizeHeaderValue(rcpt)
		cleanRcpt = strings.Trim(strings.TrimSpace(cleanRcpt), "<>")
		if cleanRcpt != "" {
			if err := smtpClient.Rcpt(cleanRcpt); err != nil {
				return false, "", fmt.Errorf("SMTP RCPT TO failed for %s: %w", cleanRcpt, err)
			}
		}
	}

	// Send Data
	w, err := smtpClient.Data()
	if err != nil {
		return false, "", fmt.Errorf("SMTP DATA command failed: %w", err)
	}

	// Ensure CRLF endings for network wire transmission
	normalized := strings.ReplaceAll(rawEmail, "\r\n", "\n")
	crlfBody := strings.ReplaceAll(normalized, "\n", "\r\n")

	if _, err := w.Write([]byte(crlfBody)); err != nil {
		_ = w.Close()
		return false, "", fmt.Errorf("failed to write email body to SMTP: %w", err)
	}
	if err := w.Close(); err != nil {
		return false, "", fmt.Errorf("failed to close SMTP data stream: %w", err)
	}

	_ = smtpClient.Quit()

	// Save copy to Sent mailbox
	if c.Maildir != nil && c.DB != nil {
		if err := c.Maildir.EnsureFolder(constants.FolderSent); err != nil {
			log.Printf("Warning: failed to ensure Sent maildir folder: %v", err)
		}
		sentFolder, err := c.DB.GetOrCreateFolder(constants.FolderSent, false)
		if err != nil {
			log.Printf("Warning: failed to get or create Sent database folder: %v", err)
		} else {
			path, err := c.Maildir.AddMessage(constants.FolderSent, []byte(crlfBody), constants.FlagSeen)
			if err != nil {
				log.Printf("Warning: failed to store sent email in maildir: %v", err)
			} else {
				if _, err := c.DB.IndexEmailFile(sentFolder.ID, path); err != nil {
					log.Printf("Warning: failed to index sent email into database: %v", err)
				}
			}
		}
	}

	return true, "Email sent successfully", nil
}

// SendMessage builds and sends an email.
func (c *Client) SendMessage(
	toAddr, subject, bodyText string,
	ccAddr, inReplyTo, references string,
) (bool, string, error) {
	return c.SendMessageWithAttachments(toAddr, subject, bodyText, ccAddr, inReplyTo, references, nil)
}

// SendMessageWithAttachments builds and sends an email with optional MIME attachments.
func (c *Client) SendMessageWithAttachments(
	toAddr, subject, bodyText string,
	ccAddr, inReplyTo, references string,
	attachments []Attachment,
) (bool, string, error) {
	raw := c.BuildMessageWithAttachments(toAddr, subject, bodyText, ccAddr, inReplyTo, references, "", attachments)

	var recipients []string
	for _, a := range splitAddressList(toAddr) {
		addr := parser.ExtractMsgIDs(a)
		if len(addr) > 0 {
			recipients = append(recipients, addr[0])
		} else {
			cleaned := strings.TrimSpace(a)
			if cleaned != "" {
				recipients = append(recipients, cleaned)
			}
		}
	}
	for _, a := range splitAddressList(ccAddr) {
		addr := parser.ExtractMsgIDs(a)
		if len(addr) > 0 {
			recipients = append(recipients, addr[0])
		} else {
			cleaned := strings.TrimSpace(a)
			if cleaned != "" {
				recipients = append(recipients, cleaned)
			}
		}
	}

	ok, msg, err := c.SendRawMessage(raw, recipients)
	if err != nil {
		return false, msg, err
	}

	// If this was a reply to an existing message, mark all folder copies replied and discard draft comments
	if inReplyTo != "" && c.DB != nil {
		cleanInReply := parser.CleanMsgID(inReplyTo)
		origs, _ := c.DB.GetMessagesByMsgID(cleanInReply)
		for _, orig := range origs {
			if orig == nil || orig.FolderName == constants.FolderDrafts || orig.FolderName == constants.FolderSent {
				continue
			}
			_ = c.DB.SetMessageReplied(orig.ID, true)
			if c.Maildir != nil && orig.FilePath != "" {
				newPath, flagErr := c.Maildir.AddFlag(orig.FilePath, constants.FlagReplied)
				if flagErr == nil && newPath != orig.FilePath {
					_ = c.DB.SetMessageFilePath(orig.ID, newPath)
				}
			}
			flagsSlice := strings.Fields(orig.Flags)
			hasReplied := false
			for _, f := range flagsSlice {
				if f == `\Answered` || f == "Answered" || f == "R" {
					hasReplied = true
					break
				}
			}
			if !hasReplied {
				if orig.IMAPUID != nil {
					flagsSlice = append(flagsSlice, `\Answered`)
				} else {
					flagsSlice = append(flagsSlice, constants.FlagReplied)
				}
				_ = c.DB.SetMessageFlags(orig.ID, strings.Join(flagsSlice, " "))
			}
			_, _ = c.DB.DiscardDraftComments(orig.MessageID)
		}
	}

	return ok, msg, nil
}
