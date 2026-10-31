// Package parser handles RFC 5322 and MIME email decoding for UndineD.
package parser

import (
	"bytes"
	"errors"
	"fmt"
	"mime"
	"net/mail"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/jhillyerd/enmime"

	"undined-go/internal/lkml/patch"
)

var (
	bracketedMsgIDRegex = regexp.MustCompile(`<([^>]+)>`)
	patchHeaderRegexes  = []*regexp.Regexp{
		regexp.MustCompile(`(?m)^diff --git `),
		regexp.MustCompile(`(?m)^--- [ab]/.+\n\+\+\+ [ab]/.+`),
		regexp.MustCompile(`(?m)^@@ -\d+(?:,\d+)? \+\d+(?:,\d+)? @@`),
	}
)

// CleanMsgID strips angle brackets and surrounding whitespace from a Message-ID.
func CleanMsgID(msgID string) string {
	msgID = strings.TrimSpace(msgID)
	msgID = strings.TrimPrefix(msgID, "<")
	msgID = strings.TrimSuffix(msgID, ">")
	return strings.TrimSpace(msgID)
}

// ExtractMsgIDs extracts a list of clean Message-IDs from header values (such as References or In-Reply-To).
func ExtractMsgIDs(headerVal string) []string {
	if headerVal == "" {
		return nil
	}

	matches := bracketedMsgIDRegex.FindAllStringSubmatch(headerVal, -1)
	if len(matches) > 0 {
		var ids []string
		for _, m := range matches {
			if len(m) > 1 {
				cleaned := CleanMsgID(m[1])
				if cleaned != "" {
					ids = append(ids, cleaned)
				}
			}
		}
		return ids
	}

	// Fallback to whitespace separated tokens
	tokens := strings.Fields(headerVal)
	var ids []string
	for _, tok := range tokens {
		cleaned := CleanMsgID(tok)
		if cleaned != "" {
			ids = append(ids, cleaned)
		}
	}
	return ids
}

// DetectPatch checks if the text contains a standalone Linux kernel patch.
// If the patch diff is followed by continuing message body (such as review discussion),
// it returns false so the message is treated as a normal conversation email.
func DetectPatch(text string) bool {
	if text == "" {
		return false
	}
	if !strings.Contains(text, "diff --git ") && !strings.Contains(text, "--- a/") {
		return false
	}
	parsed := patch.Parse(text)
	if parsed.BodyContinuesAfter {
		return false
	}
	return parsed.IsPatch || len(parsed.Files) > 0
}

// Attachment represents extracted MIME attachment metadata and payload.
type Attachment struct {
	Index       int    `json:"index"`
	FileName    string `json:"filename"`
	ContentType string `json:"content_type"`
	Size        int64  `json:"size"`
	Payload     []byte `json:"-"`
}

// ParsedEmail represents extracted email metadata and plain-text content.
type ParsedEmail struct {
	MessageID        string        `json:"message_id"`
	Subject          string        `json:"subject"`
	FromName         string        `json:"from_name"`
	FromAddr         string        `json:"from_addr"`
	ToAddr           string        `json:"to_addr"`
	CcAddr           string        `json:"cc_addr"`
	Date             string        `json:"date"`
	EpochTimestamp   int64         `json:"epoch_timestamp"`
	InReplyTo        string        `json:"in_reply_to"`
	ReferencesHeader string        `json:"references_header"`
	BodyPlain        string        `json:"body_plain"`
	RawHeaders       string        `json:"raw_headers"`
	IsPatch          bool          `json:"is_patch"`
	Attachments      []*Attachment `json:"attachments,omitempty"`
	DateParsed       time.Time     `json:"-"`
}

func decodeHeader(hdr string) string {
	if hdr == "" {
		return ""
	}
	dec := new(mime.WordDecoder)
	decoded, err := dec.DecodeHeader(hdr)
	if err == nil {
		return decoded
	}
	return hdr
}

