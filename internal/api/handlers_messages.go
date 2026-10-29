package api

import (
	"bytes"
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

func (s *Server) ensureMessageHydrated(msg *database.Message) *database.Message {
	if msg == nil {
		return nil
	}
	if msg.FilePath == "" && msg.IMAPUID != nil && s.IMAP != nil {
		folder, fErr := s.DB.GetFolderByID(msg.FolderID)
		if fErr == nil && folder != nil {
			hydrated, hErr := s.IMAP.FetchAndHydrateMessage(folder.Name, msg.FolderID, *msg.IMAPUID, msg.ID)
			if hErr == nil && hydrated != nil {
				return hydrated
			}
		}
	}
	return msg
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
	msg = s.ensureMessageHydrated(msg)

	s.attachTagInfo(msg)

	isAIReview := msg.IsAIReview || msg.FolderName == constants.FolderAIReviews || msg.FromName == "AI review"
	var reviewedMsg *database.ReviewedMessage

	// Parse diff if message is a patch or contains inline diff and not an AI review
	var parsedDiff *patch.ParsedEmailDiff
	if !isAIReview && msg.BodyPlain != "" && (msg.IsPatch || strings.Contains(msg.BodyPlain, "diff --git") || strings.Contains(msg.BodyPlain, "--- a/")) {
		parsedDiff = patch.ParsePatch(msg.BodyPlain)
		if parsedDiff != nil && !parsedDiff.IsPatch && msg.IsPatch {
			msg.IsPatch = false
			_ = s.DB.SetMessageIsPatch(msg.ID, false)
		}
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

	// Extract attachments if message has a backing file
	attachments := []*parser.Attachment{}
	if msg.FilePath != "" {
		if rawBytes, err := os.ReadFile(msg.FilePath); err == nil {
			if atts, err := parser.ExtractAttachments(rawBytes); err == nil && len(atts) > 0 {
				attachments = atts
			}
		}
	}
	msg.Attachments = attachments

	res := map[string]any{
		"message":           msg,
		"attachments":       attachments,
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

func (s *Server) handleGetAttachment(w http.ResponseWriter, r *http.Request) {
	idParam := chi.URLParam(r, "id")
	indexParam := chi.URLParam(r, "index")

	index, err := strconv.Atoi(indexParam)
	if err != nil || index < 0 {
		http.Error(w, "Invalid attachment index", http.StatusBadRequest)
		return
	}

	msg, err := s.resolveMessage(idParam)
	if err != nil || msg == nil {
		http.Error(w, "Message not found", http.StatusNotFound)
		return
	}

	// Auto-hydrate if file is missing but IMAP UID is known
	msg = s.ensureMessageHydrated(msg)

	if msg.FilePath == "" {
		http.Error(w, "Message file not available on disk", http.StatusNotFound)
		return
	}

	data, err := os.ReadFile(msg.FilePath)
	if err != nil {
		http.Error(w, "Failed to read message file from disk", http.StatusInternalServerError)
		return
	}

	attachments, err := parser.ExtractAttachments(data)
	if err != nil {
		http.Error(w, "Failed to parse message MIME structure", http.StatusInternalServerError)
		return
	}

	if index >= len(attachments) {
		http.Error(w, "Attachment index out of bounds", http.StatusNotFound)
		return
	}

	att := attachments[index]

	// Format safe Content-Disposition with RFC 5987 / RFC 6266 filename*
	filename := parser.SanitizeAttachmentFileName(att.FileName, att.Index, att.ContentType)
	encodedFilename := url.PathEscape(filename)
	disposition := fmt.Sprintf(`attachment; filename="%s"; filename*=UTF-8''%s`, filename, encodedFilename)

	contentType := att.ContentType
	if contentType == "" {
		contentType = "application/octet-stream"
	}

	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Content-Disposition", disposition)
	w.Header().Set("Content-Length", strconv.Itoa(len(att.Payload)))
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Security-Policy", "default-src 'none'")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(att.Payload)
}

func (s *Server) handleGetMessageRawHeader(w http.ResponseWriter, r *http.Request) {
	idParam := chi.URLParam(r, "id")
	msg, err := s.resolveMessage(idParam)
	if err != nil || msg == nil {
		http.Error(w, "Message not found", http.StatusNotFound)
		return
	}

	var rawHeaders string
	if msg.FilePath != "" {
		if content, err := os.ReadFile(msg.FilePath); err == nil {
			if idx := bytes.Index(content, []byte("\r\n\r\n")); idx != -1 {
				rawHeaders = string(content[:idx])
			} else if idx := bytes.Index(content, []byte("\n\n")); idx != -1 {
				rawHeaders = string(content[:idx])
			} else {
				rawHeaders = string(content)
			}
		}
	}

	if rawHeaders == "" {
		// Fallback synthesized headers
		msgID := msg.MessageID
		if msgID != "" && !strings.HasPrefix(msgID, "<") && !strings.HasSuffix(msgID, ">") {
			msgID = fmt.Sprintf("<%s>", msgID)
		}
		rawHeaders = fmt.Sprintf("From: %s <%s>\nTo: %s\nSubject: %s\nDate: %s\nMessage-ID: %s\n",
			msg.FromName, msg.FromAddr, msg.ToAddr, msg.Subject, msg.Date, msgID)
	}

	// Client explicitly requested plain text
	if r.Header.Get("Accept") == "text/plain" {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.Write([]byte(rawHeaders))
		return
	}

	msgID := msg.MessageID
	if msgID != "" && !strings.HasPrefix(msgID, "<") && !strings.HasSuffix(msgID, ">") {
		msgID = fmt.Sprintf("<%s>", msgID)
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"message_id":  msgID,
		"raw_headers": rawHeaders,
	})
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
		"file_path": msg.FilePath,
	})
}

func (s *Server) handleDeleteMessage(w http.ResponseWriter, r *http.Request) {
	idParam := chi.URLParam(r, "id")
	msg, err := s.resolveMessage(idParam)
	if err != nil || msg == nil {
		http.Error(w, "Message not found", http.StatusNotFound)
		return
	}

	if err := s.DB.DeleteMessage(msg.ID); err != nil {
		http.Error(w, fmt.Sprintf("Failed to delete message: %v", err), http.StatusInternalServerError)
		return
	}
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

	if s.Events != nil {
		s.Events.Publish("message.updated", map[string]any{
			"message_id": msg.MessageID,
			"folder":     msg.FolderName,
			"is_read":    isRead,
		})
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

	if s.Events != nil {
		s.Events.Publish("message.updated", map[string]any{
			"message_id": msg.MessageID,
			"folder":     msg.FolderName,
			"is_replied": isReplied,
		})
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
		// Legacy action support ("set", "toggle", "clear") using exact token matching
		tagVal := strings.TrimSpace(*body.Tag)
		hasToken := false
		for _, tok := range currentTokens {
			if tok == tagVal {
				hasToken = true
				break
			}
		}
		switch body.Action {
		case "set":
			if !hasToken && tagVal != "" {
				newFlags = strings.Join(append(currentTokens, tagVal), " ")
			} else {
				newFlags = strings.Join(currentTokens, " ")
			}
		case "toggle":
			if hasToken {
				var filtered []string
				for _, tok := range currentTokens {
					if tok != tagVal {
						filtered = append(filtered, tok)
					}
				}
				newFlags = strings.Join(filtered, " ")
			} else if tagVal != "" {
				newFlags = strings.Join(append(currentTokens, tagVal), " ")
			} else {
				newFlags = strings.Join(currentTokens, " ")
			}
		case "clear":
			var filtered []string
			for _, tok := range currentTokens {
				if tok != tagVal {
					filtered = append(filtered, tok)
				}
			}
			newFlags = strings.Join(filtered, " ")
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

	if s.Events != nil {
		s.Events.Publish("message.updated", map[string]any{
			"message_id": msg.MessageID,
			"folder":     msg.FolderName,
			"tag":        tagKey,
		})
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

	if s.Events != nil && saved != nil {
		s.Events.Publish("review.comment_added", map[string]any{
			"message_id": msg.MessageID,
			"comment_id": saved.ID,
			"file":       saved.FilePath,
			"line_no":    saved.LineNo,
			"author":     saved.Author,
		})
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

	if s.Events != nil {
		s.Events.Publish("review.comment_added", map[string]any{
			"message_id":      msg.MessageID,
			"action":          "discarded",
			"discarded_count": discarded,
		})
	}

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

	if err := s.DB.DeleteComment(cID); err != nil {
		http.Error(w, fmt.Sprintf("Failed to delete comment: %v", err), http.StatusInternalServerError)
		return
	}

	if s.Events != nil {
		s.Events.Publish("review.comment_added", map[string]any{
			"comment_id": cID,
			"action":     "deleted",
		})
	}

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
