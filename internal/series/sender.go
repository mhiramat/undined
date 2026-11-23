// Package series manages patch series draft queues, validation, and human-initiated
// sequential SMTP transmission with pacing, pause/cancel, and duplicate-free resume.
package series

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"undined-go/internal/config"
	"undined-go/internal/constants"
	"undined-go/internal/events"
	"undined-go/internal/smtp"
	"undined-go/internal/storage/database"
	"undined-go/internal/storage/maildir"
	"undined-go/internal/storage/parser"
)

var (
	// ErrNotFound is returned when a requested series_id does not exist.
	ErrNotFound = errors.New("patch series not found")
	// ErrItemNotFound is returned when a requested seq_index does not exist in a series.
	ErrItemNotFound = errors.New("patch series item not found")
	// ErrAlreadySending is returned when a send or mutation is already active for the given series_id.
	ErrAlreadySending = errors.New("patch series is currently busy or sending")
	// ErrAlreadySent is returned when trying to edit or re-send an already sent item/series.
	ErrAlreadySent = errors.New("patch series or item has already been sent")
	// ErrInvalidInput is returned when a caller supplies invalid parameters.
	ErrInvalidInput = errors.New("invalid patch series input")

	hunkHeaderRe = regexp.MustCompile(`^@@ -(\d+)(?:,(\d+))? \+(\d+)(?:,(\d+))? @@`)
)

// ValidationWarning represents a non-blocking pre-send or edit-time sanity check warning.
type ValidationWarning struct {
	SeqIndex int    `json:"seq_index"`
	Code     string `json:"code"`
	Message  string `json:"message"`
}

// ItemDetail combines database metadata with the live draft body from Maildir.
type ItemDetail struct {
	*database.PatchSeriesItem
	Body     string `json:"body"`
	FilePath string `json:"file_path,omitempty"`
}

// Detail represents the full response payload for GET /api/series/{id}.
type Detail struct {
	*database.PatchSeriesDraft
	Items        []*ItemDetail       `json:"items"`
	Warnings     []ValidationWarning `json:"warnings"`
	SentCount    int                 `json:"sent_count"`
	TotalCount   int                 `json:"total_count"`
	AllToAddrs   []string            `json:"all_to_addrs"`
	AllCcAddrs   []string            `json:"all_cc_addrs"`
	FromEmail    string              `json:"from_email"`
	SMTPHost     string              `json:"smtp_host"`
	IsSendingNow bool                `json:"is_sending_now"`
}

// Manager coordinates patch series draft mutations and sequential SMTP sending.
type Manager struct {
	Config  *config.Config
	DB      *database.Database
	Maildir *maildir.MaildirStore
	SMTP    *smtp.Client
	Events  *events.Hub

	mu            sync.Mutex
	activeCancels map[string]context.CancelFunc
	mutating      map[string]bool
	wg            sync.WaitGroup

	// delayOverride is an optional hook for unit tests to accelerate pacing waits.
	delayOverride *time.Duration
}

// NewManager creates a new patch series Manager.
func NewManager(cfg *config.Config, db *database.Database, store *maildir.MaildirStore, smtpClient *smtp.Client, hub *events.Hub) *Manager {
	return &Manager{
		Config:        cfg,
		DB:            db,
		Maildir:       store,
		SMTP:          smtpClient,
		Events:        hub,
		activeCancels: make(map[string]context.CancelFunc),
		mutating:      make(map[string]bool),
	}
}

func (m *Manager) acquireMutation(seriesID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, sending := m.activeCancels[seriesID]; sending || m.mutating[seriesID] {
		return ErrAlreadySending
	}
	m.mutating[seriesID] = true
	return nil
}

func (m *Manager) releaseMutation(seriesID string) {
	m.mu.Lock()
	delete(m.mutating, seriesID)
	m.mu.Unlock()
}

// Shutdown cancels all active background series sends and waits for them to stop cleanly.
func (m *Manager) Shutdown() {
	m.mu.Lock()
	cancels := make([]context.CancelFunc, 0, len(m.activeCancels))
	for _, cancel := range m.activeCancels {
		if cancel != nil {
			cancels = append(cancels, cancel)
		}
	}
	m.mu.Unlock()
	for _, cancel := range cancels {
		cancel()
	}
	m.wg.Wait()
}

