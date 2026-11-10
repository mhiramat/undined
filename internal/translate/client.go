package translate

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"undined-go/internal/config"
	"undined-go/internal/constants"
)

// ErrServerUnreachable indicates that the local-jp-translate HTTP server is not running
// or could not be reached at the configured URL.
var ErrServerUnreachable = errors.New("local translation server is not running or unreachable")

const (
	// maxSingleChunkRunes bounds a single prose segment sent to POST /translate so the
	// generated translation never exceeds the 2048-token output limit (502 backend_truncated).
	maxSingleChunkRunes = 1200
)

// Options configures a translation request.
type Options struct {
	SourceLang string
	TargetLang string
	Style      string
	MaxTokens  int
}

// Result holds both the formatted (unwrapped) source email and its translated text.
type Result struct {
	FormattedBody     string `json:"formatted_body"`
	TranslatedBody    string `json:"translated_body"`
	TranslatedSubject string `json:"translated_subject,omitempty"`
	SourceLang        string `json:"source_lang"`
	TargetLang        string `json:"target_lang"`
	Model             string `json:"model,omitempty"`
}

type translateAPIRequest struct {
	Text      string `json:"text"`
	Source    string `json:"source,omitempty"`
	Target    string `json:"target,omitempty"`
	Style     string `json:"style,omitempty"`
	MaxTokens int    `json:"max_tokens,omitempty"`
}

type translateAPIResponse struct {
	TranslatedText    string `json:"translated_text"`
	TranslatedTextAlt string `json:"translatedText"`
	Model             string `json:"model"`
	DurationMs        int64  `json:"duration_ms"`
}

