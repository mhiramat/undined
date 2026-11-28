package mcp

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"undined-go/internal/storage/database"
	"undined-go/internal/storage/parser"
)

var (
	trailerLineRe = regexp.MustCompile(`^([A-Za-z][A-Za-z0-9-]*):\s+(.+)$`)
	diffstatRowRe = regexp.MustCompile(`^\s+\S.*\s\|\s+(?:\d+\s*[+-]*|Bin\s+.*)$`)
	diffstatSumRe = regexp.MustCompile(`^\s*\d+\s+files?\s+changed\b`)
)

var canonicalTrailerKeys = map[string]string{
	"fixes":           "Fixes",
	"signed-off-by":   "Signed-off-by",
	"reviewed-by":     "Reviewed-by",
	"acked-by":        "Acked-by",
	"tested-by":       "Tested-by",
	"reported-by":     "Reported-by",
	"suggested-by":    "Suggested-by",
	"co-developed-by": "Co-developed-by",
	"link":            "Link",
	"closes":          "Closes",
	"buglink":         "BugLink",
	"resolves":        "Resolves",
	"cc":              "Cc",
	"base-commit":     "base-commit",
	"change-id":       "Change-Id",
}

// ComputeThreadID computes a deterministic short thread identifier from a canonical root Message-ID.
func ComputeThreadID(rootMsgID string) string {
	clean := parser.CleanMsgID(rootMsgID)
	if clean == "" {
		clean = strings.TrimSpace(rootMsgID)
	}
	sum := sha256.Sum256([]byte(clean))
	return "thr_" + hex.EncodeToString(sum[:])[:8]
}

func findDiffStartLine(lines []string) int {
	for i, line := range lines {
		if strings.HasPrefix(line, "diff --git ") {
			return i
		}
		if strings.HasPrefix(line, "Index: ") && i+1 < len(lines) && strings.HasPrefix(lines[i+1], "====") {
			return i
		}
		if (strings.HasPrefix(line, "--- a/") || strings.HasPrefix(line, "--- /dev/null")) &&
			i+1 < len(lines) && (strings.HasPrefix(lines[i+1], "+++ b/") || strings.HasPrefix(lines[i+1], "+++ /dev/null")) {
			return i
		}
	}
	return -1
}

// StripUnifiedDiff removes unified diff hunks starting at the first unquoted diff header
// while preserving commit message text, trailers, and diffstat intact.
func StripUnifiedDiff(body string) string {
	if body == "" {
		return ""
	}
	normalized := strings.ReplaceAll(body, "\r\n", "\n")
	lines := strings.Split(normalized, "\n")
	diffIdx := findDiffStartLine(lines)
	if diffIdx == -1 {
		return body
	}
	kept := strings.TrimRight(strings.Join(lines[:diffIdx], "\n"), "\n")
	if kept != "" {
		return kept + "\n"
	}
	return ""
}

// ExtractEmailTrailers extracts recognized kernel commit/review trailers (Fixes, Reviewed-by,
// Acked-by, Reported-by, Cc-stable, etc.) from non-quoted text preceding any unified diff.
func ExtractEmailTrailers(body string) map[string][]string {
	result := make(map[string][]string)
	if body == "" {
		return result
	}
	normalized := strings.ReplaceAll(body, "\r\n", "\n")
	lines := strings.Split(normalized, "\n")
	endIdx := len(lines)
	if diffIdx := findDiffStartLine(lines); diffIdx != -1 {
		endIdx = diffIdx
	}

	seen := make(map[string]map[string]struct{})
	addTrailer := func(key, val string) {
		val = strings.TrimSpace(val)
		if val == "" {
			return
		}
		if seen[key] == nil {
			seen[key] = make(map[string]struct{})
		}
		if _, dup := seen[key][val]; dup {
			return
		}
		seen[key][val] = struct{}{}
		result[key] = append(result[key], val)
	}

	for i := 0; i < endIdx; i++ {
		trimmed := strings.TrimSpace(lines[i])
		if trimmed == "---" || strings.HasPrefix(trimmed, ">") {
			if trimmed == "---" {
				break
			}
			continue
		}
		m := trailerLineRe.FindStringSubmatch(trimmed)
		if len(m) != 3 {
			continue
		}
		canonKey, ok := canonicalTrailerKeys[strings.ToLower(m[1])]
		if !ok {
			continue
		}
		val := strings.TrimSpace(m[2])
		addTrailer(canonKey, val)
		if canonKey == "Cc" {
			lowerVal := strings.ToLower(val)
			if strings.Contains(lowerVal, "stable@vger.kernel.org") || strings.Contains(lowerVal, "stable@kernel.org") {
				addTrailer("Cc-stable", val)
			}
		}
	}

	return result
}

