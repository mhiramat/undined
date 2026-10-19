// Package parser handles RFC 5322 and MIME email decoding for UndineD.
package parser

import (
	"bytes"
	"mime"
	"net/mail"
	"regexp"
	"strings"
	"time"

	"github.com/jhillyerd/enmime"
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

// DetectPatch checks if the text contains Linux kernel patch syntax.
func DetectPatch(text string) bool {
	if text == "" {
		return false
	}
	for _, re := range patchHeaderRegexes {
		if re.MatchString(text) {
			return true
		}
	}
	return false
}

// ParsedEmail represents extracted email metadata and plain-text content.
type ParsedEmail struct {
	MessageID        string    `json:"message_id"`
	Subject          string    `json:"subject"`
	FromName         string    `json:"from_name"`
	FromAddr         string    `json:"from_addr"`
	ToAddr           string    `json:"to_addr"`
	CcAddr           string    `json:"cc_addr"`
	Date             string    `json:"date"`
	EpochTimestamp   int64     `json:"epoch_timestamp"`
	InReplyTo        string    `json:"in_reply_to"`
	ReferencesHeader string    `json:"references_header"`
	BodyPlain        string    `json:"body_plain"`
	RawHeaders       string    `json:"raw_headers"`
	IsPatch          bool      `json:"is_patch"`
	DateParsed       time.Time `json:"-"`
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

	return parsed, nil
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

	return parsed, nil
}

// BuildReplyRecipients constructs To and Cc addresses for replying to all participants,
// excluding the sender's own email.
func BuildReplyRecipients(origFrom, origTo, origCc, myEmail string) (toAddr, ccAddr string) {
	myEmail = strings.ToLower(strings.TrimSpace(myEmail))

	// To address is original From
	toAddr = origFrom

	// Collect unique addresses for Cc
	seen := make(map[string]struct{})
	if toAddr != "" {
		if addrs, err := mail.ParseAddressList(toAddr); err == nil {
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
		addrs, err := mail.ParseAddressList(addrStr)
		if err == nil {
			for _, a := range addrs {
				low := strings.ToLower(a.Address)
				if _, exists := seen[low]; !exists {
					seen[low] = struct{}{}
					ccList = append(ccList, a.String())
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

		addrs, err := mail.ParseAddressList(header)
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