// ParseEmail parses raw RFC 5322 byte stream into a ParsedEmail struct.
func ParseEmail(raw []byte) (*ParsedEmail, error) {
	// Extract raw headers block (up to first \r\n\r\n or \n\n)
	var rawHeaders string
	if idx := bytes.Index(raw, []byte("\r\n\r\n")); idx != -1 {
		rawHeaders = string(raw[:idx])
	} else if idx := bytes.Index(raw, []byte("\n\n")); idx != -1 {
		rawHeaders = string(raw[:idx])
	} else {
		rawHeaders = string(raw)
	}

	env, err := enmime.ReadEnvelope(bytes.NewReader(raw))
	if err != nil {
		// Fallback to net/mail basic parser
		return parseBasicEmail(raw, rawHeaders)
	}

	parsed := &ParsedEmail{
		RawHeaders:       rawHeaders,
		MessageID:        CleanMsgID(env.GetHeader("Message-ID")),
		Subject:          decodeHeader(env.GetHeader("Subject")),
		Date:             env.GetHeader("Date"),
		InReplyTo:        env.GetHeader("In-Reply-To"),
		ReferencesHeader: env.GetHeader("References"),
		BodyPlain:        env.Text,
	}

	// Parse From
	fromHdr := env.GetHeader("From")
	if fromHdr != "" {
		if addrs, err := mail.ParseAddressList(fromHdr); err == nil && len(addrs) > 0 {
			parsed.FromName = decodeHeader(addrs[0].Name)
			parsed.FromAddr = addrs[0].Address
		} else {
			parsed.FromAddr = fromHdr
		}
	}

	// Parse To and Cc
	parsed.ToAddr = decodeHeader(env.GetHeader("To"))
	parsed.CcAddr = decodeHeader(env.GetHeader("Cc"))

	// Parse Timestamp
	if parsed.Date != "" {
		if t, err := mail.ParseDate(parsed.Date); err == nil {
			parsed.DateParsed = t
			parsed.EpochTimestamp = t.Unix()
		}
	}

	parsed.IsPatch = DetectPatch(parsed.BodyPlain)
	parsed.Attachments = ExtractAttachmentsFromEnvelope(env)

	return parsed, nil
}