// SetEvents updates the SSE Event Hub used for series progress broadcasts.
func (m *Manager) SetEvents(hub *events.Hub) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.Events = hub
}

func (m *Manager) publishEvent(eventType string, payload map[string]any) {
	m.mu.Lock()
	hub := m.Events
	m.mu.Unlock()
	if hub != nil {
		hub.Publish(eventType, payload)
	}
}

// IsSending reports whether a send goroutine is currently active for seriesID.
func (m *Manager) IsSending(seriesID string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	_, ok := m.activeCancels[seriesID]
	return ok
}

// Wait blocks until all active background send goroutines have exited (useful in tests).
func (m *Manager) Wait() {
	m.wg.Wait()
}

// StripSeriesHeaders removes internal X-Undine-Series-* tracking headers from a raw RFC 5322 email
// while leaving all other headers and the body byte-for-byte intact.
func StripSeriesHeaders(raw string) string {
	normalized := strings.ReplaceAll(raw, "\r\n", "\n")
	sepIdx := strings.Index(normalized, "\n\n")
	if sepIdx == -1 {
		return raw
	}
	headerPart := normalized[:sepIdx]
	bodyPart := normalized[sepIdx+2:]

	headerLines := strings.Split(headerPart, "\n")
	kept := make([]string, 0, len(headerLines))
	skippingContinuation := false
	for _, line := range headerLines {
		if len(line) > 0 && (line[0] == ' ' || line[0] == '\t') {
			if skippingContinuation {
				continue
			}
			kept = append(kept, line)
			continue
		}
		lower := strings.ToLower(line)
		if strings.HasPrefix(lower, "x-undine-series-") {
			skippingContinuation = true
			continue
		}
		skippingContinuation = false
		kept = append(kept, line)
	}
	return strings.Join(kept, "\r\n") + "\r\n\r\n" + bodyPart
}

// ValidatePatchDiff performs lightweight, non-blocking sanity checks on a non-cover-letter patch body.
func ValidatePatchDiff(seqIndex int, body string) []ValidationWarning {
	warnings := make([]ValidationWarning, 0)
	normalized := strings.ReplaceAll(body, "\r\n", "\n")
	lines := strings.Split(normalized, "\n")

	if !strings.Contains(normalized, "Signed-off-by:") {
		warnings = append(warnings, ValidationWarning{
			SeqIndex: seqIndex,
			Code:     "missing_signed_off_by",
			Message:  fmt.Sprintf("Patch %d is missing a Signed-off-by: trailer.", seqIndex),
		})
	}

	inDiff := false
	inHunk := false
	expectedOld, expectedNew := 0, 0
	actualOld, actualNew := 0, 0
	hunkBroken := false
	spacesIndentWarned := false
	currentFile := ""

	checkHunk := func() {
		if inHunk && (actualOld != expectedOld || actualNew != expectedNew) {
			hunkBroken = true
		}
		inHunk = false
	}

	for _, line := range lines {
		if strings.HasPrefix(line, "diff --git ") {
			checkHunk()
			inDiff = true
			parts := strings.Fields(line)
			if len(parts) >= 4 {
				currentFile = strings.TrimPrefix(parts[3], "b/")
			}
			continue
		}
		if !inDiff {
			continue
		}
		if matches := hunkHeaderRe.FindStringSubmatch(line); matches != nil {
			checkHunk()
			expectedOld = 1
			if matches[2] != "" {
				// Regex capture (\d+) guarantees digits; error is unreachable for valid hunk lengths.
				expectedOld, _ = strconv.Atoi(matches[2])
			}
			expectedNew = 1
			if matches[4] != "" {
				// Regex capture (\d+) guarantees digits; error is unreachable for valid hunk lengths.
				expectedNew, _ = strconv.Atoi(matches[4])
			}
			actualOld, actualNew = 0, 0
			inHunk = true
			continue
		}
		if !inHunk {
			continue
		}
		if line == "-- " || strings.HasPrefix(line, "--- ") || strings.HasPrefix(line, "+++ ") {
			checkHunk()
			continue
		}
		if strings.HasPrefix(line, `\ No newline at end of file`) {
			continue
		}
		if strings.HasPrefix(line, "+") {
			actualNew++
			if !spacesIndentWarned && (strings.HasSuffix(currentFile, ".c") || strings.HasSuffix(currentFile, ".h") || strings.HasSuffix(currentFile, "Makefile")) {
				if strings.HasPrefix(line, "+        ") {
					spacesIndentWarned = true
				}
			}
		} else if strings.HasPrefix(line, "-") {
			actualOld++
		} else if strings.HasPrefix(line, " ") || line == "" {
			actualOld++
			actualNew++
		} else {
			checkHunk()
		}
	}
	checkHunk()

	if hunkBroken {
		warnings = append(warnings, ValidationWarning{
			SeqIndex: seqIndex,
			Code:     "diff_hunk_mismatch",
			Message:  fmt.Sprintf("Patch %d has unified diff hunk line counts that do not match its @@ header.", seqIndex),
		})
	}
	if spacesIndentWarned {
		warnings = append(warnings, ValidationWarning{
			SeqIndex: seqIndex,
			Code:     "spaces_instead_of_tabs",
			Message:  fmt.Sprintf("Patch %d contains added lines indented with 8 spaces instead of tabs.", seqIndex),
		})
	}
	return warnings
}

