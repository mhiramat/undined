package mcp

import (
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"undined-go/internal/config"
	"undined-go/internal/constants"
	"undined-go/internal/storage/database"
	"undined-go/internal/storage/parser"
)

const (
	maxUpdateEmailTagsMessageIDs = 100
	maxTagsPerEmail              = 32
	maxTagKeywordLength          = 64
)

var validTagKeywordRe = regexp.MustCompile(`^[A-Za-z0-9$_\-.]{1,64}$`)

// InvalidParamsError indicates invalid MCP tool arguments mapping to JSON-RPC -32602.
type InvalidParamsError struct {
	Message string
}

func (e *InvalidParamsError) Error() string {
	return e.Message
}

func newInvalidParamsError(format string, args ...any) error {
	return &InvalidParamsError{Message: fmt.Sprintf(format, args...)}
}

// ValidateTagKeyword validates a tag string against RFC 3501 flag-keyword constraints.
func ValidateTagKeyword(tag string) error {
	if tag == "" {
		return newInvalidParamsError("invalid tag syntax: tag must not be empty")
	}
	if strings.HasPrefix(tag, `\`) {
		return newInvalidParamsError("invalid tag syntax: system flag disallowed (%q)", tag)
	}
	if len(tag) > maxTagKeywordLength {
		return newInvalidParamsError("invalid tag syntax: tag exceeds %d characters (%q)", maxTagKeywordLength, tag)
	}
	for _, r := range tag {
		if r == '\r' || r == '\n' || r < 0x20 || r == 0x7f {
			return newInvalidParamsError("invalid tag syntax: control characters disallowed")
		}
		if r == ' ' || r == '\t' {
			return newInvalidParamsError("invalid tag syntax: whitespace disallowed (%q)", tag)
		}
		if r == '*' || r == '%' {
			return newInvalidParamsError("invalid tag syntax: wildcard characters disallowed (%q)", tag)
		}
	}
	if !validTagKeywordRe.MatchString(tag) {
		return newInvalidParamsError("invalid tag syntax: disallowed characters in tag %q (allowed: [A-Za-z0-9$_\\-.])", tag)
	}
	if database.IsStandardSystemFlagToken(tag) {
		return newInvalidParamsError("invalid tag syntax: standard IMAP/Maildir system flag token disallowed (%q)", tag)
	}
	return nil
}

func (s *Server) canonicalizeTagKey(tag string) string {
	if s.Config != nil {
		for k := range s.Config.Tags {
			if strings.EqualFold(k, tag) {
				return k
			}
		}
	}
	return tag
}

func parseStrictStringArray(args map[string]interface{}, key string) ([]string, bool, error) {
	raw, exists := args[key]
	if !exists || raw == nil {
		return nil, false, nil
	}
	switch v := raw.(type) {
	case []interface{}:
		out := make([]string, 0, len(v))
		for i, item := range v {
			str, ok := item.(string)
			if !ok {
				return nil, true, newInvalidParamsError("%s[%d] must be a string", key, i)
			}
			out = append(out, str)
		}
		return out, true, nil
	case []string:
		out := make([]string, len(v))
		copy(out, v)
		return out, true, nil
	default:
		return nil, true, newInvalidParamsError("%s must be an array of strings", key)
	}
}

func formatStringListForLog(items []string) string {
	if len(items) == 0 {
		return "[]"
	}
	quoted := make([]string, len(items))
	for i, s := range items {
		quoted[i] = strconv.Quote(s)
	}
	return "[" + strings.Join(quoted, ",") + "]"
}

func sortTagList(tags []string) []string {
	out := make([]string, len(tags))
	copy(out, tags)
	sort.SliceStable(out, func(i, j int) bool {
		ri := config.TagPriorityRank(out[i])
		rj := config.TagPriorityRank(out[j])
		if ri != rj {
			return ri < rj
		}
		return strings.ToLower(out[i]) < strings.ToLower(out[j])
	})
	return out
}

func (s *Server) resolveMessageNoHydrate(idOrMsgID string) (*database.Message, error) {
	if s.DB == nil {
		return nil, fmt.Errorf("database not initialized")
	}
	trimmed := strings.TrimSpace(idOrMsgID)
	if trimmed == "" {
		return nil, fmt.Errorf("message ID cannot be empty")
	}
	if idVal, err := strconv.ParseInt(trimmed, 10, 64); err == nil {
		if msg, err := s.DB.GetMessageByID(idVal); err == nil && msg != nil {
			return msg, nil
		}
	}
	cleaned := parser.CleanMsgID(trimmed)
	if cleaned != "" {
		if msg, err := s.DB.GetMessageByMsgID(cleaned); err == nil && msg != nil {
			return msg, nil
		}
	}
	if cleaned != trimmed {
		if msg, err := s.DB.GetMessageByMsgID(trimmed); err == nil && msg != nil {
			return msg, nil
		}
	}
	return nil, fmt.Errorf("message not found: %s", trimmed)
}

func (s *Server) toolUpdateEmailTags(args map[string]interface{}) (interface{}, error) {
	t0 := time.Now()
	logReject := func(idCount int, addRaw, removeRaw []string, reason string) {
		durMs := time.Since(t0).Seconds() * 1000.0
		s.writeAccessLog("WARNING", fmt.Sprintf(
			"MCP access: update_email_tags(ids=%d, add=%s, remove=%s) -> REJECTED (%s) (%.1fms)",
			idCount, formatStringListForLog(addRaw), formatStringListForLog(removeRaw), reason, durMs,
		))
	}

	rawMsgIDs, hasMsgIDs, err := parseStrictStringArray(args, "message_ids")
	if err != nil {
		logReject(0, nil, nil, err.Error())
		return nil, err
	}
	if !hasMsgIDs || len(rawMsgIDs) == 0 {
		err = newInvalidParamsError("message_ids is required and must contain between 1 and %d items", maxUpdateEmailTagsMessageIDs)
		logReject(0, nil, nil, err.Error())
		return nil, err
	}
	if len(rawMsgIDs) > maxUpdateEmailTagsMessageIDs {
		err = newInvalidParamsError("message_ids exceeds maximum of %d items", maxUpdateEmailTagsMessageIDs)
		logReject(len(rawMsgIDs), nil, nil, err.Error())
		return nil, err
	}
	for i, mid := range rawMsgIDs {
		if strings.TrimSpace(mid) == "" {
			err = newInvalidParamsError("message_ids[%d] must not be empty", i)
			logReject(len(rawMsgIDs), nil, nil, err.Error())
			return nil, err
		}
	}

	addRaw, _, err := parseStrictStringArray(args, "add_tags")
	if err != nil {
		logReject(len(rawMsgIDs), nil, nil, err.Error())
		return nil, err
	}
	removeRaw, _, err := parseStrictStringArray(args, "remove_tags")
	if err != nil {
		logReject(len(rawMsgIDs), addRaw, nil, err.Error())
		return nil, err
	}
	replaceRaw, hasReplace, err := parseStrictStringArray(args, "replace_tags")
	if err != nil {
		logReject(len(rawMsgIDs), addRaw, removeRaw, err.Error())
		return nil, err
	}

	if hasReplace && (len(addRaw) > 0 || len(removeRaw) > 0) {
		err = newInvalidParamsError("replace_tags cannot be combined with add_tags or remove_tags")
		logReject(len(rawMsgIDs), addRaw, removeRaw, err.Error())
		return nil, err
	}
	if !hasReplace && len(addRaw) == 0 && len(removeRaw) == 0 {
		err = newInvalidParamsError("must specify at least one of add_tags, remove_tags, or replace_tags")
		logReject(len(rawMsgIDs), addRaw, removeRaw, err.Error())
		return nil, err
	}

	scope := strings.TrimSpace(getString(args, "scope"))
	if scope == "" {
		scope = "message"
	}
	if scope != "message" && scope != "thread" {
		err = newInvalidParamsError("invalid scope %q: expected 'message' or 'thread'", scope)
		logReject(len(rawMsgIDs), addRaw, removeRaw, err.Error())
		return nil, err
	}

	folderPtr := getStringPtr(args, "folder")
	if folderPtr != nil {
		trimmedFolder := strings.TrimSpace(*folderPtr)
		if trimmedFolder == "" {
			folderPtr = nil
		} else {
			folderPtr = &trimmedFolder
		}
	}
	dryRun := getBool(args, "dry_run", false)

	// Validate all tag strings and check for overlap between add_tags and remove_tags.
	addSet := make(map[string]string, len(addRaw))
	addTags := make([]string, 0, len(addRaw))
	for _, raw := range addRaw {
		if err := ValidateTagKeyword(raw); err != nil {
			logReject(len(rawMsgIDs), addRaw, removeRaw, err.Error())
			return nil, err
		}
		canon := s.canonicalizeTagKey(raw)
		lower := strings.ToLower(canon)
		if _, dup := addSet[lower]; !dup {
			addSet[lower] = canon
			addTags = append(addTags, canon)
		}
	}

	removeSet := make(map[string]string, len(removeRaw))
	removeTags := make([]string, 0, len(removeRaw))
	for _, raw := range removeRaw {
		if err := ValidateTagKeyword(raw); err != nil {
			logReject(len(rawMsgIDs), addRaw, removeRaw, err.Error())
			return nil, err
		}
		canon := s.canonicalizeTagKey(raw)
		lower := strings.ToLower(canon)
		if _, conflict := addSet[lower]; conflict {
			err = newInvalidParamsError("tag %q cannot appear in both add_tags and remove_tags", canon)
			logReject(len(rawMsgIDs), addRaw, removeRaw, err.Error())
			return nil, err
		}
		if _, dup := removeSet[lower]; !dup {
			removeSet[lower] = canon
			removeTags = append(removeTags, canon)
		}
	}

	var replaceTags []string
	if hasReplace {
		repSeen := make(map[string]struct{}, len(replaceRaw))
		replaceTags = make([]string, 0, len(replaceRaw))
		for _, raw := range replaceRaw {
			if err := ValidateTagKeyword(raw); err != nil {
				logReject(len(rawMsgIDs), addRaw, removeRaw, err.Error())
				return nil, err
			}
			canon := s.canonicalizeTagKey(raw)
			lower := strings.ToLower(canon)
			if _, dup := repSeen[lower]; !dup {
				repSeen[lower] = struct{}{}
				replaceTags = append(replaceTags, canon)
			}
		}
		if len(replaceTags) > maxTagsPerEmail {
			err = newInvalidParamsError("tag count %d exceeds maximum of %d tags per email", len(replaceTags), maxTagsPerEmail)
			logReject(len(rawMsgIDs), addRaw, removeRaw, err.Error())
			return nil, err
		}
		replaceTags = sortTagList(replaceTags)
	}

	if s.DB == nil {
		return nil, fmt.Errorf("database not initialized")
	}

	// Resolve targets without triggering any IMAP fetch or body hydration.
	notFound := make([]string, 0)
	seedMessages := make([]*database.Message, 0, len(rawMsgIDs))
	for _, rawID := range rawMsgIDs {
		msg, resErr := s.resolveMessageNoHydrate(rawID)
		if resErr != nil || msg == nil {
			notFound = append(notFound, rawID)
			continue
		}
		seedMessages = append(seedMessages, msg)
	}

	expanded := make([]*database.Message, 0, len(seedMessages))
	if scope == "thread" {
		seenThreads := make(map[string]struct{})
		for _, seed := range seedMessages {
			expanded = append(expanded, seed)
			cleanMid := parser.CleanMsgID(seed.MessageID)
			if cleanMid == "" {
				continue
			}
			if _, done := seenThreads[cleanMid]; done {
				continue
			}
			seenThreads[cleanMid] = struct{}{}
			if threadMsgs, tErr := s.DB.GetThreadHierarchy(cleanMid); tErr == nil {
				expanded = append(expanded, threadMsgs...)
			}
		}
	} else {
		expanded = append(expanded, seedMessages...)
	}

	// Collect all copies matching the target Message-IDs (or restricted by folder guard).
	targetByID := make(map[int64]*database.Message)
	seenMsgIDs := make(map[string]struct{})
	addCandidate := func(m *database.Message) {
		if m == nil {
			return
		}
		if folderPtr != nil {
			if m.FolderName != *folderPtr {
				return
			}
		} else if m.IsAIReview || m.FolderName == constants.FolderDrafts || m.FolderName == constants.FolderAIReviews {
			return
		}
		targetByID[m.ID] = m
	}

	for _, m := range expanded {
		addCandidate(m)
		cleanMid := parser.CleanMsgID(m.MessageID)
		if cleanMid == "" {
			continue
		}
		if _, done := seenMsgIDs[cleanMid]; done {
			continue
		}
		seenMsgIDs[cleanMid] = struct{}{}
		if copies, cErr := s.DB.GetMessagesByMsgID(cleanMid); cErr == nil {
			for _, copyMsg := range copies {
				addCandidate(copyMsg)
			}
		}
	}

	targets := make([]*database.Message, 0, len(targetByID))
	for _, m := range targetByID {
		targets = append(targets, m)
	}
	sort.Slice(targets, func(i, j int) bool {
		return targets[i].ID < targets[j].ID
	})

	type plannedUpdate struct {
		msg             *database.Message
		beforeTags      []string
		afterTags       []string
		addedTags       []string
		removedTags     []string
		changed         bool
		newFlags        string
		enqueueIMAPSync bool
	}

	planned := make([]plannedUpdate, 0, len(targets))
	for _, m := range targets {
		beforeTags := database.ExtractUserTagsFromFlags(m.Flags)
		beforeMap := make(map[string]string, len(beforeTags))
		for _, bt := range beforeTags {
			beforeMap[strings.ToLower(bt)] = bt
		}

		var afterTags []string
		if hasReplace {
			afterTags = make([]string, len(replaceTags))
			copy(afterTags, replaceTags)
		} else {
			afterMap := make(map[string]string, len(beforeTags)+len(addTags))
			for _, bt := range beforeTags {
				lower := strings.ToLower(bt)
				if _, shouldRemove := removeSet[lower]; shouldRemove {
					continue
				}
				afterMap[lower] = bt
			}
			for _, at := range addTags {
				lower := strings.ToLower(at)
				if _, exists := afterMap[lower]; !exists {
					afterMap[lower] = at
				}
			}
			afterTags = make([]string, 0, len(afterMap))
			for _, v := range afterMap {
				afterTags = append(afterTags, v)
			}
			afterTags = sortTagList(afterTags)
		}

		if len(afterTags) > maxTagsPerEmail {
			err = newInvalidParamsError("message %d would have %d tags, exceeding maximum of %d", m.ID, len(afterTags), maxTagsPerEmail)
			logReject(len(rawMsgIDs), addRaw, removeRaw, err.Error())
			return nil, err
		}

		afterMap := make(map[string]string, len(afterTags))
		for _, at := range afterTags {
			afterMap[strings.ToLower(at)] = at
		}

		added := make([]string, 0)
		for _, at := range afterTags {
			if _, existed := beforeMap[strings.ToLower(at)]; !existed {
				added = append(added, at)
			}
		}
		removed := make([]string, 0)
		for _, bt := range beforeTags {
			if _, stillPresent := afterMap[strings.ToLower(bt)]; !stillPresent {
				removed = append(removed, bt)
			}
		}

		changed := len(added) > 0 || len(removed) > 0
		sysFlags := database.ExtractSystemFlagsFromFlags(m.Flags)
		newFlagTokens := make([]string, 0, len(sysFlags)+len(afterTags))
		newFlagTokens = append(newFlagTokens, sysFlags...)
		newFlagTokens = append(newFlagTokens, afterTags...)
		newFlags := strings.Join(newFlagTokens, " ")

		hasValidIMAPUID := m.IMAPUID != nil && *m.IMAPUID > 0
		enqueueIMAP := !dryRun && changed && hasValidIMAPUID && !database.IsLocalOnlyFolder(m.FolderName)

		planned = append(planned, plannedUpdate{
			msg:             m,
			beforeTags:      beforeTags,
			afterTags:       afterTags,
			addedTags:       added,
			removedTags:     removed,
			changed:         changed,
			newFlags:        newFlags,
			enqueueIMAPSync: enqueueIMAP,
		})
	}

	updatedCount := 0
	unchangedCount := 0
	ops := make([]database.TagUpdateOp, 0, len(planned))
	sseUpdates := make([]map[string]interface{}, 0, len(planned))
	results := make([]map[string]interface{}, 0, len(planned))

	for _, p := range planned {
		if p.changed {
			updatedCount++
			if !dryRun {
				var uid uint32
				if p.msg.IMAPUID != nil {
					uid = *p.msg.IMAPUID
				}
				ops = append(ops, database.TagUpdateOp{
					EmailID:         p.msg.ID,
					FolderID:        p.msg.FolderID,
					FolderName:      p.msg.FolderName,
					IMAPUID:         uid,
					NewFlags:        p.newFlags,
					AfterTags:       p.afterTags,
					AddTags:         p.addedTags,
					RemoveTags:      p.removedTags,
					EnqueueIMAPSync: p.enqueueIMAPSync,
				})
				sseUpdates = append(sseUpdates, map[string]interface{}{
					"id":         p.msg.ID,
					"folder":     p.msg.FolderName,
					"message_id": p.msg.MessageID,
					"tags":       p.afterTags,
				})
			}
		} else {
			unchangedCount++
		}

		results = append(results, map[string]interface{}{
			"id":               p.msg.ID,
			"message_id":       p.msg.MessageID,
			"folder_name":      p.msg.FolderName,
			"before_tags":      p.beforeTags,
			"after_tags":       p.afterTags,
			"changed":          p.changed,
			"imap_sync_queued": p.enqueueIMAPSync,
		})
	}

	imapSyncQueuedCount := 0
	if !dryRun && len(ops) > 0 {
		queued, dbErr := s.DB.BatchUpdateEmailTags(ops)
		if dbErr != nil {
			return nil, fmt.Errorf("failed to persist tag updates: %w", dbErr)
		}
		imapSyncQueuedCount = queued

		if s.Events != nil && len(sseUpdates) > 0 {
			s.Events.Publish("email_tags_updated", map[string]interface{}{
				"updates": sseUpdates,
			})
		}
	}

	var folderFilterVal interface{}
	folderLog := "None"
	if folderPtr != nil {
		folderFilterVal = *folderPtr
		folderLog = strconv.Quote(*folderPtr)
	}

	durMs := time.Since(t0).Seconds() * 1000.0
	s.writeAccessLog("INFO", fmt.Sprintf(
		"MCP access: update_email_tags(ids=%d, scope=%q, folder=%s, add=%s, remove=%s, dry_run=%v) -> SUCCESS (updated=%d, imap_sync_queued=%d) (%.1fms)",
		len(rawMsgIDs), scope, folderLog, formatStringListForLog(addTags), formatStringListForLog(removeTags), dryRun, updatedCount, imapSyncQueuedCount, durMs,
	))

	return map[string]interface{}{
		"success":               true,
		"dry_run":               dryRun,
		"scope":                 scope,
		"folder_filter":         folderFilterVal,
		"updated_count":         updatedCount,
		"unchanged_count":       unchangedCount,
		"imap_sync_queued_count": imapSyncQueuedCount,
		"results":               results,
		"not_found":             notFound,
	}, nil
}

func (s *Server) toolListTags(args map[string]interface{}) (interface{}, error) {
	t0 := time.Now()
	folderPtr := getStringPtr(args, "folder")
	folderName := ""
	if folderPtr != nil {
		folderName = strings.TrimSpace(*folderPtr)
	}

	tagCounts := make(map[string]int)
	if s.DB != nil {
		counts, err := s.DB.GetTagCounts(folderName)
		if err != nil {
			return nil, fmt.Errorf("failed to query tag counts: %w", err)
		}
		tagCounts = counts
	}

	// Aggregate counts case-insensitively while preserving representative casing.
	lowerCounts := make(map[string]int, len(tagCounts))
	lowerCaseName := make(map[string]string, len(tagCounts))
	for tag, cnt := range tagCounts {
		lower := strings.ToLower(tag)
		lowerCounts[lower] += cnt
		if _, ok := lowerCaseName[lower]; !ok {
			lowerCaseName[lower] = tag
		}
	}

	configuredTags := config.DefaultTags()
	if s.Config != nil && len(s.Config.Tags) > 0 {
		configuredTags = s.Config.Tags
	}

	configuredKeys := make([]string, 0, len(configuredTags))
	configuredLower := make(map[string]struct{}, len(configuredTags))
	for k := range configuredTags {
		configuredKeys = append(configuredKeys, k)
		configuredLower[strings.ToLower(k)] = struct{}{}
	}
	configuredKeys = sortTagList(configuredKeys)

	colorLabels := make([]map[string]interface{}, 0, len(configuredKeys))
	for _, key := range configuredKeys {
		def := configuredTags[key]
		colorLabels = append(colorLabels, map[string]interface{}{
			"tag":          key,
			"display_name": def.Tag,
			"color":        def.Color,
			"count":        lowerCounts[strings.ToLower(key)],
		})
	}

	customKeys := make([]string, 0)
	for lower, name := range lowerCaseName {
		if _, isConfigured := configuredLower[lower]; !isConfigured {
			customKeys = append(customKeys, name)
		}
	}
	sort.Slice(customKeys, func(i, j int) bool {
		return strings.ToLower(customKeys[i]) < strings.ToLower(customKeys[j])
	})

	customTags := make([]map[string]interface{}, 0, len(customKeys))
	for _, tag := range customKeys {
		customTags = append(customTags, map[string]interface{}{
			"tag":   tag,
			"count": lowerCounts[strings.ToLower(tag)],
		})
	}

	folderDisp := "None"
	if folderName != "" {
		folderDisp = strconv.Quote(folderName)
	}
	durMs := time.Since(t0).Seconds() * 1000.0
	s.writeAccessLog("INFO", fmt.Sprintf(
		"MCP access: list_tags(folder=%s) -> %d color_labels, %d custom_tags (%.1fms)",
		folderDisp, len(colorLabels), len(customTags), durMs,
	))

	return map[string]interface{}{
		"color_labels": colorLabels,
		"custom_tags":  customTags,
	}, nil
}

func (s *Server) toolGetUserIdentity(args map[string]interface{}) (interface{}, error) {
	t0 := time.Now()
	var identity config.UserIdentity
	if s.Config != nil {
		identity = s.Config.UserIdentity()
	} else {
		identity = config.NewDefaultConfig().UserIdentity()
	}
	if identity.Aliases == nil {
		identity.Aliases = make([]string, 0)
	}

	durMs := time.Since(t0).Seconds() * 1000.0
	s.writeAccessLog("INFO", fmt.Sprintf(
		"MCP access: get_user_identity() -> primary=%q, aliases=%d (%.1fms)",
		identity.PrimaryEmail, len(identity.Aliases), durMs,
	))

	return map[string]interface{}{
		"display_name":  identity.DisplayName,
		"primary_email": identity.PrimaryEmail,
		"aliases":       identity.Aliases,
	}, nil
}