// ExtractDiffstat returns the file change statistics block located between the patch "---"
// separator and the start of the unified diff.
func ExtractDiffstat(body string) string {
	if body == "" {
		return ""
	}
	normalized := strings.ReplaceAll(body, "\r\n", "\n")
	lines := strings.Split(normalized, "\n")
	diffIdx := findDiffStartLine(lines)
	if diffIdx == -1 {
		diffIdx = len(lines)
	}

	sepIdx := -1
	for i := 0; i < diffIdx; i++ {
		if strings.TrimSpace(lines[i]) == "---" {
			sepIdx = i
			break
		}
	}
	if sepIdx == -1 || sepIdx+1 >= diffIdx {
		return ""
	}

	statLines := make([]string, 0)
	for i := sepIdx + 1; i < diffIdx; i++ {
		line := lines[i]
		if diffstatRowRe.MatchString(line) || diffstatSumRe.MatchString(line) {
			statLines = append(statLines, line)
		}
	}
	return strings.Join(statLines, "\n")
}

// tryHydrateFromLocalDisk hydrates a message's plaintext body strictly from its local Maildir
// file if present on disk, never performing any IMAP network request. It reports whether the
// message body is locally hydrated.
func (s *Server) tryHydrateFromLocalDisk(m *database.Message) bool {
	if m == nil {
		return false
	}
	if m.BodyPlain != "" {
		return true
	}
	if m.FilePath == "" {
		return false
	}
	data, err := os.ReadFile(m.FilePath)
	if err != nil {
		return false
	}
	parsed, err := parser.ParseEmail(data)
	if err != nil {
		return false
	}
	m.BodyPlain = parsed.BodyPlain
	if m.BodyPlain != "" && s.DB != nil {
		// Best-effort local cache write; returning hydrated body does not depend on persisting it.
		_ = s.DB.UpdateMessageSubjectAndBody(m.ID, m.Subject, m.BodyPlain)
	}
	return true
}

func (s *Server) selfAddressSet() map[string]struct{} {
	set := make(map[string]struct{})
	if s.Config == nil {
		return set
	}
	id := s.Config.UserIdentity()
	if p := strings.ToLower(strings.TrimSpace(id.PrimaryEmail)); p != "" {
		set[p] = struct{}{}
	}
	for _, a := range id.Aliases {
		if clean := strings.ToLower(strings.TrimSpace(a)); clean != "" {
			set[clean] = struct{}{}
		}
	}
	return set
}

func (s *Server) expandExcludeFrom(raw []string) []string {
	if len(raw) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(raw))
	out := make([]string, 0, len(raw))
	add := func(addr string) {
		lower := strings.ToLower(strings.TrimSpace(addr))
		if lower == "" {
			return
		}
		if _, dup := seen[lower]; dup {
			return
		}
		seen[lower] = struct{}{}
		out = append(out, lower)
	}
	for _, item := range raw {
		trimmed := strings.TrimSpace(item)
		if strings.EqualFold(trimmed, "self") {
			for selfAddr := range s.selfAddressSet() {
				add(selfAddr)
			}
			continue
		}
		add(trimmed)
	}
	return out
}