func mergeUniqueAddrs(lists ...[]string) []string {
	seen := make(map[string]struct{})
	out := make([]string, 0)
	for _, list := range lists {
		for _, raw := range list {
			trimmed := strings.TrimSpace(raw)
			if trimmed == "" {
				continue
			}
			key := strings.ToLower(trimmed)
			if _, ok := seen[key]; !ok {
				seen[key] = struct{}{}
				out = append(out, trimmed)
			}
		}
	}
	return out
}

func extractEnvelopeRecipients(toAddrs, ccAddrs []string) []string {
	merged := mergeUniqueAddrs(toAddrs, ccAddrs)
	seen := make(map[string]struct{}, len(merged))
	rcpts := make([]string, 0, len(merged))
	for _, raw := range merged {
		for _, part := range parser.SplitAddressList(raw) {
			ids := parser.ExtractMsgIDs(part)
			addr := ""
			if len(ids) > 0 {
				addr = ids[0]
			} else {
				addr = strings.TrimSpace(part)
			}
			if addr == "" {
				continue
			}
			key := strings.ToLower(addr)
			if _, ok := seen[key]; !ok {
				seen[key] = struct{}{}
				rcpts = append(rcpts, addr)
			}
		}
	}
	return rcpts
}

func extractBodyFromRawEmail(raw string) string {
	normalized := strings.ReplaceAll(raw, "\r\n", "\n")
	if idx := strings.Index(normalized, "\n\n"); idx != -1 {
		return normalized[idx+2:]
	}
	return normalized
}

func (m *Manager) loadDraftMessage(item *database.PatchSeriesItem) (*database.Message, string) {
	if item == nil || m.DB == nil {
		return nil, ""
	}
	if idVal, err := strconv.ParseInt(item.DraftMsgID, 10, 64); err == nil {
		if msg, err := m.DB.GetMessageByID(idVal); err == nil && msg != nil {
			if msg.FilePath != "" {
				if rawBytes, readErr := os.ReadFile(msg.FilePath); readErr == nil {
					return msg, extractBodyFromRawEmail(string(rawBytes))
				}
			}
			return msg, msg.BodyPlain
		}
	}
	return nil, ""
}

