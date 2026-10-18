package api

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"

	"undined-go/internal/config"
	"undined-go/internal/constants"
	"undined-go/internal/lkml/patch"
	"undined-go/internal/storage/database"
	"undined-go/internal/storage/parser"
)

func (s *Server) resolveMessage(idOrMsgID string) (*database.Message, error) {
	if unescaped, err := url.PathUnescape(idOrMsgID); err == nil && unescaped != "" {
		idOrMsgID = unescaped
	}
	cleanID := parser.CleanMsgID(idOrMsgID)
	if cleanID == "" {
		cleanID = idOrMsgID
	}

	if num, err := strconv.ParseInt(idOrMsgID, 10, 64); err == nil {
		msg, err := s.DB.GetMessageByID(num)
		if err == nil && msg != nil {
			return msg, nil
		}
	}

	msg, err := s.DB.GetMessageByMsgID(cleanID)
	if err == nil && msg != nil {
		return msg, nil
	}

	if cleanID != idOrMsgID {
		return s.DB.GetMessageByMsgID(idOrMsgID)
	}
	return nil, fmt.Errorf("message not found")
}

func (s *Server) attachTagInfo(m *database.Message) {
	if m == nil {
		return
	}
	if s.Config != nil {
		tagInfo := s.Config.ResolveTag(m.Flags)
		if tagInfo != nil {
			m.TagKey = &tagInfo.Key
			m.TagName = &tagInfo.Tag
			m.TagColor = &tagInfo.Color
		}
	}
	if m.ReviewCommitID != "" {
		m.ReviewCommitIDHyphen = m.ReviewCommitID
	}
}

