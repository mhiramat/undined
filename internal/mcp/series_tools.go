package mcp

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"undined-go/internal/config"
	"undined-go/internal/constants"
	"undined-go/internal/lkml/patch"
	"undined-go/internal/smtp"
	"undined-go/internal/storage/database"
	"undined-go/internal/storage/parser"
)

var (
	allowlistedExtraHeaderRe = regexp.MustCompile(`^[Xx]-[A-Za-z0-9-]+$`)
	patchSeqSubjectRe        = regexp.MustCompile(`\[[^\]]*?(\d+)\s*/\s*(\d+)[^\]]*\]`)
	mboxFromEscapeRe         = regexp.MustCompile(`(?m)^(>*From )`)
	reviewTrailerLineRe      = regexp.MustCompile(`^(Reviewed-by|Acked-by|Tested-by|Suggested-by|Reported-by|Fixes):\s+.+$`)
)

func generateID(prefix string) string {
	b := make([]byte, 8)
	// crypto/rand.Read never returns an error on supported platforms (Go 1.24+).
	_, _ = rand.Read(b)
	return fmt.Sprintf("%s%x%s", prefix, time.Now().UTC().UnixMilli(), hex.EncodeToString(b))
}

func (s *Server) buildWebURL(fragment string) string {
	host := constants.DefaultHost
	port := constants.DefaultPort
	if s.Config != nil {
		if strings.TrimSpace(s.Config.General.Host) != "" {
			host = strings.TrimSpace(s.Config.General.Host)
		}
		if s.Config.General.Port > 0 {
			port = s.Config.General.Port
		}
	}
	if !strings.HasPrefix(fragment, "/") {
		fragment = "/" + fragment
	}
	return fmt.Sprintf("http://%s:%d%s", host, port, fragment)
}

func (s *Server) fromEmailAddress() string {
	if s.Config != nil && strings.TrimSpace(s.Config.SMTP.FromEmail) != "" {
		return strings.TrimSpace(s.Config.SMTP.FromEmail)
	}
	return constants.DefaultFromEmail
}

func getStringSlice(args map[string]interface{}, keys ...string) []string {
	for _, k := range keys {
		val, ok := args[k]
		if !ok || val == nil {
			continue
		}
		switch v := val.(type) {
		case []interface{}:
			out := make([]string, 0, len(v))
			for _, item := range v {
				if str, ok := item.(string); ok {
					if trimmed := strings.TrimSpace(str); trimmed != "" {
						out = append(out, trimmed)
					}
				}
			}
			return out
		case []string:
			out := make([]string, 0, len(v))
			for _, str := range v {
				if trimmed := strings.TrimSpace(str); trimmed != "" {
					out = append(out, trimmed)
				}
			}
			return out
		case string:
			if trimmed := strings.TrimSpace(v); trimmed != "" {
				return parser.SplitAddressList(trimmed)
			}
		}
	}
	return make([]string, 0)
}