// LoadDetail loads full series metadata, item bodies, recipients, and validation warnings.
func (m *Manager) LoadDetail(seriesID string) (*Detail, error) {
	series, err := m.DB.GetPatchSeriesDraft(seriesID)
	if err != nil {
		return nil, err
	}
	if series == nil {
		return nil, ErrNotFound
	}

	details := make([]*ItemDetail, 0, len(series.Items))
	warnings := make([]ValidationWarning, 0)
	allTo := mergeUniqueAddrs(series.ToAddrs)
	allCc := mergeUniqueAddrs(series.CcAddrs)
	sentCount := 0

	for _, item := range series.Items {
		if item.Status == database.SeriesItemStatusSent {
			sentCount++
		}
		allTo = mergeUniqueAddrs(allTo, item.ExtraTo)
		allCc = mergeUniqueAddrs(allCc, item.ExtraCc)

		msg, body := m.loadDraftMessage(item)
		fp := ""
		if msg != nil {
			fp = msg.FilePath
		}
		details = append(details, &ItemDetail{
			PatchSeriesItem: item,
			Body:            body,
			FilePath:        fp,
		})
		if !item.IsCoverLetter && item.SeqIndex >= 1 && body != "" {
			warnings = append(warnings, ValidatePatchDiff(item.SeqIndex, body)...)
		}
	}

	fromEmail := constants.DefaultFromEmail
	smtpHost := ""
	if m.Config != nil {
		if m.Config.SMTP.FromEmail != "" {
			fromEmail = m.Config.SMTP.FromEmail
		}
		smtpHost = m.Config.SMTP.Host
	}

	return &Detail{
		PatchSeriesDraft: series,
		Items:            details,
		Warnings:         warnings,
		SentCount:        sentCount,
		TotalCount:       len(series.Items),
		AllToAddrs:       allTo,
		AllCcAddrs:       allCc,
		FromEmail:        fromEmail,
		SMTPHost:         smtpHost,
		IsSendingNow:     m.IsSending(seriesID),
	}, nil
}

func (m *Manager) buildItemRawEmail(series *database.PatchSeriesDraft, item *database.PatchSeriesItem, rootMsgID string, totalCount int, body string, includeTrackingHeaders bool) string {
	cleanedParent := parser.CleanMsgID(series.InReplyTo)
	var inReplyTo, references string
	if item.RFCMessageID == rootMsgID {
		if cleanedParent != "" {
			inReplyTo = "<" + cleanedParent + ">"
			references = "<" + cleanedParent + ">"
		}
	} else {
		inReplyTo = rootMsgID
		if cleanedParent != "" {
			references = fmt.Sprintf("<%s> %s", cleanedParent, rootMsgID)
		} else {
			references = rootMsgID
		}
	}

	effTo := mergeUniqueAddrs(series.ToAddrs, item.ExtraTo)
	effCc := mergeUniqueAddrs(series.CcAddrs, item.ExtraCc)

	var extraHeaders []smtp.HeaderField
	if includeTrackingHeaders {
		extraHeaders = []smtp.HeaderField{
			{Name: "X-Undine-Series-ID", Value: series.SeriesID},
			{Name: "X-Undine-Series-Index", Value: strconv.Itoa(item.SeqIndex)},
			{Name: "X-Undine-Series-Total", Value: strconv.Itoa(totalCount)},
		}
	}

	smtpClient := m.SMTP
	if smtpClient == nil {
		smtpClient = smtp.NewClient(m.Config, m.Maildir, m.DB)
	}
	raw, _ := smtpClient.BuildPlainTextMessage(smtp.PlainTextMessageOptions{
		To:           strings.Join(effTo, ", "),
		Cc:           strings.Join(effCc, ", "),
		Subject:      item.Subject,
		Body:         body,
		InReplyTo:    inReplyTo,
		References:   references,
		MessageID:    item.RFCMessageID,
		ExtraHeaders: extraHeaders,
	})
	return raw
}

func (m *Manager) rewriteDraftItemFile(series *database.PatchSeriesDraft, item *database.PatchSeriesItem, rootMsgID string, totalCount int, body string) error {
	raw := m.buildItemRawEmail(series, item, rootMsgID, totalCount, body, true)
	msg, _ := m.loadDraftMessage(item)
	if msg != nil && msg.FilePath != "" {
		if err := os.WriteFile(msg.FilePath, []byte(raw), 0600); err != nil {
			return fmt.Errorf("write draft file %s: %w", msg.FilePath, err)
		}
		if _, err := m.DB.IndexEmailFile(msg.FolderID, msg.FilePath); err != nil {
			return fmt.Errorf("reindex draft file %s: %w", msg.FilePath, err)
		}
		return nil
	}

	draftFolder, err := m.DB.GetOrCreateFolder(constants.FolderDrafts, false)
	if err != nil {
		return fmt.Errorf("get Drafts folder: %w", err)
	}
	savedPath, err := m.Maildir.AddMessage(constants.FolderDrafts, []byte(raw), constants.FlagDraft)
	if err != nil {
		return fmt.Errorf("add draft file: %w", err)
	}
	dbID, err := m.DB.IndexEmailFile(draftFolder.ID, savedPath)
	if err != nil {
		return fmt.Errorf("index new draft file: %w", err)
	}
	newDraftID := strconv.FormatInt(dbID, 10)
	if err := m.DB.UpdatePatchSeriesItemDraftMsgID(series.SeriesID, item.SeqIndex, newDraftID); err != nil {
		return err
	}
	item.DraftMsgID = newDraftID
	return nil
}