func (s *Server) handleListMessages(w http.ResponseWriter, r *http.Request) {
	if msgID := r.URL.Query().Get("msgid"); msgID != "" {
		msg, err := s.resolveMessage(msgID)
		if err != nil || msg == nil {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode([]*database.Message{})
			return
		}
		s.attachTagInfo(msg)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode([]*database.Message{msg})
		return
	}

	var folderID *int64
	if fStr := r.URL.Query().Get("folder_id"); fStr != "" {
		if id, err := strconv.ParseInt(fStr, 10, 64); err == nil {
			folderID = &id
		}
	}

	limit := 0
	if lStr := r.URL.Query().Get("limit"); lStr != "" {
		if l, err := strconv.Atoi(lStr); err == nil && l > 0 {
			limit = l
		}
	}

	offset := 0
	if oStr := r.URL.Query().Get("offset"); oStr != "" {
		if o, err := strconv.Atoi(oStr); err == nil && o >= 0 {
			offset = o
		}
	}

	threaded := true
	if r.URL.Query().Get("threaded") == "false" {
		threaded = false
	}

	sortBy := r.URL.Query().Get("sort_by")
	if sortBy == "" {
		sortBy = "date"
	}

	sortOrder := r.URL.Query().Get("sort_order")
	if sortOrder == "" {
		sortOrder = "desc"
	}

	msgs, err := s.DB.ListMessagesWithOptions(folderID, limit, offset, sortBy, sortOrder, threaded)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	for _, m := range msgs {
		s.attachTagInfo(m)
	}

	if msgs == nil {
		msgs = []*database.Message{}
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(msgs)
}

func (s *Server) handleGetMessageDetail(w http.ResponseWriter, r *http.Request) {
	idParam := chi.URLParam(r, "id")
	msg, err := s.resolveMessage(idParam)
	if err != nil || msg == nil {
		http.Error(w, "Message not found", http.StatusNotFound)
		return
	}

	// Auto-hydrate if file is missing but IMAP UID is known
	if msg.FilePath == "" && msg.IMAPUID != nil && s.IMAP != nil {
		folder, fErr := s.DB.GetFolderByID(msg.FolderID)
		if fErr == nil && folder != nil {
			hydrated, hErr := s.IMAP.FetchAndHydrateMessage(folder.Name, msg.FolderID, *msg.IMAPUID, msg.ID)
			if hErr == nil && hydrated != nil {
				msg = hydrated
			}
		}
	}

	s.attachTagInfo(msg)

	isAIReview := msg.IsAIReview || msg.FolderName == constants.FolderAIReviews || msg.FromName == "AI review"
	var reviewedMsg *database.ReviewedMessage

	// Parse diff if message is a patch and not an AI review
	var parsedDiff *patch.ParsedEmailDiff
	if !isAIReview && msg.IsPatch && msg.BodyPlain != "" {
		parsedDiff = patch.ParsePatch(msg.BodyPlain)
	}

	// Fetch inline comments
	var comments []*database.PatchComment
	if !isAIReview && msg.MessageID != "" {
		comments, _ = s.DB.GetMessageComments(msg.MessageID)
	}
	if comments == nil {
		comments = make([]*database.PatchComment, 0)
	}

	// Fetch review commit ID
	revCommit := msg.ReviewCommitID
	if revCommit == "" && msg.MessageID != "" {
		revCommit, _ = s.DB.GetReviewCommit(msg.MessageID)
	}

	// Fetch parent info
	var missingParent bool
	var primaryParentID string
	var parentInfo *database.ParentInfo
	if !isAIReview {
		missingParent, primaryParentID, parentInfo = s.DB.FindParentInfo(msg.InReplyTo, msg.ReferencesHeader, &msg.FolderID, msg.MessageID)
	} else {
		msg.IsAIReview = true
		msg.MessageID = ""
		msg.IsPatch = false
		if rev, err := s.DB.FindReviewedMessage(msg); err == nil && rev != nil {
			reviewedMsg = rev
			msg.ReviewedMessage = rev
		}
	}

	loreURL := ""
	if !isAIReview && msg.MessageID != "" {
		cleaned := parser.CleanMsgID(msg.MessageID)
		if cleaned != "" {
			loreURL = fmt.Sprintf("https://lore.kernel.org/r/%s", cleaned)
		}
	}

	res := map[string]any{
		"message":           msg,
		"patch":             parsedDiff,
		"parsed_diff":       parsedDiff,
		"comments":          comments,
		"review_commit_id":  revCommit,
		"missing_parent":    missingParent,
		"parent_msgid":      primaryParentID,
		"parent_info":       parentInfo,
		"primary_parent_id": primaryParentID,
		"reviewed_message":  reviewedMsg,
		"lore_url":          loreURL,
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(res)
}

func (s *Server) handleGetMessageRawHeader(w http.ResponseWriter, r *http.Request) {
	idParam := chi.URLParam(r, "id")
	msg, err := s.resolveMessage(idParam)
	if err != nil || msg == nil {
		http.Error(w, "Message not found", http.StatusNotFound)
		return
	}

	if msg.FilePath != "" {
		if content, err := os.ReadFile(msg.FilePath); err == nil {
			w.Header().Set("Content-Type", "text/plain; charset=utf-8")
			// Return headers portion (up to first blank line)
			idx := strings.Index(string(content), "\n\n")
			if idx > 0 {
				w.Write(content[:idx])
			} else {
				w.Write(content)
			}
			return
		}
	}

	// Fallback synthesized headers
	headers := fmt.Sprintf("From: %s <%s>\nTo: %s\nSubject: %s\nDate: %s\nMessage-ID: <%s>\n",
		msg.FromName, msg.FromAddr, msg.ToAddr, msg.Subject, msg.Date, msg.MessageID)
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Write([]byte(headers))
}

func (s *Server) handleGetMessageMboxPath(w http.ResponseWriter, r *http.Request) {
	idParam := chi.URLParam(r, "id")
	msg, err := s.resolveMessage(idParam)
	if err != nil || msg == nil {
		http.Error(w, "Message not found", http.StatusNotFound)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"mbox_path": msg.FilePath,
	})
}

func (s *Server) handleDeleteMessage(w http.ResponseWriter, r *http.Request) {
	idParam := chi.URLParam(r, "id")
	msg, err := s.resolveMessage(idParam)
	if err != nil || msg == nil {
		http.Error(w, "Message not found", http.StatusNotFound)
		return
	}

	_ = s.DB.DeleteMessage(msg.ID)
	if msg.FilePath != "" && s.Maildir != nil {
		_ = s.Maildir.DeleteMessage(msg.FilePath)
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"success":    true,
		"status":     "ok",
		"id":         msg.ID,
		"deleted_id": msg.ID,
		"message_id": msg.MessageID,
		"message":    "Message deleted successfully",
	})
}