func parseAllowlistedExtraHeaders(args map[string]interface{}) ([]smtp.HeaderField, error) {
	raw, ok := args["extra_headers"]
	if !ok || raw == nil {
		return nil, nil
	}
	hdrMap, ok := raw.(map[string]interface{})
	if !ok {
		return nil, fmt.Errorf("extra_headers must be an object mapping header names to strings")
	}

	keys := make([]string, 0, len(hdrMap))
	for k := range hdrMap {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	headers := make([]smtp.HeaderField, 0, len(keys))
	for _, k := range keys {
		name := strings.TrimSpace(k)
		if !allowlistedExtraHeaderRe.MatchString(name) {
			return nil, fmt.Errorf("disallowed extra header %q: only X-* headers are permitted", k)
		}
		lower := strings.ToLower(name)
		if lower == "x-mailer" || strings.HasPrefix(lower, "x-undine-series-") {
			return nil, fmt.Errorf("reserved header %q cannot be set via extra_headers", k)
		}
		valStr, ok := hdrMap[k].(string)
		if !ok {
			return nil, fmt.Errorf("extra header %q value must be a string", k)
		}
		if strings.ContainsAny(valStr, "\r\n") {
			return nil, fmt.Errorf("extra header %q value must not contain CR or LF", k)
		}
		headers = append(headers, smtp.HeaderField{Name: name, Value: strings.TrimSpace(valStr)})
	}
	return headers, nil
}

func formatReferencesHeader(refs []string, fallbackInReplyTo string) string {
	seen := make(map[string]struct{})
	formatted := make([]string, 0, len(refs)+1)
	for _, r := range refs {
		for _, id := range parser.ExtractMsgIDs(r) {
			if _, exists := seen[id]; !exists {
				seen[id] = struct{}{}
				formatted = append(formatted, "<"+id+">")
			}
		}
	}
	if fallbackInReplyTo != "" {
		if cleaned := parser.CleanMsgID(fallbackInReplyTo); cleaned != "" {
			if _, exists := seen[cleaned]; !exists {
				seen[cleaned] = struct{}{}
				formatted = append(formatted, "<"+cleaned+">")
			}
		}
	}
	return strings.Join(formatted, " ")
}

func mergeAddresses(base, extra []string) []string {
	seen := make(map[string]struct{}, len(base)+len(extra))
	merged := make([]string, 0, len(base)+len(extra))
	for _, list := range [][]string{base, extra} {
		for _, addr := range list {
			trimmed := strings.TrimSpace(addr)
			if trimmed == "" {
				continue
			}
			key := strings.ToLower(trimmed)
			if _, ok := seen[key]; !ok {
				seen[key] = struct{}{}
				merged = append(merged, trimmed)
			}
		}
	}
	return merged
}

func (s *Server) resolveTagFlags(tags []string) []string {
	if len(tags) == 0 {
		return nil
	}
	tokens := make([]string, 0, len(tags))
	seen := make(map[string]struct{}, len(tags))
	for _, raw := range tags {
		trimmed := strings.TrimSpace(raw)
		if trimmed == "" {
			continue
		}
		resolved := trimmed
		if s.Config != nil && len(s.Config.Tags) > 0 {
			for key, def := range s.Config.Tags {
				if strings.EqualFold(key, trimmed) || strings.EqualFold(def.Tag, trimmed) {
					resolved = key
					break
				}
			}
		}
		// Ensure flag tokens contain no whitespace or control characters.
		resolved = strings.Map(func(r rune) rune {
			if r <= 0x20 || r == 0x7f {
				return -1
			}
			return r
		}, resolved)
		if resolved == "" {
			continue
		}
		lower := strings.ToLower(resolved)
		if _, ok := seen[lower]; !ok {
			seen[lower] = struct{}{}
			tokens = append(tokens, resolved)
		}
	}
	return tokens
}

func (s *Server) toolCreateMailDraft(args map[string]interface{}) (interface{}, error) {
	t0 := time.Now()
	if s.DB == nil || s.Maildir == nil {
		durMs := time.Since(t0).Seconds() * 1000.0
		s.writeAccessLog("WARNING", fmt.Sprintf("MCP access: create_mail_draft() -> REJECTED (Storage not initialized) (%.1fms)", durMs))
		return nil, fmt.Errorf("storage not initialized")
	}

	toAddrs := getStringSlice(args, "to")
	ccAddrs := getStringSlice(args, "cc")
	subject := strings.TrimSpace(getString(args, "subject"))
	body := getString(args, "body")
	inReplyTo := strings.TrimSpace(getString(args, "in_reply_to"))
	refs := getStringSlice(args, "references")
	tags := getStringSlice(args, "tags")

	if len(toAddrs) == 0 {
		return nil, fmt.Errorf("to is required and must contain at least one recipient")
	}
	if subject == "" {
		return nil, fmt.Errorf("subject is required")
	}
	if strings.TrimSpace(body) == "" {
		return nil, fmt.Errorf("body is required")
	}

	extraHeaders, err := parseAllowlistedExtraHeaders(args)
	if err != nil {
		durMs := time.Since(t0).Seconds() * 1000.0
		s.writeAccessLog("WARNING", fmt.Sprintf("MCP access: create_mail_draft() -> REJECTED (%v) (%.1fms)", err, durMs))
		return nil, err
	}

	referencesHdr := formatReferencesHeader(refs, inReplyTo)

	// Strictly build RFC 5322 email in memory without ever invoking SMTP transport.
	smtpClient := smtp.NewClient(s.Config, s.Maildir, s.DB)
	rawMsg, msgID := smtpClient.BuildPlainTextMessage(smtp.PlainTextMessageOptions{
		To:           strings.Join(toAddrs, ", "),
		Cc:           strings.Join(ccAddrs, ", "),
		Subject:      subject,
		Body:         body,
		InReplyTo:    inReplyTo,
		References:   referencesHdr,
		ExtraHeaders: extraHeaders,
	})

	if err := s.Maildir.EnsureFolder(constants.FolderDrafts); err != nil {
		return nil, fmt.Errorf("ensure Drafts folder: %w", err)
	}
	draftFolder, err := s.DB.GetOrCreateFolder(constants.FolderDrafts, false)
	if err != nil {
		return nil, fmt.Errorf("get Drafts folder: %w", err)
	}

	savedPath, err := s.Maildir.AddMessage(constants.FolderDrafts, []byte(rawMsg), constants.FlagDraft)
	if err != nil {
		return nil, fmt.Errorf("write draft to Maildir: %w", err)
	}

	dbID, err := s.DB.IndexEmailFile(draftFolder.ID, savedPath)
	if err != nil {
		// Best-effort cleanup of Maildir file when DB indexing fails.
		_ = os.Remove(savedPath)
		return nil, fmt.Errorf("index draft in database: %w", err)
	}

	if tagTokens := s.resolveTagFlags(tags); len(tagTokens) > 0 {
		existingFlags := ""
		if msg, getErr := s.DB.GetMessageByID(dbID); getErr == nil && msg != nil {
			existingFlags = msg.Flags
		}
		allTokens := strings.Fields(existingFlags)
		seen := make(map[string]struct{}, len(allTokens))
		for _, tok := range allTokens {
			seen[strings.ToLower(tok)] = struct{}{}
		}
		for _, tok := range tagTokens {
			if _, ok := seen[strings.ToLower(tok)]; !ok {
				seen[strings.ToLower(tok)] = struct{}{}
				allTokens = append(allTokens, tok)
			}
		}
		if err := s.DB.SetMessageFlags(dbID, strings.Join(allTokens, " ")); err != nil {
			return nil, fmt.Errorf("apply draft tags: %w", err)
		}
	}

	draftID := strconv.FormatInt(dbID, 10)
	durMs := time.Since(t0).Seconds() * 1000.0
	s.writeAccessLog("INFO", fmt.Sprintf("MCP access: create_mail_draft(subject=%q, to=%d, cc=%d, tags=%d) -> SUCCESS (draft_id=%s, message_id=%q) (%.1fms)",
		subject, len(toAddrs), len(ccAddrs), len(tags), draftID, msgID, durMs))

	return map[string]interface{}{
		"draft_id":     draftID,
		"message_id":   msgID,
		"maildir_path": savedPath,
		"web_url":      s.buildWebURL("/#drafts/" + draftID),
	}, nil
}

type seriesMessageInput struct {
	Index         int
	IsCoverLetter bool
	Subject       string
	Body          string
	CommitSHA     string
	ExtraTo       []string
	ExtraCc       []string
}

func parseSeriesMessages(raw []interface{}) ([]seriesMessageInput, error) {
	if len(raw) == 0 {
		return nil, fmt.Errorf("messages is required and must contain at least one message")
	}

	parsed := make([]seriesMessageInput, 0, len(raw))
	seenIdx := make(map[int]struct{}, len(raw))

	for i, item := range raw {
		m, ok := item.(map[string]interface{})
		if !ok {
			return nil, fmt.Errorf("messages[%d] must be an object", i)
		}
		idx := getInt(m, "index", -1)
		if idx < 0 {
			return nil, fmt.Errorf("messages[%d].index must be a non-negative integer", i)
		}
		if _, dup := seenIdx[idx]; dup {
			return nil, fmt.Errorf("duplicate message index %d", idx)
		}
		seenIdx[idx] = struct{}{}

		isCover := getBool(m, "is_cover_letter", false)
		if isCover && idx != 0 {
			return nil, fmt.Errorf("messages[%d]: is_cover_letter may only be true when index is 0", i)
		}

		subject := strings.TrimSpace(getString(m, "subject"))
		if subject == "" {
			return nil, fmt.Errorf("messages[%d].subject is required", i)
		}
		body := getString(m, "body")
		if strings.TrimSpace(body) == "" {
			return nil, fmt.Errorf("messages[%d].body is required", i)
		}

		parsed = append(parsed, seriesMessageInput{
			Index:         idx,
			IsCoverLetter: isCover,
			Subject:       subject,
			Body:          body,
			CommitSHA:     strings.TrimSpace(getString(m, "commit_sha")),
			ExtraTo:       getStringSlice(m, "extra_to"),
			ExtraCc:       getStringSlice(m, "extra_cc"),
		})
	}

	sort.Slice(parsed, func(i, j int) bool {
		return parsed[i].Index < parsed[j].Index
	})
	return parsed, nil
}

func (s *Server) deleteExistingSeriesDrafts(existing *database.PatchSeriesDraft) error {
	for _, item := range existing.Items {
		if idVal, err := strconv.ParseInt(item.DraftMsgID, 10, 64); err == nil {
			if msg, getErr := s.DB.GetMessageByID(idVal); getErr == nil && msg != nil {
				if msg.FilePath != "" {
					if err := os.Remove(msg.FilePath); err != nil && !os.IsNotExist(err) {
						return fmt.Errorf("remove draft file %s: %w", msg.FilePath, err)
					}
				}
				if err := s.DB.DeleteMessage(msg.ID); err != nil {
					return fmt.Errorf("delete draft message %d: %w", msg.ID, err)
				}
			}
		}
	}
	return s.DB.DeletePatchSeriesDraft(existing.SeriesID)
}

func (s *Server) toolStagePatchSeries(args map[string]interface{}) (interface{}, error) {
	t0 := time.Now()
	if s.DB == nil || s.Maildir == nil {
		durMs := time.Since(t0).Seconds() * 1000.0
		s.writeAccessLog("WARNING", fmt.Sprintf("MCP access: stage_patch_series() -> REJECTED (Storage not initialized) (%.1fms)", durMs))
		return nil, fmt.Errorf("storage not initialized")
	}

	seriesKey := strings.TrimSpace(getString(args, "series_key"))
	branch := strings.TrimSpace(getString(args, "branch"))
	version := getInt(args, "version", 1)
	if version <= 0 {
		version = 1
	}
	toAddrs := getStringSlice(args, "to")
	ccAddrs := getStringSlice(args, "cc")
	inReplyTo := strings.TrimSpace(getString(args, "in_reply_to"))
	baseCommit := strings.TrimSpace(getString(args, "base_commit"))
	replaceExisting := getBool(args, "replace_existing", false)
	rawMessages := getSlice(args, "messages")

	if seriesKey == "" {
		return nil, fmt.Errorf("series_key is required")
	}
	if len(toAddrs) == 0 {
		return nil, fmt.Errorf("to is required and must contain at least one recipient")
	}

	parsedMsgs, err := parseSeriesMessages(rawMessages)
	if err != nil {
		durMs := time.Since(t0).Seconds() * 1000.0
		s.writeAccessLog("WARNING", fmt.Sprintf("MCP access: stage_patch_series(series_key=%q) -> REJECTED (%v) (%.1fms)", seriesKey, err, durMs))
		return nil, err
	}

	existing, err := s.DB.GetPatchSeriesDraftByKey(seriesKey)
	if err != nil {
		return nil, fmt.Errorf("lookup existing series %q: %w", seriesKey, err)
	}
	if existing != nil {
		if !replaceExisting {
			return nil, fmt.Errorf("unsent series draft with series_key %q already exists (set replace_existing=true to replace)", seriesKey)
		}
		hasSent, err := s.DB.HasAnySentItem(existing.SeriesID)
		if err != nil {
			return nil, fmt.Errorf("check sent status for series %q: %w", seriesKey, err)
		}
		if hasSent || existing.Status == database.SeriesStatusSent || existing.Status == database.SeriesStatusSending {
			durMs := time.Since(t0).Seconds() * 1000.0
			s.writeAccessLog("WARNING", fmt.Sprintf("MCP access: stage_patch_series(series_key=%q) -> REJECTED (already sent) (%.1fms)", seriesKey, durMs))
			return nil, fmt.Errorf("cannot replace series %q: one or more messages have already been sent", seriesKey)
		}
		if err := s.deleteExistingSeriesDrafts(existing); err != nil {
			return nil, fmt.Errorf("replace existing series %q: %w", seriesKey, err)
		}
	}

	if err := s.Maildir.EnsureFolder(constants.FolderDrafts); err != nil {
		return nil, fmt.Errorf("ensure Drafts folder: %w", err)
	}
	draftFolder, err := s.DB.GetOrCreateFolder(constants.FolderDrafts, false)
	if err != nil {
		return nil, fmt.Errorf("get Drafts folder: %w", err)
	}

	seriesID := generateID("ser_")
	now := time.Now().UTC()
	token := smtp.RandomSeriesToken()
	fromEmail := s.fromEmailAddress()
	totalCount := len(parsedMsgs)
	cleanedParent := parser.CleanMsgID(inReplyTo)

	smtpClient := smtp.NewClient(s.Config, s.Maildir, s.DB)

	type stagedFile struct {
		dbID int64
		path string
	}
	stagedFiles := make([]stagedFile, 0, totalCount)
	cleanupStaged := func() {
		for _, sf := range stagedFiles {
			// Best-effort rollback cleanup on partial staging failure.
			_ = os.Remove(sf.path)
			_ = s.DB.DeleteMessage(sf.dbID)
		}
	}

	dbItems := make([]*database.PatchSeriesItem, 0, totalCount)
	respItems := make([]map[string]interface{}, 0, totalCount)
	rootMsgID := ""

	for i, msgInput := range parsedMsgs {
		rfcMsgID := smtp.GenerateGitSendEmailMessageID(now, token, i+1, fromEmail)
		if i == 0 {
			rootMsgID = rfcMsgID
		}

		var itemInReplyTo, itemReferences string
		if i == 0 {
			if cleanedParent != "" {
				itemInReplyTo = "<" + cleanedParent + ">"
				itemReferences = "<" + cleanedParent + ">"
			}
		} else {
			itemInReplyTo = rootMsgID
			if cleanedParent != "" {
				itemReferences = fmt.Sprintf("<%s> %s", cleanedParent, rootMsgID)
			} else {
				itemReferences = rootMsgID
			}
		}

		effTo := mergeAddresses(toAddrs, msgInput.ExtraTo)
		effCc := mergeAddresses(ccAddrs, msgInput.ExtraCc)

		trackingHeaders := []smtp.HeaderField{
			{Name: "X-Undine-Series-ID", Value: seriesID},
			{Name: "X-Undine-Series-Index", Value: strconv.Itoa(msgInput.Index)},
			{Name: "X-Undine-Series-Total", Value: strconv.Itoa(totalCount)},
		}

		rawMsg, builtMsgID := smtpClient.BuildPlainTextMessage(smtp.PlainTextMessageOptions{
			To:           strings.Join(effTo, ", "),
			Cc:           strings.Join(effCc, ", "),
			Subject:      msgInput.Subject,
			Body:         msgInput.Body,
			InReplyTo:    itemInReplyTo,
			References:   itemReferences,
			MessageID:    rfcMsgID,
			Date:         now.Add(time.Duration(i) * time.Second),
			ExtraHeaders: trackingHeaders,
		})

		savedPath, err := s.Maildir.AddMessage(constants.FolderDrafts, []byte(rawMsg), constants.FlagDraft)
		if err != nil {
			cleanupStaged()
			return nil, fmt.Errorf("write series item %d to Maildir: %w", msgInput.Index, err)
		}

		dbID, err := s.DB.IndexEmailFile(draftFolder.ID, savedPath)
		if err != nil {
			// Best-effort cleanup of Maildir file when DB indexing fails.
			_ = os.Remove(savedPath)
			cleanupStaged()
			return nil, fmt.Errorf("index series item %d: %w", msgInput.Index, err)
		}
		stagedFiles = append(stagedFiles, stagedFile{dbID: dbID, path: savedPath})

		draftID := strconv.FormatInt(dbID, 10)
		dbItems = append(dbItems, &database.PatchSeriesItem{
			SeriesID:      seriesID,
			SeqIndex:      msgInput.Index,
			IsCoverLetter: msgInput.IsCoverLetter,
			DraftMsgID:    draftID,
			RFCMessageID:  builtMsgID,
			Subject:       msgInput.Subject,
			CommitSHA:     msgInput.CommitSHA,
			ExtraTo:       msgInput.ExtraTo,
			ExtraCc:       msgInput.ExtraCc,
			Status:        database.SeriesItemStatusDraft,
		})
		respItems = append(respItems, map[string]interface{}{
			"index":      msgInput.Index,
			"draft_id":   draftID,
			"message_id": builtMsgID,
			"status":     database.SeriesItemStatusDraft,
		})
	}

	seriesRecord := &database.PatchSeriesDraft{
		SeriesID:     seriesID,
		SeriesKey:    seriesKey,
		Branch:       branch,
		Version:      version,
		BaseCommit:   baseCommit,
		ToAddrs:      toAddrs,
		CcAddrs:      ccAddrs,
		InReplyTo:    inReplyTo,
		Status:       database.SeriesStatusDraft,
		SendDelaySec: database.DefaultSeriesSendDelaySec,
		CreatedAt:    now.Format(time.RFC3339),
		UpdatedAt:    now.Format(time.RFC3339),
	}

	if err := s.DB.CreatePatchSeriesDraft(seriesRecord, dbItems); err != nil {
		cleanupStaged()
		return nil, fmt.Errorf("save series draft metadata: %w", err)
	}

	if s.Events != nil {
		s.Events.Publish("series_draft_updated", map[string]any{
			"series_id":       seriesID,
			"series_key":      seriesKey,
			"message_count":   len(dbItems),
			"root_message_id": rootMsgID,
			"status":          database.SeriesStatusDraft,
		})
	}

	durMs := time.Since(t0).Seconds() * 1000.0
	s.writeAccessLog("INFO", fmt.Sprintf("MCP access: stage_patch_series(series_key=%q, count=%d) -> SUCCESS (series_id=%s, root=%q) (%.1fms)",
		seriesKey, len(dbItems), seriesID, rootMsgID, durMs))

	return map[string]interface{}{
		"series_id":       seriesID,
		"series_key":      seriesKey,
		"message_count":   len(dbItems),
		"root_message_id": rootMsgID,
		"items":           respItems,
		"web_url":         s.buildWebURL("/#series/" + seriesID),
	}, nil
}

func (s *Server) allowedExportDirs() []string {
	dirs := []string{os.TempDir()}
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		dirs = append(dirs, filepath.Join(home, ".cache", "undined"))
	}
	if s.Config != nil {
		if s.Config.General.LogFile != "" {
			dirs = append(dirs, filepath.Dir(config.ExpandPath(s.Config.General.LogFile)))
		}
		if s.Config.General.DataDir != "" {
			dirs = append(dirs, filepath.Join(config.ExpandPath(s.Config.General.DataDir), "exports"))
		}
	}
	cleaned := make([]string, 0, len(dirs))
	for _, d := range dirs {
		if d == "" {
			continue
		}
		abs, err := filepath.Abs(filepath.Clean(d))
		if err != nil {
			continue
		}
		if resolved, err := filepath.EvalSymlinks(abs); err == nil {
			abs = resolved
		}
		cleaned = append(cleaned, abs)
	}
	return cleaned
}