// UpdateHeaders bulk-updates shared To, Cc, In-Reply-To, and send_delay_sec across all unsent items.
func (m *Manager) UpdateHeaders(seriesID string, toAddrs, ccAddrs []string, inReplyTo string, sendDelaySec int) (*Detail, error) {
	if err := m.acquireMutation(seriesID); err != nil {
		return nil, err
	}
	defer m.releaseMutation(seriesID)

	series, err := m.DB.GetPatchSeriesDraft(seriesID)
	if err != nil {
		return nil, err
	}
	if series == nil {
		return nil, ErrNotFound
	}
	if len(toAddrs) == 0 {
		return nil, fmt.Errorf("%w: to_addrs must contain at least one recipient", ErrInvalidInput)
	}

	if err := m.DB.UpdatePatchSeriesHeaders(seriesID, toAddrs, ccAddrs, strings.TrimSpace(inReplyTo), sendDelaySec); err != nil {
		return nil, err
	}

	updated, err := m.DB.GetPatchSeriesDraft(seriesID)
	if err != nil {
		return nil, err
	}
	rootMsgID := ""
	if len(updated.Items) > 0 {
		rootMsgID = updated.Items[0].RFCMessageID
	}

	for _, item := range updated.Items {
		if item.Status == database.SeriesItemStatusSent {
			continue
		}
		_, body := m.loadDraftMessage(item)
		if err := m.rewriteDraftItemFile(updated, item, rootMsgID, len(updated.Items), body); err != nil {
			return nil, err
		}
	}

	m.publishEvent("series_draft_updated", map[string]any{
		"series_id":  seriesID,
		"series_key": updated.SeriesKey,
		"status":     updated.Status,
	})
	return m.LoadDetail(seriesID)
}

// UpdateItem saves human edits to a single unsent cover letter or patch item verbatim.
func (m *Manager) UpdateItem(seriesID string, seqIndex int, subject, body string, extraTo, extraCc []string) (*ItemDetail, []ValidationWarning, error) {
	if err := m.acquireMutation(seriesID); err != nil {
		return nil, nil, err
	}
	defer m.releaseMutation(seriesID)

	series, err := m.DB.GetPatchSeriesDraft(seriesID)
	if err != nil {
		return nil, nil, err
	}
	if series == nil {
		return nil, nil, ErrNotFound
	}

	var target *database.PatchSeriesItem
	for _, it := range series.Items {
		if it.SeqIndex == seqIndex {
			target = it
			break
		}
	}
	if target == nil {
		return nil, nil, ErrItemNotFound
	}
	if target.Status == database.SeriesItemStatusSent {
		return nil, nil, ErrAlreadySent
	}

	cleanSubj := strings.TrimSpace(subject)
	if cleanSubj == "" {
		cleanSubj = target.Subject
	}

	if err := m.DB.UpdatePatchSeriesItemMeta(seriesID, seqIndex, cleanSubj, extraTo, extraCc); err != nil {
		return nil, nil, err
	}
	target.Subject = cleanSubj
	if extraTo == nil {
		extraTo = make([]string, 0)
	}
	if extraCc == nil {
		extraCc = make([]string, 0)
	}
	target.ExtraTo = extraTo
	target.ExtraCc = extraCc

	rootMsgID := ""
	if len(series.Items) > 0 {
		rootMsgID = series.Items[0].RFCMessageID
	}
	if err := m.rewriteDraftItemFile(series, target, rootMsgID, len(series.Items), body); err != nil {
		return nil, nil, err
	}

	warnings := make([]ValidationWarning, 0)
	if !target.IsCoverLetter && target.SeqIndex >= 1 {
		warnings = ValidatePatchDiff(target.SeqIndex, body)
	}

	m.publishEvent("series_draft_updated", map[string]any{
		"series_id":  seriesID,
		"series_key": series.SeriesKey,
		"seq_index":  seqIndex,
		"status":     series.Status,
	})

	msg, savedBody := m.loadDraftMessage(target)
	fp := ""
	if msg != nil {
		fp = msg.FilePath
	}
	return &ItemDetail{
		PatchSeriesItem: target,
		Body:            savedBody,
		FilePath:        fp,
	}, warnings, nil
}

