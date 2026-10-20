// Package smtp implements strict plain-text SMTP delivery conforming to LKML requirements.
package smtp

import (
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"errors"
	"fmt"
	"mime"
	"net"
	"net/smtp"
	"os"
	"strings"
	"time"

	"undined-go/internal/config"
	"undined-go/internal/constants"
	"undined-go/internal/storage/database"
	"undined-go/internal/storage/maildir"
	"undined-go/internal/storage/parser"
)

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

// encodeAddressList sanitizes an address list and encodes any non-ASCII display names per RFC 2047.
func encodeAddressList(addrs string) string {
	addrs = sanitizeHeaderValue(addrs)
	if addrs == "" {
		return ""
	}
	parts := strings.Split(addrs, ",")
	encodedParts := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if idx := strings.Index(p, "<"); idx > 0 {
			name := strings.TrimSpace(p[:idx])
			addr := strings.TrimSpace(p[idx:])
			encodedParts = append(encodedParts, fmt.Sprintf("%s %s", mime.QEncoding.Encode("utf-8", name), addr))
		} else {
			encodedParts = append(encodedParts, p)
		}
	}
	return strings.Join(encodedParts, ", ")
}

// BuildMessage constructs an RFC822 email enforcing LKML plaintext rules and project identity.
func (c *Client) BuildMessage(
	toAddr, subject, bodyText string,
	ccAddr, inReplyTo, references, customFrom string,
) string {
	fromHdr := customFrom
	if fromHdr == "" {
		fromName := sanitizeHeaderValue(c.Config.SMTP.FromName)
		fromEmail := sanitizeHeaderValue(c.Config.SMTP.FromEmail)
		if fromName != "" {
			fromHdr = fmt.Sprintf("%s <%s>", mime.QEncoding.Encode("utf-8", fromName), fromEmail)
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

	lines = append(lines,
		"MIME-Version: 1.0",
		"Content-Type: text/plain; charset=utf-8",
		"Content-Transfer-Encoding: 8bit",
		"",
		bodyText,
	)

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

	id, err := smtpClient.Text.Cmd(cmdStr)
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
	addr := fmt.Sprintf("%s:%d", cfg.Host, cfg.Port)
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
		_ = c.Maildir.EnsureFolder(constants.FolderSent)
		sentFolder, err := c.DB.GetOrCreateFolder(constants.FolderSent, false)
		if err == nil {
			path, err := c.Maildir.AddMessage(constants.FolderSent, []byte(crlfBody), constants.FlagSeen)
			if err == nil {
				_, _ = c.DB.IndexEmailFile(sentFolder.ID, path)
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
	raw := c.BuildMessage(toAddr, subject, bodyText, ccAddr, inReplyTo, references, "")

	var recipients []string
	for _, a := range strings.Split(toAddr, ",") {
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
	for _, a := range strings.Split(ccAddr, ",") {
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

	// If this was a reply to an existing message, mark it replied and discard draft comments
	if inReplyTo != "" && c.DB != nil {
		cleanInReply := parser.CleanMsgID(inReplyTo)
		orig, _ := c.DB.GetMessageByMsgID(cleanInReply)
		if orig != nil {
			_ = c.DB.SetMessageReplied(orig.ID, true)
			if c.Maildir != nil && orig.FilePath != "" {
				newPath, flagErr := c.Maildir.AddFlag(orig.FilePath, constants.FlagReplied)
				if flagErr == nil && newPath != orig.FilePath {
					_ = c.DB.SetMessageFilePath(orig.ID, newPath)
				}
			}
			_, _ = c.DB.DiscardDraftComments(orig.MessageID)
		}
	}

	return ok, msg, nil
}