func resolvePathThroughExistingAncestor(absPath string) (string, error) {
	// Reject if target itself already exists as a symlink.
	if info, err := os.Lstat(absPath); err == nil {
		if info.Mode()&os.ModeSymlink != 0 {
			return "", fmt.Errorf("output_path %q must not be a symlink", absPath)
		}
	}

	curr := absPath
	var tail []string
	for {
		resolved, err := filepath.EvalSymlinks(curr)
		if err == nil {
			for i := len(tail) - 1; i >= 0; i-- {
				resolved = filepath.Join(resolved, tail[i])
			}
			return resolved, nil
		}
		parent := filepath.Dir(curr)
		if parent == curr {
			return absPath, nil
		}
		tail = append(tail, filepath.Base(curr))
		curr = parent
	}
}

func (s *Server) validateExportOutputPath(outputPath string) (string, error) {
	expanded := config.ExpandPath(strings.TrimSpace(outputPath))
	absPath, err := filepath.Abs(filepath.Clean(expanded))
	if err != nil {
		return "", fmt.Errorf("invalid output_path %q: %w", outputPath, err)
	}
	resolvedTarget, err := resolvePathThroughExistingAncestor(absPath)
	if err != nil {
		return "", err
	}
	for _, base := range s.allowedExportDirs() {
		rel, err := filepath.Rel(base, resolvedTarget)
		if err == nil && rel != "." && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return resolvedTarget, nil
		}
	}
	return "", fmt.Errorf("output_path %q is outside allowed temp/cache directories", outputPath)
}