type translateAPIErrorResponse struct {
	Error struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

// Client communicates with the local-jp-translate REST API.
// Translation jobs are serialized through a FIFO queue so multi-paragraph emails
// never interleave requests against the local translation server.
type Client struct {
	Config     *config.Config
	HTTPClient *http.Client

	queueMu sync.Mutex
	waiters []chan struct{}
}

// NewClient creates a new local-jp-translate API client.
func NewClient(cfg *config.Config) *Client {
	timeout := constants.DefaultTranslateTimeoutSeconds
	if cfg != nil && cfg.Translate.TimeoutSeconds > 0 {
		timeout = cfg.Translate.TimeoutSeconds
	}
	return &Client{
		Config: cfg,
		HTTPClient: &http.Client{
			Timeout: time.Duration(timeout * float64(time.Second)),
		},
	}
}

// acquireQueueTurn waits in strict FIFO order for exclusive access to the translation worker.
func (c *Client) acquireQueueTurn(ctx context.Context) (func(), error) {
	if c == nil {
		return func() {}, nil
	}
	myTurn := make(chan struct{})
	c.queueMu.Lock()
	if len(c.waiters) == 0 {
		close(myTurn)
	}
	c.waiters = append(c.waiters, myTurn)
	c.queueMu.Unlock()

	release := func() {
		c.queueMu.Lock()
		defer c.queueMu.Unlock()
		for i, ch := range c.waiters {
			if ch == myTurn {
				c.waiters = append(c.waiters[:i], c.waiters[i+1:]...)
				break
			}
		}
		if i := 0; i < len(c.waiters) {
			// Wake up the new head of the FIFO queue if not already closed
			select {
			case <-c.waiters[0]:
			default:
				close(c.waiters[0])
			}
		}
	}

	select {
	case <-myTurn:
		return release, nil
	case <-ctx.Done():
		release()
		return nil, fmt.Errorf("translation request canceled while queued: %w", ctx.Err())
	}
}

func (c *Client) effectiveBaseURL() string {
	if c != nil && c.Config != nil && strings.TrimSpace(c.Config.Translate.URL) != "" {
		return strings.TrimRight(strings.TrimSpace(c.Config.Translate.URL), "/")
	}
	return constants.DefaultTranslateURL
}

func (c *Client) resolveOptions(opts Options) Options {
	res := opts
	if c != nil && c.Config != nil {
		if res.SourceLang == "" {
			res.SourceLang = strings.TrimSpace(c.Config.Translate.SourceLang)
		}
		if res.TargetLang == "" {
			res.TargetLang = strings.TrimSpace(c.Config.Translate.TargetLang)
		}
		if res.Style == "" {
			res.Style = strings.TrimSpace(c.Config.Translate.Style)
		}
		if res.MaxTokens <= 0 {
			res.MaxTokens = c.Config.Translate.MaxTokens
		}
	}
	if res.SourceLang == "" {
		res.SourceLang = constants.DefaultTranslateSourceLang
	}
	if res.TargetLang == "" {
		res.TargetLang = constants.DefaultTranslateTargetLang
	}
	if res.Style == "" {
		res.Style = constants.DefaultTranslateStyle
	}
	if res.MaxTokens <= 0 {
		res.MaxTokens = constants.DefaultTranslateMaxTokens
	}
	if res.MaxTokens > 2048 {
		res.MaxTokens = 2048
	}
	return res
}

// TranslateText translates a single plain-text string using POST /translate on local-jp-translate.
func (c *Client) TranslateText(ctx context.Context, text string, opts Options) (string, string, error) {
	trimmed := strings.TrimSpace(text)
	if trimmed == "" {
		return text, "", nil
	}

	resolved := c.resolveOptions(opts)
	chunks := splitProseIntoChunks(trimmed, maxSingleChunkRunes)

	translatedChunks := make([]string, 0, len(chunks))
	var modelName string

	for _, chunk := range chunks {
		out, model, err := c.callTranslateEndpoint(ctx, chunk, resolved)
		if err != nil {
			return "", "", err
		}
		if model != "" {
			modelName = model
		}
		translatedChunks = append(translatedChunks, strings.TrimSpace(out))
	}

	return strings.Join(translatedChunks, " "), modelName, nil
}

func (c *Client) callTranslateEndpoint(ctx context.Context, text string, opts Options) (string, string, error) {
	baseURL := c.effectiveBaseURL()
	endpoint := baseURL + "/translate"

	reqPayload := translateAPIRequest{
		Text:      text,
		Source:    opts.SourceLang,
		Target:    opts.TargetLang,
		MaxTokens: opts.MaxTokens,
	}
	// local-jp-translate only accepts "style" when target is "ja"; otherwise it returns 400 style_target_mismatch.
	if strings.EqualFold(opts.TargetLang, "ja") && opts.Style != "" {
		reqPayload.Style = opts.Style
	}

	bodyBytes, err := json.Marshal(reqPayload)
	if err != nil {
		return "", "", fmt.Errorf("failed to marshal translate request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(bodyBytes))
	if err != nil {
		return "", "", fmt.Errorf("failed to create translate request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	httpClient := c.HTTPClient
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 180 * time.Second}
	}

	resp, err := httpClient.Do(req)
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return "", "", fmt.Errorf("translation request timed out or was canceled: %w", err)
		}
		return "", "", fmt.Errorf("%w at %s: %v", ErrServerUnreachable, baseURL, err)
	}
	defer resp.Body.Close()

	respBytes, err := io.ReadAll(io.LimitReader(resp.Body, 2*1024*1024))
	if err != nil {
		return "", "", fmt.Errorf("failed to read response from translation server: %w", err)
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		var errResp translateAPIErrorResponse
		if jsonErr := json.Unmarshal(respBytes, &errResp); jsonErr == nil && errResp.Error.Message != "" {
			if resp.StatusCode == http.StatusServiceUnavailable && errResp.Error.Code == "not_ready" {
				return "", "", fmt.Errorf("%w: server at %s is not ready (%s)", ErrServerUnreachable, baseURL, errResp.Error.Message)
			}
			return "", "", fmt.Errorf("translation server error (%s): %s", errResp.Error.Code, errResp.Error.Message)
		}
		return "", "", fmt.Errorf("translation server returned HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(respBytes)))
	}

	var okResp translateAPIResponse
	if err := json.Unmarshal(respBytes, &okResp); err != nil {
		return "", "", fmt.Errorf("invalid JSON response from translation server: %w", err)
	}

	translated := okResp.TranslatedText
	if translated == "" {
		translated = okResp.TranslatedTextAlt
	}
	return translated, okResp.Model, nil
}

// TranslateEmail formats a raw email body (unwrapping 80-column hard-wrapped lines and
// grouping '>' quoted blocks) and translates all prose segments while keeping code blocks,
// commit trailers, signatures, and unified diffs intact.
// Concurrent TranslateEmail requests are queued in FIFO order so paragraphs from different
// emails do not interleave on the local translation server.
func (c *Client) TranslateEmail(ctx context.Context, subject string, rawBody string, opts Options) (*Result, error) {
	release, err := c.acquireQueueTurn(ctx)
	if err != nil {
		return nil, err
	}
	defer release()

	resolved := c.resolveOptions(opts)
	segments := ParseEmailSegments(rawBody)
	formattedBody := RenderSegments(segments)

	res := &Result{
		FormattedBody: formattedBody,
		SourceLang:    resolved.SourceLang,
		TargetLang:    resolved.TargetLang,
	}

	// Translate subject if non-empty (preserving leading [PATCH ...] prefix verbatim)
	if strings.TrimSpace(subject) != "" {
		prefix, cleanSubj := splitPatchSubjectPrefix(subject)
		if strings.TrimSpace(cleanSubj) != "" {
			transSubj, model, err := c.TranslateText(ctx, cleanSubj, resolved)
			if err != nil {
				return nil, err
			}
			if model != "" {
				res.Model = model
			}
			if prefix != "" {
				res.TranslatedSubject = prefix + " " + transSubj
			} else {
				res.TranslatedSubject = transSubj
			}
		} else {
			res.TranslatedSubject = subject
		}
	}

	if len(segments) == 0 {
		return res, nil
	}

	translatedSegments := make([]Segment, len(segments))
	copy(translatedSegments, segments)

	for i, seg := range segments {
		if seg.Kind != SegmentProse || strings.TrimSpace(seg.Text) == "" {
			continue
		}

		// Preserve list marker (e.g. "- ", "* ", "1. ") so translated list items keep their bullet
		bulletPrefix, proseContent := extractListMarker(seg.Text)
		if strings.TrimSpace(proseContent) == "" {
			continue
		}

		translatedText, model, err := c.TranslateText(ctx, proseContent, resolved)
		if err != nil {
			return nil, err
		}
		if model != "" {
			res.Model = model
		}
		translatedSegments[i].Text = bulletPrefix + translatedText
	}

	res.TranslatedBody = RenderSegments(translatedSegments)
	return res, nil
}

func extractListMarker(text string) (marker string, rest string) {
	if m := listBulletRe.FindStringSubmatch(text); len(m) == 3 {
		return m[1], m[2]
	}
	return "", text
}

func splitPatchSubjectPrefix(subject string) (prefix string, rest string) {
	s := strings.TrimSpace(subject)
	// Handle optional Re: / Fwd: before [PATCH ...]
	rePrefix := ""
	for {
		lower := strings.ToLower(s)
		if strings.HasPrefix(lower, "re:") {
			rePrefix += s[:3] + " "
			s = strings.TrimSpace(s[3:])
		} else if strings.HasPrefix(lower, "fwd:") {
			rePrefix += s[:4] + " "
			s = strings.TrimSpace(s[4:])
		} else {
			break
		}
	}
	if strings.HasPrefix(s, "[") {
		if closeIdx := strings.Index(s, "]"); closeIdx > 0 {
			bracket := s[:closeIdx+1]
			remainder := strings.TrimSpace(s[closeIdx+1:])
			return strings.TrimSpace(rePrefix + bracket), remainder
		}
	}
	return strings.TrimSpace(rePrefix), s
}

// splitProseIntoChunks splits a long unwrapped paragraph at sentence boundaries (". ", "? ", "! ")
// if its rune length exceeds maxRunes.
func splitProseIntoChunks(text string, maxRunes int) []string {
	runes := []rune(text)
	if len(runes) <= maxRunes || maxRunes <= 0 {
		return []string{text}
	}

	var chunks []string
	for len(runes) > maxRunes {
		splitIdx := -1
		// Search backwards from maxRunes for a sentence boundary ". ", "? ", "! "
		for i := maxRunes; i >= maxRunes/3; i-- {
			if i < len(runes) && runes[i] == ' ' && i > 0 {
				prev := runes[i-1]
				if prev == '.' || prev == '?' || prev == '!' {
					splitIdx = i
					break
				}
			}
		}
		// Fallback to any space boundary
		if splitIdx == -1 {
			for i := maxRunes; i >= maxRunes/3; i-- {
				if i < len(runes) && runes[i] == ' ' {
					splitIdx = i
					break
				}
			}
		}
		if splitIdx == -1 {
			splitIdx = maxRunes
		}

		chunk := strings.TrimSpace(string(runes[:splitIdx]))
		if chunk != "" {
			chunks = append(chunks, chunk)
		}
		runes = []rune(strings.TrimSpace(string(runes[splitIdx:])))
	}
	if len(runes) > 0 {
		chunks = append(chunks, string(runes))
	}
	return chunks
}