func (s *Server) handleSetMessageRead(w http.ResponseWriter, r *http.Request) {
	idParam := chi.URLParam(r, "id")
	msg, err := s.resolveMessage(idParam)
	if err != nil || msg == nil {
		http.Error(w, "Message not found", http.StatusNotFound)
		return
	}

	var raw map[string]any
	_ = json.NewDecoder(r.Body).Decode(&raw)

	isRead := true
	if v, ok := raw["is_read"]; ok {
		if b, ok := v.(bool); ok {
			isRead = b
		}
	} else if v, ok := raw["read"]; ok {
		if b, ok := v.(bool); ok {
			isRead = b
		}
	}

	_ = s.DB.SetMessageRead(msg.ID, isRead)
	newPath := msg.FilePath
	if s.Maildir != nil && msg.FilePath != "" {
		var flagErr error
		if isRead {
			newPath, flagErr = s.Maildir.AddFlag(msg.FilePath, constants.FlagSeen)
		} else {
			newPath, flagErr = s.Maildir.RemoveFlag(msg.FilePath, constants.FlagSeen)
		}
		if flagErr == nil && newPath != msg.FilePath {
			_ = s.DB.SetMessageFilePath(msg.ID, newPath)
		}
	}

	if s.IMAP != nil && msg.IMAPUID != nil && *msg.IMAPUID > 0 && msg.FolderName != "" {
		folderName := msg.FolderName
		uid := *msg.IMAPUID
		go func() {
			if err := s.IMAP.MarkIMAPMessageRead(folderName, uid, isRead); err != nil {
				log.Printf("Warning: failed to update IMAP read flag for message %d (UID %d in %s): %v", msg.ID, uid, folderName, err)
			}
		}()
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"success":       true,
		"status":        "ok",
		"message_db_id": msg.ID,
		"is_read":       isRead,
		"file_path":     newPath,
	})
}

func (s *Server) handleSetMessageReplied(w http.ResponseWriter, r *http.Request) {
	idParam := chi.URLParam(r, "id")
	msg, err := s.resolveMessage(idParam)
	if err != nil || msg == nil {
		http.Error(w, "Message not found", http.StatusNotFound)
		return
	}

	var raw map[string]any
	_ = json.NewDecoder(r.Body).Decode(&raw)

	isReplied := true
	if v, ok := raw["is_replied"]; ok {
		if b, ok := v.(bool); ok {
			isReplied = b
		}
	} else if v, ok := raw["replied"]; ok {
		if b, ok := v.(bool); ok {
			isReplied = b
		}
	}

	_ = s.DB.SetMessageReplied(msg.ID, isReplied)
	newPath := msg.FilePath
	if s.Maildir != nil && msg.FilePath != "" {
		var flagErr error
		if isReplied {
			newPath, flagErr = s.Maildir.AddFlag(msg.FilePath, constants.FlagReplied)
		} else {
			newPath, flagErr = s.Maildir.RemoveFlag(msg.FilePath, constants.FlagReplied)
		}
		if flagErr == nil && newPath != msg.FilePath {
			_ = s.DB.SetMessageFilePath(msg.ID, newPath)
		}
	}

	if s.IMAP != nil && msg.IMAPUID != nil && *msg.IMAPUID > 0 && msg.FolderName != "" {
		folderName := msg.FolderName
		uid := *msg.IMAPUID
		go func() {
			if err := s.IMAP.MarkIMAPMessageReplied(folderName, uid, isReplied); err != nil {
				log.Printf("Warning: failed to update IMAP replied flag for message %d (UID %d in %s): %v", msg.ID, uid, folderName, err)
			}
		}()
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"success":       true,
		"status":        "ok",
		"message_db_id": msg.ID,
		"is_replied":    isReplied,
		"file_path":     newPath,
	})
}