func parsePatchSequence(subject string) (seq int, total int, ok bool) {
	m := patchSeqSubjectRe.FindStringSubmatch(subject)
	if len(m) != 3 {
		return 0, 0, false
	}
	sVal, err1 := strconv.Atoi(m[1])
	tVal, err2 := strconv.Atoi(m[2])
	if err1 != nil || err2 != nil || tVal <= 0 {
		return 0, 0, false
	}
	return sVal, tVal, true
}

func isReplySubject(subject string) bool {
	return strings.HasPrefix(strings.ToLower(strings.TrimSpace(subject)), "re:")
}

func extractReviewTrailers(body string) []string {
	lines := strings.Split(strings.ReplaceAll(body, "\r\n", "\n"), "\n")
	trailers := make([]string, 0)
	seen := make(map[string]struct{})
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		// Skip quoted lines so we only pick up new trailers authored in the reply.
		if strings.HasPrefix(trimmed, ">") {
			continue
		}
		if reviewTrailerLineRe.MatchString(trimmed) {
			if _, dup := seen[trimmed]; !dup {
				seen[trimmed] = struct{}{}
				trailers = append(trailers, trimmed)
			}
		}
	}
	return trailers
}

func injectTrailersIntoRawEmail(raw []byte, trailers []string) []byte {
	if len(trailers) == 0 {
		return raw
	}
	normalized := strings.ReplaceAll(string(raw), "\r\n", "\n")
	sepIdx := strings.Index(normalized, "\n\n")
	if sepIdx == -1 {
		return raw
	}
	headers := normalized[:sepIdx+2]
	body := normalized[sepIdx+2:]

	// Determine which trailers are not already present in the patch body, deduplicating inputs.
	seen := make(map[string]struct{}, len(trailers))
	missing := make([]string, 0, len(trailers))
	for _, tr := range trailers {
		if _, dup := seen[tr]; dup {
			continue
		}
		seen[tr] = struct{}{}
		if !strings.Contains(body, tr) {
			missing = append(missing, tr)
		}
	}
	if len(missing) == 0 {
		return raw
	}

	lines := strings.Split(body, "\n")
	insertAt := -1
	for i, line := range lines {
		if line == "---" || strings.HasPrefix(line, "diff --git ") {
			insertAt = i
			break
		}
	}
	if insertAt == -1 {
		return raw
	}

	resultLines := make([]string, 0, len(lines)+len(missing))
	resultLines = append(resultLines, lines[:insertAt]...)
	resultLines = append(resultLines, missing...)
	resultLines = append(resultLines, lines[insertAt:]...)
	return []byte(headers + strings.Join(resultLines, "\n"))
}

