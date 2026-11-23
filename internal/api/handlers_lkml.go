package api

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	"undined-go/internal/bridge"
	"undined-go/internal/constants"
	"undined-go/internal/lkml/patch"
	"undined-go/internal/lkml/quote"
	"undined-go/internal/smtp"
	"undined-go/internal/storage/parser"
)

func (s *Server) handleB4Import(w http.ResponseWriter, r *http.Request) {
	var body struct {
		MessageID string `json:"message_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.MessageID == "" {
		http.Error(w, "message_id is required", http.StatusBadRequest)
		return
	}

	if s.B4 == nil {
		http.Error(w, "b4 runner not configured", http.StatusInternalServerError)
		return
	}

	res, err := s.B4.ImportThread(body.MessageID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	if s.Events != nil && len(res) > 0 {
		s.Events.Publish("thread.hydrated", map[string]any{
			"root_message_id": body.MessageID,
			"imported_count":  len(res),
			"timestamp":       time.Now().Unix(),
		})
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"success":        true,
		"message_id":     body.MessageID,
		"imported_count": len(res),
		"indexed_ids":    res,
	})
}

func (s *Server) handleB4Apply(w http.ResponseWriter, r *http.Request) {
	var body struct {
		MessageID     string `json:"message_id"`
		TargetGitRepo string `json:"target_git_repo"`
		BranchName    string `json:"branch_name"`
		BaseCommit    string `json:"base_commit"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.MessageID == "" {
		http.Error(w, "message_id is required", http.StatusBadRequest)
		return
	}

	if s.B4 == nil {
		http.Error(w, "b4 runner not configured", http.StatusInternalServerError)
		return
	}

	res, err := s.B4.ApplyPatch(body.MessageID, body.TargetGitRepo, body.BranchName, body.BaseCommit)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	if s.Events != nil && res != nil {
		s.Events.Publish("patch.applied", map[string]any{
			"message_id":  body.MessageID,
			"target_repo": body.TargetGitRepo,
			"branch":      body.BranchName,
			"success":     res.Success,
			"error":       res.Stderr,
		})
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(res)
}

func (s *Server) handleB4Abort(w http.ResponseWriter, r *http.Request) {
	if s.B4 == nil {
		http.Error(w, "b4 runner not configured", http.StatusInternalServerError)
		return
	}

	var body struct {
		TargetGitRepo  string `json:"target_git_repo"`
		BranchName     string `json:"branch_name"`
		BaseCommit     string `json:"base_commit"`
		OriginalBranch string `json:"original_branch"`
		HeadBefore     string `json:"head_before"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)

	res, err := s.B4.AbortApply(body.TargetGitRepo, body.BranchName, body.BaseCommit, body.OriginalBranch, body.HeadBefore)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	if s.Events != nil && res != nil {
		s.Events.Publish("patch.applied", map[string]any{
			"message_id":  "",
			"target_repo": body.TargetGitRepo,
			"branch":      body.BranchName,
			"success":     false,
			"action":      "aborted",
			"error":       res.Error,
		})
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(res)
}

func (s *Server) handleReviewTrigger(w http.ResponseWriter, r *http.Request) {
	var body struct {
		MessageID string `json:"message_id"`
		CommitID  string `json:"commit_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.MessageID == "" {
		http.Error(w, "message_id is required", http.StatusBadRequest)
		return
	}

	if s.Review == nil {
		http.Error(w, "review runner not configured", http.StatusInternalServerError)
		return
	}

	res, err := s.Review.TriggerReview(body.MessageID, body.CommitID)
	if err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusConflict)
		_ = json.NewEncoder(w).Encode(map[string]any{"detail": err.Error()})
		return
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(res)
}

func (s *Server) handleReviewStatus(w http.ResponseWriter, r *http.Request) {
	if s.Review == nil {
		http.Error(w, "review runner not configured", http.StatusInternalServerError)
		return
	}

	st := s.Review.GetStatus()
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(st)
}

func (s *Server) handleReviewCancel(w http.ResponseWriter, r *http.Request) {
	var body struct {
		MessageID string `json:"message_id"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)

	targetID := body.MessageID
	if targetID == "" {
		targetID = r.URL.Query().Get("message_id")
	}

	if s.Review == nil {
		http.Error(w, "review runner not configured", http.StatusInternalServerError)
		return
	}

	cancelled := s.Review.CancelReview(targetID)
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"success":    true,
		"status":     "ok",
		"cancelled":  cancelled,
		"message_id": targetID,
	})
}

func (s *Server) handlePreviewQuote(w http.ResponseWriter, r *http.Request) {
	var body struct {
		MessageID      string             `json:"message_id"`
		GeneralComment string             `json:"general_comment"`
		Notes          []quote.InlineNote `json:"notes"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "Invalid request", http.StatusBadRequest)
		return
	}

	msg, err := s.resolveMessage(body.MessageID)
	if err != nil || msg == nil {
		http.Error(w, "Message not found", http.StatusNotFound)
		return
	}
	msg = s.ensureMessageHydrated(msg)

	notes := body.Notes
	if len(notes) == 0 && s.DB != nil {
		dbComments, _ := s.DB.GetMessageComments(msg.MessageID)
		for _, c := range dbComments {
			notes = append(notes, quote.InlineNote{
				File:   c.FilePath,
				LineNo: c.LineNo,
				Side:   c.Side,
				Text:   c.Text,
			})
		}
	}

	parsed := patch.Parse(msg.BodyPlain)
	sig := s.Config.SMTP.EffectiveSignature()
	quoteText := quote.MergePatchReviewQuote(
		msg.Date, msg.FromName, msg.FromAddr, msg.BodyPlain,
		parsed, notes, body.GeneralComment, sig,
	)

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"quote_text": quoteText,
		"body":       quoteText,
		"quote":      quoteText,
	})
}