// DiscardSeries removes an unsent series queue and deletes its remaining Maildir Drafts files.
func (m *Manager) DiscardSeries(seriesID string) error {
	if err := m.acquireMutation(seriesID); err != nil {
		return err
	}
	defer m.releaseMutation(seriesID)

	series, err := m.DB.GetPatchSeriesDraft(seriesID)
	if err != nil {
		return err
	}
	if series == nil {
		return ErrNotFound
	}

	for _, item := range series.Items {
		if item.Status == database.SeriesItemStatusSent {
			continue
		}
		if idVal, parseErr := strconv.ParseInt(item.DraftMsgID, 10, 64); parseErr == nil {
			if msg, getErr := m.DB.GetMessageByID(idVal); getErr == nil && msg != nil {
				if msg.FilePath != "" {
					if rmErr := os.Remove(msg.FilePath); rmErr != nil && !os.IsNotExist(rmErr) {
						return fmt.Errorf("remove draft file %s: %w", msg.FilePath, rmErr)
					}
				}
				if delErr := m.DB.DeleteMessage(msg.ID); delErr != nil {
					return fmt.Errorf("delete draft message %d: %w", msg.ID, delErr)
				}
			}
		}
	}

	if err := m.DB.DeletePatchSeriesDraft(seriesID); err != nil {
		return err
	}

	m.publishEvent("series_draft_updated", map[string]any{
		"series_id":  seriesID,
		"series_key": series.SeriesKey,
		"deleted":    true,
	})
	return nil
}

// TestSendSelf transmits the series only to smtp.from_email with [TEST] subject prefix
// without marking any items as sent or deleting Drafts files.
func (m *Manager) TestSendSelf(seriesID string) (int, error) {
	if m.SMTP == nil {
		return 0, errors.New("SMTP client not configured")
	}
	fromEmail := ""
	if m.Config != nil {
		fromEmail = strings.TrimSpace(m.Config.SMTP.FromEmail)
	}
	if fromEmail == "" {
		return 0, errors.New("smtp.from_email is not configured")
	}

	series, err := m.DB.GetPatchSeriesDraft(seriesID)
	if err != nil {
		return 0, err
	}
	if series == nil {
		return 0, ErrNotFound
	}

	rootMsgID := ""
	if len(series.Items) > 0 {
		rootMsgID = series.Items[0].RFCMessageID
	}

	sentCount := 0
	for _, item := range series.Items {
		_, body := m.loadDraftMessage(item)
		testItem := *item
		if !strings.HasPrefix(testItem.Subject, "[TEST]") {
			testItem.Subject = "[TEST] " + testItem.Subject
		}
		testSeries := *series
		testSeries.ToAddrs = []string{fromEmail}
		testSeries.CcAddrs = nil
		testItem.ExtraTo = nil
		testItem.ExtraCc = nil

		raw := m.buildItemRawEmail(&testSeries, &testItem, rootMsgID, len(series.Items), body, false)
		if _, _, err := m.SMTP.SendRawMessage(raw, []string{fromEmail}); err != nil {
			return sentCount, fmt.Errorf("test send item %d failed: %w", item.SeqIndex, err)
		}
		sentCount++
	}
	return sentCount, nil
}