// SanitizeAttachmentFileName strips dangerous characters and path traversal components,
// providing a clean fallback name if empty.
func SanitizeAttachmentFileName(name string, index int, contentType string) string {
	clean := strings.TrimSpace(name)
	clean = filepath.Base(clean)
	clean = strings.ReplaceAll(clean, "\r", "")
	clean = strings.ReplaceAll(clean, "\n", "")
	clean = strings.ReplaceAll(clean, `"`, "")
	clean = strings.ReplaceAll(clean, "/", "")
	clean = strings.ReplaceAll(clean, `\`, "")
	clean = strings.TrimSpace(clean)

	if clean == "." || clean == "" {
		ext := ""
		switch strings.ToLower(contentType) {
		case "text/x-diff", "text/x-patch":
			ext = ".patch"
		case "text/plain":
			ext = ".txt"
		case "application/pdf":
			ext = ".pdf"
		case "image/png":
			ext = ".png"
		case "image/jpeg":
			ext = ".jpg"
		case "application/gzip", "application/x-gzip":
			ext = ".tar.gz"
		case "application/zip":
			ext = ".zip"
		default:
			ext = ".bin"
		}
		clean = fmt.Sprintf("attachment-%d%s", index+1, ext)
	}
	return clean
}

// ExtractAttachments decodes and extracts all MIME attachments from raw RFC 5322 email bytes.
func ExtractAttachments(raw []byte) ([]*Attachment, error) {
	env, err := enmime.ReadEnvelope(bytes.NewReader(raw))
	if err != nil {
		return nil, err
	}
	return ExtractAttachmentsFromEnvelope(env), nil
}

// ExtractAttachmentsFromEnvelope collects attachments from an enmime.Envelope.
func ExtractAttachmentsFromEnvelope(env *enmime.Envelope) []*Attachment {
	if env == nil {
		return []*Attachment{}
	}

	var results []*Attachment
	seenPayloads := make(map[string]bool)

	addPart := func(p *enmime.Part) {
		if p == nil || len(p.Content) == 0 {
			return
		}
		hashKey := fmt.Sprintf("%s:%d:%s", p.FileName, len(p.Content), p.ContentType)
		if seenPayloads[hashKey] {
			return
		}
		seenPayloads[hashKey] = true

		cType := strings.ToLower(strings.TrimSpace(p.ContentType))
		if cType == "" {
			cType = "application/octet-stream"
		}

		idx := len(results)
		fileName := SanitizeAttachmentFileName(p.FileName, idx, cType)

		results = append(results, &Attachment{
			Index:       idx,
			FileName:    fileName,
			ContentType: cType,
			Size:        int64(len(p.Content)),
			Payload:     p.Content,
		})
	}

	// 1. Standard Attachments
	for _, p := range env.Attachments {
		addPart(p)
	}

	// 2. Inline attachments (patches, images, logs with filename or non-plain content)
	for _, p := range env.Inlines {
		if p.FileName != "" || (!strings.HasPrefix(p.ContentType, "text/plain") && !strings.HasPrefix(p.ContentType, "text/html")) {
			addPart(p)
		}
	}

	// 3. Other parts
	for _, p := range env.OtherParts {
		if p.FileName != "" || (!strings.HasPrefix(p.ContentType, "text/plain") && !strings.HasPrefix(p.ContentType, "text/html")) {
			addPart(p)
		}
	}

	if results == nil {
		results = []*Attachment{}
	}
	return results
}

func parseBasicEmail(raw []byte, rawHeaders string) (*ParsedEmail, error) {
	msg, err := mail.ReadMessage(bytes.NewReader(raw))
	if err != nil {
		return nil, err
	}

	parsed := &ParsedEmail{
		RawHeaders:       rawHeaders,
		MessageID:        CleanMsgID(msg.Header.Get("Message-ID")),
		Subject:          decodeHeader(msg.Header.Get("Subject")),
		Date:             msg.Header.Get("Date"),
		InReplyTo:        msg.Header.Get("In-Reply-To"),
		ReferencesHeader: msg.Header.Get("References"),
		ToAddr:           decodeHeader(msg.Header.Get("To")),
		CcAddr:           decodeHeader(msg.Header.Get("Cc")),
	}

	fromHdr := msg.Header.Get("From")
	if fromHdr != "" {
		if addrs, err := mail.ParseAddressList(fromHdr); err == nil && len(addrs) > 0 {
			parsed.FromName = decodeHeader(addrs[0].Name)
			parsed.FromAddr = addrs[0].Address
		} else {
			parsed.FromAddr = fromHdr
		}
	}

	if parsed.Date != "" {
		if t, err := mail.ParseDate(parsed.Date); err == nil {
			parsed.DateParsed = t
			parsed.EpochTimestamp = t.Unix()
		}
	}

	buf := new(bytes.Buffer)
	_, _ = buf.ReadFrom(msg.Body)
	parsed.BodyPlain = buf.String()
	parsed.IsPatch = DetectPatch(parsed.BodyPlain)
	parsed.Attachments = []*Attachment{}

	return parsed, nil
}

// QuoteDisplayName quotes an email display-name with double quotations according to RFC 5322
// if it contains special characters such as ',', '(', ')', '<', '>', '[', ']', ':', ';', '@', '\\', '.', '"'.
// If the name is already quoted or does not contain special characters, it is returned as is.
func QuoteDisplayName(name string) string {
	name = strings.TrimSpace(name)
	if name == "" {
		return ""
	}
	// If already properly enclosed in double quotes, preserve it
	if len(name) >= 2 && strings.HasPrefix(name, "\"") && strings.HasSuffix(name, "\"") {
		return name
	}
	// RFC 5322 Section 3.2.5 specials that cannot appear in an unquoted atom:
	// specials = ( ) < > [ ] : ; @ \ , . "
	const specials = "(),:;<>@[\\]\"."
	if strings.ContainsAny(name, specials) {
		escaped := strings.ReplaceAll(name, "\\", "\\\\")
		escaped = strings.ReplaceAll(escaped, "\"", "\\\"")
		return fmt.Sprintf("\"%s\"", escaped)
	}
	return name
}

// FormatAddress formats a display name and email address into an RFC 5322 address string,
// quoting the display-name with double quotations if it contains special characters such as ',' or '('.
func FormatAddress(name, email string) string {
	email = strings.Trim(strings.TrimSpace(email), "<>")
	name = strings.TrimSpace(name)
	if name == "" {
		if email == "" {
			return ""
		}
		return email
	}
	quotedName := QuoteDisplayName(name)
	return fmt.Sprintf("%s <%s>", quotedName, email)
}

// FormatAddressString formats an address string into an RFC 5322 address, quoting
// the display-name if it contains special characters such as ',' or '('.
func FormatAddressString(addr string) string {
	addr = strings.TrimSpace(addr)
	if addr == "" {
		return ""
	}
	if idx := strings.LastIndex(addr, "<"); idx > 0 && strings.HasSuffix(addr, ">") {
		name := strings.TrimSpace(addr[:idx])
		email := strings.Trim(addr[idx:], "<> ")
		return FormatAddress(name, email)
	}
	return addr
}

// hasAngleAhead checks whether there is an opening '<' before any closing '>' ahead in the runes.
func hasAngleAhead(runes []rune) bool {
	inQ := false
	for _, r := range runes {
		if r == '"' {
			inQ = !inQ
		} else if r == '<' && !inQ {
			return true
		} else if r == '>' && !inQ {
			return false
		}
	}
	return false
}

// SplitAddressList splits a comma-separated RFC 5322 address list while respecting
// double quotes, angle brackets, and unquoted display names containing commas.
func SplitAddressList(addrs string) []string {
	var parts []string
	var curr strings.Builder
	inQuotes := false
	inAngle := false
	escaped := false

	runes := []rune(addrs)
	for i := 0; i < len(runes); i++ {
		r := runes[i]
		if escaped {
			curr.WriteRune(r)
			escaped = false
			continue
		}
		if r == '\\' && inQuotes {
			curr.WriteRune(r)
			escaped = true
			continue
		}
		if r == '"' {
			inQuotes = !inQuotes
		} else if r == '<' && !inQuotes {
			inAngle = true
		} else if r == '>' && !inQuotes {
			inAngle = false
		} else if (r == ',' || r == ';') && !inQuotes && !inAngle {
			currStr := curr.String()
			hasCompleteAddr := (strings.Contains(currStr, "<") && strings.Contains(currStr, ">")) ||
				strings.Contains(currStr, "@")
			if !hasCompleteAddr && hasAngleAhead(runes[i+1:]) {
				curr.WriteRune(r)
				continue
			}

			if s := strings.TrimSpace(curr.String()); s != "" {
				parts = append(parts, s)
			}
			curr.Reset()
			continue
		}
		curr.WriteRune(r)
	}
	if s := strings.TrimSpace(curr.String()); s != "" {
		parts = append(parts, s)
	}
	return parts
}

// ParseAddressList parses an RFC 5322 address list, handling unquoted display-names
// containing commas or parentheses before angle-addr.
func ParseAddressList(header string) ([]*mail.Address, error) {
	header = strings.TrimSpace(header)
	if header == "" {
		return nil, nil
	}

	parts := SplitAddressList(header)
	var addrs []*mail.Address
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		if idx := strings.LastIndex(p, "<"); idx > 0 && strings.HasSuffix(p, ">") {
			name := strings.TrimSpace(p[:idx])
			email := strings.Trim(p[idx:], "<> ")
			name = strings.Trim(name, "\"")
			addrs = append(addrs, &mail.Address{
				Name:    name,
				Address: email,
			})
		} else if parsed, err := mail.ParseAddress(p); err == nil {
			addrs = append(addrs, parsed)
		} else if strings.Contains(p, "@") {
			clean := strings.Trim(p, "<> ")
			addrs = append(addrs, &mail.Address{
				Address: clean,
			})
		}
	}
	if len(addrs) == 0 {
		return nil, errors.New("mail: no valid addresses found")
	}
	return addrs, nil
}

// BuildReplyRecipients constructs To and Cc addresses for replying to all participants,
// excluding the sender's own email.
func BuildReplyRecipients(origFrom, origTo, origCc, myEmail string) (toAddr, ccAddr string) {
	myEmail = strings.ToLower(strings.TrimSpace(myEmail))

	// To address is original From, formatted with RFC 5322 quoting if needed
	toAddr = origFrom
	if toAddr != "" {
		toAddr = FormatAddressString(toAddr)
	}

	// Collect unique addresses for Cc
	seen := make(map[string]struct{})
	if toAddr != "" {
		if addrs, err := ParseAddressList(toAddr); err == nil {
			for _, a := range addrs {
				seen[strings.ToLower(a.Address)] = struct{}{}
			}
		} else {
			seen[strings.ToLower(toAddr)] = struct{}{}
		}
	}
	if myEmail != "" {
		seen[myEmail] = struct{}{}
	}

	var ccList []string
	addIfNew := func(addrStr string) {
		if addrStr == "" {
			return
		}
		addrs, err := ParseAddressList(addrStr)
		if err == nil {
			for _, a := range addrs {
				low := strings.ToLower(a.Address)
				if _, exists := seen[low]; !exists {
					seen[low] = struct{}{}
					if a.Name != "" {
						ccList = append(ccList, FormatAddress(a.Name, a.Address))
					} else {
						ccList = append(ccList, a.Address)
					}
				}
			}
		}
	}

	addIfNew(origTo)
	addIfNew(origCc)

	return toAddr, strings.Join(ccList, ", ")
}

// ExtractedAddress represents a parsed email address and optional display name.
type ExtractedAddress struct {
	Name    string `json:"name"`
	Address string `json:"address"`
}

// ExtractAddresses parses one or more RFC 5322 header strings (such as To or Cc)
// and extracts all valid, de-duplicated email addresses and names.
func ExtractAddresses(headers ...string) []ExtractedAddress {
	var results []ExtractedAddress
	seen := make(map[string]int) // address -> index in results

	for _, header := range headers {
		if strings.TrimSpace(header) == "" {
			continue
		}

		addrs, err := ParseAddressList(header)
		if err != nil {
			// Fallback: split by comma or newline and try parsing individually
			parts := strings.FieldsFunc(header, func(r rune) bool {
				return r == ',' || r == '\n' || r == '\r' || r == ';'
			})
			for _, p := range parts {
				p = strings.TrimSpace(p)
				if p == "" {
					continue
				}
				if parsed, err := mail.ParseAddress(p); err == nil {
					addrs = append(addrs, parsed)
				} else {
					cleaned := strings.Trim(p, "<>")
					if strings.Contains(cleaned, "@") {
						addrs = append(addrs, &mail.Address{
							Address: cleaned,
						})
					}
				}
			}
		}

		for _, addr := range addrs {
			cleanEmail := strings.ToLower(strings.TrimSpace(addr.Address))
			cleanEmail = strings.Trim(cleanEmail, "<>")
			if cleanEmail == "" || !strings.Contains(cleanEmail, "@") {
				continue
			}
			name := decodeHeader(strings.TrimSpace(addr.Name))
			name = strings.Trim(name, `"'`)

			if idx, exists := seen[cleanEmail]; exists {
				if name != "" && results[idx].Name == "" {
					results[idx].Name = name
				}
			} else {
				seen[cleanEmail] = len(results)
				results = append(results, ExtractedAddress{
					Name:    name,
					Address: cleanEmail,
				})
			}
		}
	}

	return results
}