func (s *Server) handleOpenEditor(w http.ResponseWriter, r *http.Request) {
	var req bridge.OpenEditorRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Invalid request payload", http.StatusBadRequest)
		return
	}

	if s.Launcher == nil {
		http.Error(w, "Editor launcher not configured", http.StatusInternalServerError)
		return
	}

	res, err := s.Launcher.Launch(req)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(res)
}

type composerAttachmentInput struct {
	FileName              string `json:"filename"`
	ContentType           string `json:"content_type"`
	ContentBase64         string `json:"content_base64"`
	SourceMessageID       int64  `json:"source_message_id,omitempty"`
	SourceAttachmentIndex *int   `json:"source_attachment_index,omitempty"`
}

func (s *Server) resolveComposerAttachments(inputs []composerAttachmentInput) ([]smtp.Attachment, error) {
	if len(inputs) == 0 {
		return nil, nil
	}

	resolved := make([]smtp.Attachment, 0, len(inputs))
	cachedSourceAtts := make(map[int64][]*parser.Attachment)
	var totalBytes int64

	for idx, item := range inputs {
		var data []byte
		fileName := strings.TrimSpace(item.FileName)
		contentType := strings.TrimSpace(item.ContentType)

		if item.ContentBase64 != "" {
			b64 := strings.TrimSpace(item.ContentBase64)
			// Strip optional data URI prefix (e.g. data:application/octet-stream;base64,...)
			if strings.HasPrefix(b64, "data:") {
				if comma := strings.Index(b64, ","); comma != -1 {
					b64 = b64[comma+1:]
				}
			}
			decoded, err := base64.StdEncoding.DecodeString(b64)
			if err != nil {
				return nil, fmt.Errorf("invalid base64 data for attachment %d (%s): %w", idx+1, fileName, err)
			}
			data = decoded
		} else if item.SourceMessageID > 0 && item.SourceAttachmentIndex != nil && *item.SourceAttachmentIndex >= 0 {
			atts, ok := cachedSourceAtts[item.SourceMessageID]
			if !ok {
				if s.DB == nil {
					return nil, fmt.Errorf("database not configured for source attachment %d", idx+1)
				}
				srcMsg, err := s.DB.GetMessageByID(item.SourceMessageID)
				if err != nil || srcMsg == nil || srcMsg.FilePath == "" {
					return nil, fmt.Errorf("source message %d not found for attachment %d", item.SourceMessageID, idx+1)
				}
				rawBytes, err := os.ReadFile(srcMsg.FilePath)
				if err != nil {
					return nil, fmt.Errorf("failed to read source message %d: %w", item.SourceMessageID, err)
				}
				extracted, err := parser.ExtractAttachments(rawBytes)
				if err != nil {
					return nil, fmt.Errorf("failed to extract attachments from message %d: %w", item.SourceMessageID, err)
				}
				atts = extracted
				cachedSourceAtts[item.SourceMessageID] = atts
			}
			attIdx := *item.SourceAttachmentIndex
			if attIdx >= len(atts) {
				return nil, fmt.Errorf("source attachment index %d out of range for message %d", attIdx, item.SourceMessageID)
			}
			srcAtt := atts[attIdx]
			data = srcAtt.Payload
			if fileName == "" {
				fileName = srcAtt.FileName
			}
			if contentType == "" {
				contentType = srcAtt.ContentType
			}
		} else {
			return nil, fmt.Errorf("attachment %d (%s) is missing content_base64 or source reference", idx+1, fileName)
		}

		totalBytes += int64(len(data))
		if totalBytes > smtp.MaxAttachmentTotalBytes {
			return nil, fmt.Errorf("total attachment size exceeds maximum limit of %d bytes", smtp.MaxAttachmentTotalBytes)
		}

		if contentType == "" {
			contentType = smtp.DetectAttachmentContentType(fileName, data)
		}
		fileName = parser.SanitizeAttachmentFileName(fileName, idx, contentType)

		resolved = append(resolved, smtp.Attachment{
			FileName:    fileName,
			ContentType: contentType,
			Data:        data,
		})
	}

	return resolved, nil
}