func resolveMessageRootID(m *database.Message) string {
	if m == nil {
		return ""
	}
	if refs := parser.ExtractMsgIDs(m.ReferencesHeader); len(refs) > 0 {
		return refs[0]
	}
	if inReplies := parser.ExtractMsgIDs(m.InReplyTo); len(inReplies) > 0 {
		return inReplies[0]
	}
	if clean := parser.CleanMsgID(m.MessageID); clean != "" {
		return clean
	}
	return fmt.Sprintf("local-%d", m.ID)
}

type threadCursor struct {
	Offset int `json:"offset"`
}

func encodeThreadCursor(offset int) string {
	// Marshaling a plain integer struct cannot fail.
	b, _ := json.Marshal(threadCursor{Offset: offset})
	return base64.RawURLEncoding.EncodeToString(b)
}

func decodeThreadCursor(raw string) (int, error) {
	b, err := base64.RawURLEncoding.DecodeString(raw)
	if err != nil {
		return 0, newInvalidParamsError("invalid cursor: %v", err)
	}
	var c threadCursor
	if err := json.Unmarshal(b, &c); err != nil || c.Offset < 0 {
		return 0, newInvalidParamsError("invalid cursor payload")
	}
	return c.Offset, nil
}

func isMetaHydrated(m *database.ThreadMessageMeta) bool {
	if m.HasBodyPlain {
		return true
	}
	if m.FilePath != "" {
		if _, err := os.Stat(m.FilePath); err == nil {
			return true
		}
	}
	return false
}

func tagSetsEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	m := make(map[string]struct{}, len(a))
	for _, t := range a {
		m[strings.ToLower(t)] = struct{}{}
	}
	for _, t := range b {
		if _, ok := m[strings.ToLower(t)]; !ok {
			return false
		}
	}
	return true
}