// StartSend starts or resumes sequential human-initiated SMTP transmission for remaining unsent items.
func (m *Manager) StartSend(seriesID string) error {
	if m.SMTP == nil {
		return fmt.Errorf("%w: SMTP client not configured", ErrInvalidInput)
	}

	m.mu.Lock()
	if _, active := m.activeCancels[seriesID]; active || m.mutating[seriesID] {
		m.mu.Unlock()
		return ErrAlreadySending
	}
	series, err := m.DB.GetPatchSeriesDraft(seriesID)
	if err != nil {
		m.mu.Unlock()
		return err
	}
	if series == nil {
		m.mu.Unlock()
		return ErrNotFound
	}

	hasUnsent := false
	for _, item := range series.Items {
		if item.Status != database.SeriesItemStatusSent {
			hasUnsent = true
			break
		}
	}
	if !hasUnsent {
		m.mu.Unlock()
		return ErrAlreadySent
	}

	ctx, cancel := context.WithCancel(context.Background())
	m.activeCancels[seriesID] = cancel
	m.wg.Add(1)
	m.mu.Unlock()

	if err := m.DB.UpdatePatchSeriesStatus(seriesID, database.SeriesStatusSending); err != nil {
		m.mu.Lock()
		delete(m.activeCancels, seriesID)
		m.mu.Unlock()
		cancel()
		m.wg.Done()
		return err
	}

	go m.runSequentialSend(ctx, seriesID)
	return nil
}

// CancelSend signals an active sequential send for seriesID to pause cleanly after the current item.
func (m *Manager) CancelSend(seriesID string) bool {
	m.mu.Lock()
	cancel, ok := m.activeCancels[seriesID]
	m.mu.Unlock()
	if ok && cancel != nil {
		cancel()
		return true
	}
	return false
}