func stripLeadingMboxFromLines(normalized []byte) []byte {
	rest := normalized
	for {
		rest = bytes.TrimLeft(rest, "\n")
		if !bytes.HasPrefix(rest, []byte("From ")) {
			break
		}
		nl := bytes.IndexByte(rest, '\n')
		if nl == -1 {
			return nil
		}
		rest = rest[nl+1:]
	}
	return rest
}

func formatMboxEntry(m *database.Message, raw []byte) []byte {
	sender := "MAILER-DAEMON"
	if fields := strings.Fields(m.FromAddr); len(fields) > 0 {
		sender = fields[0]
	}
	ts := time.Now().UTC()
	if m.EpochTimestamp > 0 {
		ts = time.Unix(m.EpochTimestamp, 0).UTC()
	}
	fromLine := fmt.Sprintf("From %s %s\n", sender, ts.Format(time.ANSIC))

	normalized := bytes.ReplaceAll(raw, []byte("\r\n"), []byte("\n"))
	normalized = stripLeadingMboxFromLines(normalized)
	sepIdx := bytes.Index(normalized, []byte("\n\n"))
	var out bytes.Buffer
	out.WriteString(fromLine)
	if sepIdx != -1 {
		out.Write(normalized[:sepIdx+2])
		body := mboxFromEscapeRe.ReplaceAll(normalized[sepIdx+2:], []byte(">$1"))
		out.Write(body)
	} else {
		out.Write(normalized)
	}
	if !bytes.HasSuffix(out.Bytes(), []byte("\n")) {
		out.WriteByte('\n')
	}
	out.WriteByte('\n')
	return out.Bytes()
}