func (s *Server) handleSetMessageTag(w http.ResponseWriter, r *http.Request) {
	idParam := chi.URLParam(r, "id")
	msg, err := s.resolveMessage(idParam)
	if err != nil || msg == nil {
		http.Error(w, "Message not found", http.StatusNotFound)
		return
	}

	var body struct {
		Tag    *string `json:"tag"`
		Action string  `json:"action"` // optional legacy fallback
	}
	_ = json.NewDecoder(r.Body).Decode(&body)

	allTags := make(map[string]config.TagDefinition)
	if s.Config != nil {
		allTags = s.Config.Tags
	}
	lowerTagMap := make(map[string]string)
	for k, v := range allTags {
		lowerTagMap[strings.ToLower(k)] = k
		if v.Tag != "" && lowerTagMap[strings.ToLower(v.Tag)] == "" {
			lowerTagMap[strings.ToLower(v.Tag)] = k
		}
	}

	currentTokens := strings.Fields(msg.Flags)
	existingTagKeys := make(map[string]bool)
	for _, t := range currentTokens {
		if _, ok := allTags[t]; ok {
			existingTagKeys[t] = true
		} else if _, ok := lowerTagMap[strings.ToLower(t)]; ok {
			existingTagKeys[t] = true
		}
	}

	var newFlags string
	if body.Action != "" && body.Tag != nil {
		// Legacy action support ("set", "toggle", "clear")
		tagVal := *body.Tag
		currentFlags := msg.Flags
		switch body.Action {
		case "set":
			if !strings.Contains(currentFlags, tagVal) {
				newFlags = strings.TrimSpace(currentFlags + " " + tagVal)
			} else {
				newFlags = currentFlags
			}
		case "toggle":
			if strings.Contains(currentFlags, tagVal) {
				newFlags = strings.TrimSpace(strings.ReplaceAll(currentFlags, tagVal, ""))
			} else {
				newFlags = strings.TrimSpace(currentFlags + " " + tagVal)
			}
		case "clear":
			newFlags = strings.TrimSpace(strings.ReplaceAll(currentFlags, tagVal, ""))
		}
	} else {
		// Standard UndineD set tag logic: replace existing recognized tag with new tag
		var newTagKey string
		if body.Tag != nil && strings.TrimSpace(*body.Tag) != "" {
			reqClean := strings.TrimSpace(*body.Tag)
			if matchedKey, ok := lowerTagMap[strings.ToLower(reqClean)]; ok {
				newTagKey = matchedKey
			} else {
				newTagKey = reqClean
			}
		}

		var remainingTokens []string
		for _, t := range currentTokens {
			if !existingTagKeys[t] {
				remainingTokens = append(remainingTokens, t)
			}
		}
		if newTagKey != "" {
			remainingTokens = append(remainingTokens, newTagKey)
		}
		newFlags = strings.Join(remainingTokens, " ")
	}

	_ = s.DB.SetMessageFlags(msg.ID, newFlags)

	if s.IMAP != nil && msg.IMAPUID != nil && *msg.IMAPUID > 0 && msg.FolderName != "" {
		oldTokens := strings.Fields(msg.Flags)
		newTokens := strings.Fields(newFlags)

		oldMap := make(map[string]bool)
		for _, t := range oldTokens {
			oldMap[t] = true
		}
		newMap := make(map[string]bool)
		for _, t := range newTokens {
			newMap[t] = true
		}

		var addFlags, removeFlags []string
		for _, t := range newTokens {
			if !oldMap[t] {
				addFlags = append(addFlags, t)
			}
		}
		for _, t := range oldTokens {
			if !newMap[t] {
				removeFlags = append(removeFlags, t)
			}
		}

		if len(addFlags) > 0 || len(removeFlags) > 0 {
			folderName := msg.FolderName
			uid := *msg.IMAPUID
			go func() {
				if err := s.IMAP.UpdateIMAPMessageCustomTags(folderName, uid, addFlags, removeFlags); err != nil {
					log.Printf("Warning: failed to update IMAP custom tags for message %d (UID %d in %s): %v", msg.ID, uid, folderName, err)
				}
			}()
		}
	}

	var tagKey, tagName, tagColor any
	if s.Config != nil {
		if tagInfo := s.Config.ResolveTag(newFlags); tagInfo != nil {
			tagKey = tagInfo.Key
			tagName = tagInfo.Tag
			tagColor = tagInfo.Color
		}
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"success":    true,
		"status":     "ok",
		"message_id": msg.ID,
		"flags":      newFlags,
		"tag_key":    tagKey,
		"tag_name":   tagName,
		"tag_color":  tagColor,
	})
}