func (m *Manager) runSequentialSend(ctx context.Context, seriesID string) {
	defer func() {
		m.mu.Lock()
		delete(m.activeCancels, seriesID)
		m.mu.Unlock()
		m.wg.Done()
	}()

	series, err := m.DB.GetPatchSeriesDraft(seriesID)
	if err != nil || series == nil {
		return
	}

	totalCount := len(series.Items)
	sentCount := 0
	for _, item := range series.Items {
		if item.Status == database.SeriesItemStatusSent {
			sentCount++
		}
	}

	rootMsgID := ""
	if totalCount > 0 {
		rootMsgID = series.Items[0].RFCMessageID
	}

	delay := time.Duration(series.SendDelaySec) * time.Second
	if delay <= 0 {
		delay = time.Duration(database.DefaultSeriesSendDelaySec) * time.Second
	}
	m.mu.Lock()
	if m.delayOverride != nil {
		delay = *m.delayOverride
	}
	m.mu.Unlock()

	markPaused := func(seqIndex int) {
		if err := m.DB.UpdatePatchSeriesStatus(seriesID, database.SeriesStatusPaused); err != nil {
			log.Printf("[series] failed to mark series %s paused: %v", seriesID, err)
		}
		m.publishEvent("series_send_progress", map[string]any{
			"series_id":   seriesID,
			"seq_index":   seqIndex,
			"status":      database.SeriesStatusPaused,
			"sent_count":  sentCount,
			"total_count": totalCount,
		})
	}

	for idx, item := range series.Items {
		if item.Status == database.SeriesItemStatusSent {
			continue
		}

		if ctx.Err() != nil {
			markPaused(item.SeqIndex)
			return
		}

		if err := m.DB.UpdatePatchSeriesItemStatus(seriesID, item.SeqIndex, database.SeriesItemStatusSending, "", nil); err != nil {
			log.Printf("[series] failed to mark item %s[%d] sending: %v", seriesID, item.SeqIndex, err)
		}
		m.publishEvent("series_send_progress", map[string]any{
			"series_id":   seriesID,
			"seq_index":   item.SeqIndex,
			"status":      database.SeriesItemStatusSending,
			"sent_count":  sentCount,
			"total_count": totalCount,
		})

		msg, body := m.loadDraftMessage(item)
		var rawToSend string
		if msg != nil && msg.FilePath != "" {
			if diskBytes, readErr := os.ReadFile(msg.FilePath); readErr == nil && len(diskBytes) > 0 {
				rawToSend = StripSeriesHeaders(string(diskBytes))
			}
		}
		if rawToSend == "" {
			rawToSend = m.buildItemRawEmail(series, item, rootMsgID, totalCount, body, false)
		}

		effTo := mergeUniqueAddrs(series.ToAddrs, item.ExtraTo)
		effCc := mergeUniqueAddrs(series.CcAddrs, item.ExtraCc)
		recipients := extractEnvelopeRecipients(effTo, effCc)

		if _, _, sendErr := m.SMTP.SendRawMessage(rawToSend, recipients); sendErr != nil {
			if dbErr := m.DB.UpdatePatchSeriesItemStatus(seriesID, item.SeqIndex, database.SeriesItemStatusFailed, sendErr.Error(), nil); dbErr != nil {
				log.Printf("[series] failed to record item %s[%d] failure: %v", seriesID, item.SeqIndex, dbErr)
			}
			if dbErr := m.DB.UpdatePatchSeriesStatus(seriesID, database.SeriesStatusFailed); dbErr != nil {
				log.Printf("[series] failed to record series %s failure: %v", seriesID, dbErr)
			}
			m.publishEvent("series_send_progress", map[string]any{
				"series_id":     seriesID,
				"seq_index":     item.SeqIndex,
				"status":        database.SeriesItemStatusFailed,
				"error_message": sendErr.Error(),
				"sent_count":    sentCount,
				"total_count":   totalCount,
			})
			return
		}

		// Remove the transmitted draft copy from Drafts (SendRawMessage stored the copy in Sent).
		if msg != nil {
			if msg.FilePath != "" {
				// Best-effort cleanup of transmitted draft file.
				_ = os.Remove(msg.FilePath)
			}
			// Best-effort removal of transmitted draft DB row.
			_ = m.DB.DeleteMessage(msg.ID)
		}

		sentAt := time.Now().UTC()
		if dbErr := m.DB.UpdatePatchSeriesItemStatus(seriesID, item.SeqIndex, database.SeriesItemStatusSent, "", &sentAt); dbErr != nil {
			// Critical: halt immediately if persisting sent status fails to avoid risking duplicate sends.
			log.Printf("[series] CRITICAL: failed to persist sent status for %s[%d]: %v", seriesID, item.SeqIndex, dbErr)
			if stErr := m.DB.UpdatePatchSeriesStatus(seriesID, database.SeriesStatusFailed); stErr != nil {
				log.Printf("[series] failed to mark series %s failed after DB error: %v", seriesID, stErr)
			}
			m.publishEvent("series_send_progress", map[string]any{
				"series_id":     seriesID,
				"seq_index":     item.SeqIndex,
				"status":        database.SeriesItemStatusFailed,
				"error_message": fmt.Sprintf("sent over SMTP but failed to persist state: %v", dbErr),
				"sent_count":    sentCount,
				"total_count":   totalCount,
			})
			return
		}
		sentCount++

		m.publishEvent("series_send_progress", map[string]any{
			"series_id":   seriesID,
			"seq_index":   item.SeqIndex,
			"status":      database.SeriesItemStatusSent,
			"sent_count":  sentCount,
			"total_count": totalCount,
		})

		// Check whether there are more unsent items after this one.
		hasMore := false
		for j := idx + 1; j < totalCount; j++ {
			if series.Items[j].Status != database.SeriesItemStatusSent {
				hasMore = true
				break
			}
		}
		if hasMore {
			if ctx.Err() != nil {
				markPaused(item.SeqIndex)
				return
			}
			timer := time.NewTimer(delay)
			select {
			case <-ctx.Done():
				timer.Stop()
				markPaused(item.SeqIndex)
				return
			case <-timer.C:
			}
		}
	}

	if err := m.DB.UpdatePatchSeriesStatus(seriesID, database.SeriesStatusSent); err != nil {
		log.Printf("[series] failed to mark series %s sent: %v", seriesID, err)
	}
	m.publishEvent("series_send_progress", map[string]any{
		"series_id":   seriesID,
		"status":      database.SeriesStatusSent,
		"sent_count":  sentCount,
		"total_count": totalCount,
	})
}