func (s *Server) readRawEmailBytes(m *database.Message) []byte {
	if m.FilePath != "" {
		if raw, err := os.ReadFile(m.FilePath); err == nil && len(raw) > 0 {
			return raw
		}
	}
	var b strings.Builder
	fromHdr := m.FromAddr
	if m.FromName != "" {
		fromHdr = fmt.Sprintf("%s <%s>", m.FromName, m.FromAddr)
	}
	fmt.Fprintf(&b, "From: %s\n", fromHdr)
	if m.ToAddr != "" {
		fmt.Fprintf(&b, "To: %s\n", m.ToAddr)
	}
	if m.CcAddr != "" {
		fmt.Fprintf(&b, "Cc: %s\n", m.CcAddr)
	}
	fmt.Fprintf(&b, "Subject: %s\n", m.Subject)
	if m.Date != "" {
		fmt.Fprintf(&b, "Date: %s\n", m.Date)
	}
	if cleanID := parser.CleanMsgID(m.MessageID); cleanID != "" {
		fmt.Fprintf(&b, "Message-ID: <%s>\n", cleanID)
	}
	b.WriteString("MIME-Version: 1.0\nContent-Type: text/plain; charset=utf-8\nContent-Transfer-Encoding: 8bit\n\n")
	b.WriteString(m.BodyPlain)
	if !strings.HasSuffix(m.BodyPlain, "\n") {
		b.WriteByte('\n')
	}
	return []byte(b.String())
}