func (s *Server) handleGetComments(w http.ResponseWriter, r *http.Request) {
	idParam := chi.URLParam(r, "id")
	msg, err := s.resolveMessage(idParam)
	if err != nil || msg == nil {
		http.Error(w, "Message not found", http.StatusNotFound)
		return
	}

	state := r.URL.Query().Get("state")
	comments, err := s.DB.GetMessageComments(msg.MessageID, state)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if comments == nil {
		comments = make([]*database.PatchComment, 0)
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"message_id": msg.MessageID,
		"comments":   comments,
	})
}

func (s *Server) handleSaveComment(w http.ResponseWriter, r *http.Request) {
	idParam := chi.URLParam(r, "id")
	msg, err := s.resolveMessage(idParam)
	if err != nil || msg == nil {
		http.Error(w, "Message not found", http.StatusNotFound)
		return
	}

	var body struct {
		ID        int64  `json:"id"`
		CommentID int64  `json:"comment_id"`
		File      string `json:"file"`
		FilePath  string `json:"file_path"`
		LineNo    int    `json:"line_no"`
		LineNoAlt *int   `json:"lineNo"`
		Side      string `json:"side"`
		Text      string `json:"text"`
		State     string `json:"state"`
		Author    string `json:"author"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "Invalid comment payload", http.StatusBadRequest)
		return
	}

	commentID := body.ID
	if commentID <= 0 && body.CommentID > 0 {
		commentID = body.CommentID
	}

	filePath := body.FilePath
	if filePath == "" && body.File != "" {
		filePath = body.File
	}
	if strings.EqualFold(filePath, "DESCRIPTION") {
		filePath = "DESCRIPTION"
	}

	lineNo := body.LineNo
	if lineNo == 0 && body.LineNoAlt != nil {
		lineNo = *body.LineNoAlt
	}

	req := database.PatchComment{
		ID:        commentID,
		MessageID: msg.MessageID,
		FilePath:  filePath,
		LineNo:    lineNo,
		Side:      body.Side,
		Text:      body.Text,
		State:     body.State,
		Author:    body.Author,
	}

	saved, err := s.DB.SavePatchComment(&req)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"success": true,
		"comment": saved,
	})
}

func (s *Server) handleDiscardComments(w http.ResponseWriter, r *http.Request) {
	idParam := chi.URLParam(r, "id")
	msg, err := s.resolveMessage(idParam)
	if err != nil || msg == nil {
		http.Error(w, "Message not found", http.StatusNotFound)
		return
	}

	discarded, _ := s.DB.DiscardDraftComments(msg.MessageID)
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"success":         true,
		"discarded_count": discarded,
	})
}

func (s *Server) handleDeleteComment(w http.ResponseWriter, r *http.Request) {
	commentIDStr := chi.URLParam(r, "commentID")
	cID, err := strconv.ParseInt(commentIDStr, 10, 64)
	if err != nil {
		http.Error(w, "Invalid comment ID", http.StatusBadRequest)
		return
	}

	_ = s.DB.DeleteComment(cID)
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"success": true})
}

func (s *Server) handleSearch(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query().Get("q")
	limit := 20
	if lStr := r.URL.Query().Get("limit"); lStr != "" {
		if l, err := strconv.Atoi(lStr); err == nil && l > 0 {
			limit = l
		}
	}

	results, err := s.DB.Search(q, limit)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	if results == nil {
		results = make([]*database.SearchResult, 0)
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(results)
}