// maxComposerRequestBodyBytes caps JSON composer payloads (~25 MiB attachments with base64 overhead + body).
const maxComposerRequestBodyBytes = 40 << 20

func (s *Server) handleSaveDraft(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, maxComposerRequestBodyBytes)
	var body struct {
		ToAddr      string                    `json:"to_addr"`
		To          string                    `json:"to"`
		Subject     string                    `json:"subject"`
		BodyText    string                    `json:"body_text"`
		Body        string                    `json:"body"`
		CcAddr      string                    `json:"cc_addr"`
		Cc          string                    `json:"cc"`
		InReplyTo   string                    `json:"in_reply_to"`
		References  string                    `json:"references"`
		CustomFrom  string                    `json:"custom_from"`
		DraftID     int64                     `json:"draft_id"`
		Attachments []composerAttachmentInput `json:"attachments"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "Invalid draft payload", http.StatusBadRequest)
		return
	}

	toAddr := body.ToAddr
	if toAddr == "" {
		toAddr = body.To
	}
	bodyText := body.BodyText
	if bodyText == "" {
		bodyText = body.Body
	}
	ccAddr := body.CcAddr
	if ccAddr == "" {
		ccAddr = body.Cc
	}

	if s.SMTP == nil || s.Maildir == nil || s.DB == nil {
		http.Error(w, "Storage not configured", http.StatusInternalServerError)
		return
	}

	attachments, err := s.resolveComposerAttachments(body.Attachments)
	if err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(map[string]any{"success": false, "detail": err.Error()})
		return
	}

	raw := s.SMTP.BuildMessageWithAttachments(toAddr, body.Subject, bodyText, ccAddr, body.InReplyTo, body.References, body.CustomFrom, attachments)

	_ = s.Maildir.EnsureFolder(constants.FolderDrafts)
	draftFolder, err := s.DB.GetOrCreateFolder(constants.FolderDrafts, false)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	path, err := s.Maildir.AddMessage(constants.FolderDrafts, []byte(raw), constants.FlagDraft)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	dbID, err := s.DB.IndexEmailFile(draftFolder.ID, path)
	if err != nil {
		if s.Maildir != nil {
			_ = s.Maildir.DeleteMessage(path)
		}
		http.Error(w, fmt.Sprintf("Failed to index draft message: %v", err), http.StatusInternalServerError)
		return
	}

	// If overwriting a previous draft from the same editor session, delete the old draft
	if body.DraftID > 0 && body.DraftID != dbID {
		if prevDraft, _ := s.DB.GetMessageByID(body.DraftID); prevDraft != nil && prevDraft.FolderID == draftFolder.ID {
			_ = s.DB.DeleteMessage(prevDraft.ID)
			if s.Maildir != nil && prevDraft.FilePath != "" {
				_ = s.Maildir.DeleteMessage(prevDraft.FilePath)
			}
		}
	}

	msgID := ""
	if savedMsg, _ := s.DB.GetMessageByID(dbID); savedMsg != nil {
		msgID = savedMsg.MessageID
	}

	savedAttachments := make([]*parser.Attachment, 0, len(attachments))
	for i, att := range attachments {
		savedAttachments = append(savedAttachments, &parser.Attachment{
			Index:       i,
			FileName:    att.FileName,
			ContentType: att.ContentType,
			Size:        int64(len(att.Data)),
		})
	}

	if s.Events != nil {
		eventType := "draft.created"
		if body.DraftID > 0 {
			eventType = "draft.updated"
		}
		s.Events.Publish(eventType, map[string]any{
			"draft_id":  dbID,
			"subject":   body.Subject,
			"recipient": toAddr,
		})
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"success":     true,
		"status":      "ok",
		"message_id":  msgID,
		"db_id":       dbID,
		"file_path":   path,
		"attachments": savedAttachments,
	})
}

func (s *Server) handleSMTPSend(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, maxComposerRequestBodyBytes)
	var body struct {
		ToAddr      string                    `json:"to_addr"`
		To          string                    `json:"to"`
		CcAddr      string                    `json:"cc_addr"`
		Cc          string                    `json:"cc"`
		Subject     string                    `json:"subject"`
		BodyText    string                    `json:"body_text"`
		Body        string                    `json:"body"`
		InReplyTo   string                    `json:"in_reply_to"`
		References  string                    `json:"references"`
		OrigMsgID   any                       `json:"orig_msg_id"`
		DraftID     int64                     `json:"draft_id"`
		Attachments []composerAttachmentInput `json:"attachments"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "Invalid send payload", http.StatusBadRequest)
		return
	}

	toAddr := body.ToAddr
	if toAddr == "" {
		toAddr = body.To
	}
	bodyText := body.BodyText
	if bodyText == "" {
		bodyText = body.Body
	}
	ccAddr := body.CcAddr
	if ccAddr == "" {
		ccAddr = body.Cc
	}

	if s.SMTP == nil {
		http.Error(w, "SMTP client not configured", http.StatusInternalServerError)
		return
	}

	attachments, err := s.resolveComposerAttachments(body.Attachments)
	if err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(map[string]any{"success": false, "detail": err.Error()})
		return
	}

	ok, msg, err := s.SMTP.SendMessageWithAttachments(toAddr, body.Subject, bodyText, ccAddr, body.InReplyTo, body.References, attachments)
	if err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		_ = json.NewEncoder(w).Encode(map[string]any{"success": false, "detail": err.Error()})
		return
	}

	if s.DB != nil && (toAddr != "" || ccAddr != "") {
		_ = s.DB.RecordKnownAddressesFromHeaders(toAddr, ccAddr)
	}

	// If this message was replying to an existing email, mark all folder copies of the original email as replied
	var origRefStr string
	switch v := body.OrigMsgID.(type) {
	case string:
		origRefStr = strings.TrimSpace(v)
	case float64:
		if v > 0 {
			origRefStr = fmt.Sprintf("%.0f", v)
		}
	}
	if s.DB != nil {
		seenMsgIDs := make(map[int64]bool)
		for _, ref := range []string{origRefStr, body.InReplyTo} {
			if strings.TrimSpace(ref) == "" {
				continue
			}
			if orig, _ := s.resolveMessage(ref); orig != nil {
				if (body.DraftID > 0 && orig.ID == body.DraftID) || orig.FolderName == constants.FolderDrafts || orig.FolderName == constants.FolderSent {
					continue
				}
				if seenMsgIDs[orig.ID] {
					continue
				}
				seenMsgIDs[orig.ID] = true
				_ = s.markMessageAndCopiesReplied(orig, true)
				_, _ = s.DB.DiscardDraftComments(orig.MessageID)
			}
		}
	}

	// If this sent message originated from a draft, clean up the draft from Drafts folder
	if body.DraftID > 0 && s.DB != nil {
		if draftFolder, err := s.DB.GetFolderByName(constants.FolderDrafts); err == nil && draftFolder != nil {
			if draftMsg, err := s.DB.GetMessageByID(body.DraftID); err == nil && draftMsg != nil && draftMsg.FolderID == draftFolder.ID {
				_ = s.DB.DeleteMessage(draftMsg.ID)
				if s.Maildir != nil && draftMsg.FilePath != "" {
					_ = s.Maildir.DeleteMessage(draftMsg.FilePath)
				}
			}
		}
	}

	if s.Events != nil {
		s.Events.Publish("mail.sync_completed", map[string]any{
			"folder":    constants.FolderSent,
			"new_count": 1,
			"timestamp": time.Now().Unix(),
		})
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"success": ok,
		"message": msg,
	})
}