func (s *Server) toolExportThreadMbox(args map[string]interface{}) (interface{}, error) {
	t0 := time.Now()
	if s.DB == nil {
		return nil, fmt.Errorf("database not initialized")
	}

	msgID := strings.TrimSpace(getString(args, "message_id", "id"))
	if msgID == "" {
		return nil, fmt.Errorf("message_id is required")
	}
	outputPath := strings.TrimSpace(getString(args, "output_path"))
	patchesOnly := getBool(args, "patches_only", true)

	orig, err := s.resolveMessage(msgID)
	if err != nil || orig == nil {
		durMs := time.Since(t0).Seconds() * 1000.0
		s.writeAccessLog("WARNING", fmt.Sprintf("MCP access: export_thread_mbox(%q) -> NOT FOUND (%.1fms)", msgID, durMs))
		return nil, fmt.Errorf("message not found: %s", msgID)
	}

	threadMsgs, err := s.DB.GetThreadHierarchy(orig.MessageID)
	if err != nil {
		return nil, fmt.Errorf("load thread for %q: %w", msgID, err)
	}
	foundOrig := false
	for _, m := range threadMsgs {
		if m.ID == orig.ID {
			foundOrig = true
			break
		}
	}
	if !foundOrig {
		threadMsgs = append([]*database.Message{orig}, threadMsgs...)
	}

	// Deduplicate by cleaned Message-ID (preferring copies with FilePath on disk).
	dedupMap := make(map[string]*database.Message, len(threadMsgs))
	orderedUnique := make([]*database.Message, 0, len(threadMsgs))
	for _, m := range threadMsgs {
		s.hydrateMessageBody(m)
		key := parser.CleanMsgID(m.MessageID)
		if key == "" {
			key = strconv.FormatInt(m.ID, 10)
		}
		if existing, exists := dedupMap[key]; exists {
			if existing.FilePath == "" && m.FilePath != "" {
				dedupMap[key] = m
			}
			continue
		}
		dedupMap[key] = m
		orderedUnique = append(orderedUnique, m)
	}
	for i, m := range orderedUnique {
		key := parser.CleanMsgID(m.MessageID)
		if key == "" {
			key = strconv.FormatInt(m.ID, 10)
		}
		orderedUnique[i] = dedupMap[key]
	}

	// Determine root_message_id and base_commit across the thread.
	rootMsg := orderedUnique[0]
	for _, m := range orderedUnique {
		parent := parser.CleanMsgID(m.InReplyTo)
		if parent == "" || dedupMap[parent] == nil {
			rootMsg = m
			break
		}
	}
	rootClean := parser.CleanMsgID(rootMsg.MessageID)
	rootMsgID := ""
	if rootClean != "" {
		rootMsgID = "<" + rootClean + ">"
	}

	baseCommit := ""
	for _, m := range orderedUnique {
		if bc := patch.ExtractBaseCommit(m.BodyPlain); bc != "" {
			baseCommit = bc
			break
		}
	}

	// Identify cover letter ID (if any) and collect review trailers from replies.
	coverLetterID := ""
	trailersByTarget := make(map[string][]string)
	for _, m := range orderedUnique {
		seq, _, hasSeq := parsePatchSequence(m.Subject)
		if hasSeq && seq == 0 && !isReplySubject(m.Subject) {
			coverLetterID = parser.CleanMsgID(m.MessageID)
		}
		if isReplySubject(m.Subject) || !m.IsPatch {
			trailers := extractReviewTrailers(m.BodyPlain)
			if len(trailers) > 0 {
				parentID := parser.CleanMsgID(m.InReplyTo)
				if parentID != "" {
					trailersByTarget[parentID] = append(trailersByTarget[parentID], trailers...)
				}
			}
		}
	}

	type exportCandidate struct {
		msg   *database.Message
		seq   int
		hasSq bool
	}
	selected := make([]exportCandidate, 0, len(orderedUnique))
	if patchesOnly {
		for _, m := range orderedUnique {
			if isReplySubject(m.Subject) {
				continue
			}
			seq, _, hasSeq := parsePatchSequence(m.Subject)
			if hasSeq && seq == 0 {
				continue // Skip 0/N cover letter
			}
			isPatchMsg := m.IsPatch
			if !isPatchMsg {
				parsed := patch.Parse(m.BodyPlain)
				isPatchMsg = parsed.IsPatch && !parsed.BodyContinuesAfter
			}
			if !isPatchMsg {
				continue
			}
			selected = append(selected, exportCandidate{msg: m, seq: seq, hasSq: hasSeq})
		}
		sort.SliceStable(selected, func(i, j int) bool {
			if selected[i].hasSq && selected[j].hasSq && selected[i].seq != selected[j].seq {
				return selected[i].seq < selected[j].seq
			}
			if selected[i].hasSq != selected[j].hasSq {
				return selected[i].hasSq
			}
			if selected[i].msg.EpochTimestamp != selected[j].msg.EpochTimestamp {
				return selected[i].msg.EpochTimestamp < selected[j].msg.EpochTimestamp
			}
			return selected[i].msg.ID < selected[j].msg.ID
		})
	} else {
		for _, m := range orderedUnique {
			seq, _, hasSeq := parsePatchSequence(m.Subject)
			selected = append(selected, exportCandidate{msg: m, seq: seq, hasSq: hasSeq})
		}
	}

	var mboxBuf bytes.Buffer
	for _, cand := range selected {
		raw := s.readRawEmailBytes(cand.msg)
		if patchesOnly {
			var combinedTrailers []string
			cleanID := parser.CleanMsgID(cand.msg.MessageID)
			if coverLetterID != "" {
				combinedTrailers = append(combinedTrailers, trailersByTarget[coverLetterID]...)
			}
			if cleanID != "" {
				combinedTrailers = append(combinedTrailers, trailersByTarget[cleanID]...)
			}
			raw = injectTrailersIntoRawEmail(raw, combinedTrailers)
		}
		mboxBuf.Write(formatMboxEntry(cand.msg, raw))
	}

	var destPath string
	if outputPath != "" {
		validated, valErr := s.validateExportOutputPath(outputPath)
		if valErr != nil {
			durMs := time.Since(t0).Seconds() * 1000.0
			s.writeAccessLog("WARNING", fmt.Sprintf("MCP access: export_thread_mbox(%q) -> REJECTED (%v) (%.1fms)", msgID, valErr, durMs))
			return nil, valErr
		}
		if mkErr := os.MkdirAll(filepath.Dir(validated), 0700); mkErr != nil {
			return nil, fmt.Errorf("create output directory: %w", mkErr)
		}
		if writeErr := os.WriteFile(validated, mboxBuf.Bytes(), 0600); writeErr != nil {
			return nil, fmt.Errorf("write mbox file: %w", writeErr)
		}
		destPath = validated
	} else {
		tmpFile, tmpErr := os.CreateTemp("", "undined-export-*.mbx")
		if tmpErr != nil {
			return nil, fmt.Errorf("create temporary mbox file: %w", tmpErr)
		}
		destPath = tmpFile.Name()
		if _, writeErr := tmpFile.Write(mboxBuf.Bytes()); writeErr != nil {
			// Best-effort cleanup of partially written temp file.
			_ = tmpFile.Close()
			_ = os.Remove(destPath)
			return nil, fmt.Errorf("write temporary mbox file: %w", writeErr)
		}
		if closeErr := tmpFile.Close(); closeErr != nil {
			// Best-effort cleanup of temp file that failed to close cleanly.
			_ = os.Remove(destPath)
			return nil, fmt.Errorf("close temporary mbox file: %w", closeErr)
		}
	}

	durMs := time.Since(t0).Seconds() * 1000.0
	s.writeAccessLog("INFO", fmt.Sprintf("MCP access: export_thread_mbox(%q, patches_only=%v) -> SUCCESS (path=%q, patches=%d) (%.1fms)",
		msgID, patchesOnly, destPath, len(selected), durMs))

	return map[string]interface{}{
		"mbox_path":       destPath,
		"patch_count":     len(selected),
		"base_commit":     baseCommit,
		"root_message_id": rootMsgID,
	}, nil
}