func (s *Server) toolListThreads(args map[string]interface{}) (interface{}, error) {
	t0 := time.Now()
	folderPtr := getStringPtr(args, "folder")
	folderName := ""
	if folderPtr != nil {
		folderName = strings.TrimSpace(*folderPtr)
	}

	limit := getInt(args, "limit", 30)
	if limit < 1 || limit > 200 {
		return nil, newInvalidParamsError("limit must be between 1 and 200 (got %d)", limit)
	}

	cursorPtr := getStringPtr(args, "cursor")
	offset := 0
	if cursorPtr != nil && strings.TrimSpace(*cursorPtr) != "" {
		decodedOffset, err := decodeThreadCursor(strings.TrimSpace(*cursorPtr))
		if err != nil {
			return nil, err
		}
		offset = decodedOffset
	}

	excludeRaw, _, err := parseStrictStringArray(args, "exclude_from")
	if err != nil {
		return nil, err
	}
	hasTagsRaw, _, err := parseStrictStringArray(args, "has_tags")
	if err != nil {
		return nil, err
	}
	withoutTagsRaw, _, err := parseStrictStringArray(args, "without_tags")
	if err != nil {
		return nil, err
	}
	patchesOnly := getBool(args, "patches_only", false)

	sortMode := strings.TrimSpace(getString(args, "sort"))
	if sortMode == "" {
		sortMode = "latest_date_desc"
	}
	if sortMode != "latest_date_desc" && sortMode != "latest_date_asc" {
		return nil, newInvalidParamsError("invalid sort %q: expected 'latest_date_desc' or 'latest_date_asc'", sortMode)
	}

	if s.DB == nil {
		return map[string]interface{}{
			"total_threads": 0,
			"returned":      0,
			"next_cursor":   nil,
			"threads":       make([]map[string]interface{}, 0),
		}, nil
	}

	allMeta, err := s.DB.ListAllMessagesMeta()
	if err != nil {
		return nil, fmt.Errorf("failed to query thread messages: %w", err)
	}

	// Build Message-ID -> representative metadata map to resolve multi-hop thread roots accurately.
	byCleanID := make(map[string]*database.ThreadMessageMeta, len(allMeta))
	for _, m := range allMeta {
		clean := parser.CleanMsgID(m.MessageID)
		if clean == "" {
			continue
		}
		existing, ok := byCleanID[clean]
		if !ok || (!existing.HasBodyPlain && m.HasBodyPlain) {
			byCleanID[clean] = m
		}
	}

	parentOf := func(m *database.ThreadMessageMeta) string {
		self := parser.CleanMsgID(m.MessageID)
		if refs := parser.ExtractMsgIDs(m.ReferencesHeader); len(refs) > 0 {
			for _, r := range refs {
				if r != "" && r != self {
					return r
				}
			}
		}
		if inReplies := parser.ExtractMsgIDs(m.InReplyTo); len(inReplies) > 0 {
			for _, r := range inReplies {
				if r != "" && r != self {
					return r
				}
			}
		}
		return ""
	}

	rootCache := make(map[string]string, len(allMeta))
	findRoot := func(m *database.ThreadMessageMeta) string {
		self := parser.CleanMsgID(m.MessageID)
		if self != "" {
			if cached, ok := rootCache[self]; ok {
				return cached
			}
		}
		visited := make(map[string]struct{})
		curr := m
		currID := self
		if currID == "" {
			currID = fmt.Sprintf("local-%d", m.ID)
		}
		for {
			visited[currID] = struct{}{}
			p := parentOf(curr)
			if p == "" {
				break
			}
			if _, loop := visited[p]; loop {
				break
			}
			currID = p
			parentMeta, exists := byCleanID[p]
			if !exists {
				break
			}
			curr = parentMeta
		}
		if self != "" {
			rootCache[self] = currID
		}
		return currID
	}

	type threadBucket struct {
		rootID     string
		allMsgs    []*database.ThreadMessageMeta
		folderMsgs []*database.ThreadMessageMeta
	}
	buckets := make(map[string]*threadBucket)
	for _, m := range allMeta {
		rootID := findRoot(m)
		b, ok := buckets[rootID]
		if !ok {
			b = &threadBucket{rootID: rootID}
			buckets[rootID] = b
		}
		b.allMsgs = append(b.allMsgs, m)
		if folderName == "" || m.FolderName == folderName {
			b.folderMsgs = append(b.folderMsgs, m)
		}
	}

	excludeSet := make(map[string]struct{})
	for _, addr := range s.expandExcludeFrom(excludeRaw) {
		excludeSet[addr] = struct{}{}
	}
	hasTagSet := make(map[string]struct{}, len(hasTagsRaw))
	for _, t := range hasTagsRaw {
		if trimmed := strings.ToLower(strings.TrimSpace(t)); trimmed != "" {
			hasTagSet[trimmed] = struct{}{}
		}
	}
	withoutTagSet := make(map[string]struct{}, len(withoutTagsRaw))
	for _, t := range withoutTagsRaw {
		if trimmed := strings.ToLower(strings.TrimSpace(t)); trimmed != "" {
			withoutTagSet[trimmed] = struct{}{}
		}
	}
	selfAddrs := s.selfAddressSet()

	type threadSummary struct {
		threadID             string
		rootMsgID            string
		subject              string
		authorName           string
		authorAddr           string
		latestDate           string
		latestEpoch          int64
		patchCountInFolder   int
		totalMessages        int
		replyCount           int
		folderMessageIDs     []int64
		tags                 []string
		tagsConsistent       bool
		hasMyReply           bool
		hasUnhydratedReplies bool
	}

	summaries := make([]threadSummary, 0, len(buckets))
	for _, b := range buckets {
		if len(b.folderMsgs) == 0 {
			continue
		}

		// Pick root representative message for subject/author.
		var rootRep *database.ThreadMessageMeta
		for _, m := range b.allMsgs {
			if parser.CleanMsgID(m.MessageID) == b.rootID {
				if rootRep == nil || (!rootRep.HasBodyPlain && m.HasBodyPlain) {
					rootRep = m
				}
			}
		}
		if rootRep == nil {
			// Fallback to earliest non-reply message, else earliest message overall.
			for _, m := range b.allMsgs {
				if !isReplySubject(m.Subject) {
					if rootRep == nil || m.EpochTimestamp < rootRep.EpochTimestamp {
						rootRep = m
					}
				}
			}
		}
		if rootRep == nil {
			rootRep = b.allMsgs[0]
		}

		// Apply exclude_from on root author.
		if len(excludeSet) > 0 {
			if _, excluded := excludeSet[strings.ToLower(strings.TrimSpace(rootRep.FromAddr))]; excluded {
				continue
			}
		}

		// Compute folder-scoped stats and tags.
		folderMsgIDs := make([]int64, 0, len(b.folderMsgs))
		patchInFolderSeen := make(map[string]struct{})
		patchCountInFolder := 0
		tagUnionMap := make(map[string]string)
		var firstTagSet []string
		tagsConsistent := true

		for idx, m := range b.folderMsgs {
			folderMsgIDs = append(folderMsgIDs, m.ID)
			if m.IsPatch {
				if folderName == "" {
					key := parser.CleanMsgID(m.MessageID)
					if key == "" {
						key = fmt.Sprintf("id-%d", m.ID)
					}
					if _, dup := patchInFolderSeen[key]; !dup {
						patchInFolderSeen[key] = struct{}{}
						patchCountInFolder++
					}
				} else {
					patchCountInFolder++
				}
			}
			mTags := database.ExtractUserTagsFromFlags(m.Flags)
			if idx == 0 {
				firstTagSet = mTags
			} else if !tagSetsEqual(firstTagSet, mTags) {
				tagsConsistent = false
			}
			for _, t := range mTags {
				lower := strings.ToLower(t)
				if _, exists := tagUnionMap[lower]; !exists {
					tagUnionMap[lower] = t
				}
			}
		}

		if patchesOnly && patchCountInFolder == 0 {
			continue
		}

		unionTags := make([]string, 0, len(tagUnionMap))
		for _, t := range tagUnionMap {
			unionTags = append(unionTags, t)
		}
		unionTags = sortTagList(unionTags)

		// Apply has_tags / without_tags filters.
		if len(hasTagSet) > 0 {
			matched := false
			for _, t := range unionTags {
				if _, ok := hasTagSet[strings.ToLower(t)]; ok {
					matched = true
					break
				}
			}
			if !matched {
				continue
			}
		}
		if len(withoutTagSet) > 0 {
			excluded := false
			for _, t := range unionTags {
				if _, ok := withoutTagSet[strings.ToLower(t)]; ok {
					excluded = true
					break
				}
			}
			if excluded {
				continue
			}
		}

		// Deduplicate across all folders in the thread for thread-wide metrics.
		type uniqueMsgInfo struct {
			cleanID  string
			rep      *database.ThreadMessageMeta
			hydrated bool
			isPatch  bool
		}
		uniqueMsgs := make(map[string]*uniqueMsgInfo)
		latestEpoch := int64(0)
		latestDate := ""
		hasMyReply := false

		for _, m := range b.allMsgs {
			if m.EpochTimestamp > latestEpoch || (latestEpoch == 0 && latestDate == "") {
				latestEpoch = m.EpochTimestamp
				latestDate = m.Date
			}
			clean := parser.CleanMsgID(m.MessageID)
			if clean == "" {
				clean = fmt.Sprintf("id-%d", m.ID)
			}
			hydrated := isMetaHydrated(m)
			info, exists := uniqueMsgs[clean]
			if !exists {
				info = &uniqueMsgInfo{
					cleanID:  clean,
					rep:      m,
					hydrated: hydrated,
					isPatch:  m.IsPatch,
				}
				uniqueMsgs[clean] = info
			} else {
				if hydrated {
					info.hydrated = true
					if !isMetaHydrated(info.rep) {
						info.rep = m
					}
				}
				if m.IsPatch {
					info.isPatch = true
				}
			}
			if m.IsReplied {
				hasMyReply = true
			}
		}

		replyCount := 0
		hasUnhydratedReplies := false
		for clean, info := range uniqueMsgs {
			isRoot := clean == b.rootID
			isReply := !isRoot && (isReplySubject(info.rep.Subject) || !info.isPatch)
			if isReply {
				replyCount++
				if _, isSelf := selfAddrs[strings.ToLower(strings.TrimSpace(info.rep.FromAddr))]; isSelf {
					hasMyReply = true
				}
				if !info.hydrated {
					hasUnhydratedReplies = true
				}
			}
		}

		summaries = append(summaries, threadSummary{
			threadID:             ComputeThreadID(b.rootID),
			rootMsgID:            b.rootID,
			subject:              rootRep.Subject,
			authorName:           rootRep.FromName,
			authorAddr:           rootRep.FromAddr,
			latestDate:           latestDate,
			latestEpoch:          latestEpoch,
			patchCountInFolder:   patchCountInFolder,
			totalMessages:        len(uniqueMsgs),
			replyCount:           replyCount,
			folderMessageIDs:     folderMsgIDs,
			tags:                 unionTags,
			tagsConsistent:       tagsConsistent,
			hasMyReply:           hasMyReply,
			hasUnhydratedReplies: hasUnhydratedReplies,
		})
	}

	sort.Slice(summaries, func(i, j int) bool {
		if summaries[i].latestEpoch != summaries[j].latestEpoch {
			if sortMode == "latest_date_asc" {
				return summaries[i].latestEpoch < summaries[j].latestEpoch
			}
			return summaries[i].latestEpoch > summaries[j].latestEpoch
		}
		return summaries[i].threadID < summaries[j].threadID
	})

	totalThreads := len(summaries)
	if offset > totalThreads {
		offset = totalThreads
	}
	end := offset + limit
	if end > totalThreads {
		end = totalThreads
	}
	page := summaries[offset:end]

	var nextCursor interface{}
	if end < totalThreads {
		nextCursor = encodeThreadCursor(end)
	}

	threadMaps := make([]map[string]interface{}, 0, len(page))
	for _, ts := range page {
		threadMaps = append(threadMaps, map[string]interface{}{
			"thread_id":              ts.threadID,
			"root_message_id":        ts.rootMsgID,
			"subject":                ts.subject,
			"author_name":            ts.authorName,
			"author_addr":            ts.authorAddr,
			"latest_date":            ts.latestDate,
			"patch_count_in_folder":  ts.patchCountInFolder,
			"total_messages_in_thread": ts.totalMessages,
			"reply_count":            ts.replyCount,
			"folder_message_ids":     ts.folderMessageIDs,
			"tags":                   ts.tags,
			"tags_consistent":        ts.tagsConsistent,
			"has_my_reply":           ts.hasMyReply,
			"has_unhydrated_replies": ts.hasUnhydratedReplies,
		})
	}

	folderLog := "None"
	if folderName != "" {
		folderLog = strconv.Quote(folderName)
	}
	durMs := time.Since(t0).Seconds() * 1000.0
	s.writeAccessLog("INFO", fmt.Sprintf(
		"MCP access: list_threads(folder=%s, exclude_from=%s, without_tags=%s) -> %d threads (%.1fms)",
		folderLog, formatStringListForLog(excludeRaw), formatStringListForLog(withoutTagsRaw), len(threadMaps), durMs,
	))

	return map[string]interface{}{
		"total_threads": totalThreads,
		"returned":      len(threadMaps),
		"next_cursor":   nextCursor,
		"threads":       threadMaps,
	}, nil
}

func getBoolPtr(args map[string]interface{}, key string) *bool {
	val, ok := args[key]
	if !ok || val == nil {
		return nil
	}
	if b, ok := val.(bool); ok {
		return &b
	}
	return nil
}
