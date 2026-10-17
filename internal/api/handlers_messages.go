package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"

	"undined-go/internal/constants"
	"undined-go/internal/lkml/patch"
	"undined-go/internal/storage/database"
	"undined-go/internal/storage/parser"
)

func (s *Server) resolveMessage(idOrMsgID string) (*database.Message, error) {
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

func (s *Server) handleListMessages(w http.ResponseWriter, r *http.Request) {
	var folderID *int64
	if fStr := r.URL.Query().Get("folder_id"); fStr != "" {
		if id, err := strconv.ParseInt(fStr, 10, 64); err == nil {
			folderID = &id
		}
	}

	limit := 50
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

	threaded := r.URL.Query().Get("threaded") == "true"
	if threaded && folderID != nil {
		// In threaded mode, list top-level messages or entire thread trees
		msgs, err := s.DB.ListMessages(folderID, limit, offset)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(msgs)
		return
	}

	msgs, err := s.DB.ListMessages(folderID, limit, offset)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
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

	// Parse diff if message is a patch
	var parsedDiff *patch.ParsedEmailDiff
	if msg.IsPatch && msg.BodyPlain != "" {
		parsedDiff = patch.ParsePatch(msg.BodyPlain)
	}

	// Fetch inline comments
	comments, _ := s.DB.GetMessageComments(msg.MessageID)

	// Fetch review commit ID
	revCommit := msg.ReviewCommitID
	if revCommit == "" {
		revCommit, _ = s.DB.GetReviewCommit(msg.MessageID)
	}

	// Fetch parent info
	_, primaryParentID, parentInfo := s.DB.FindParentInfo(msg.InReplyTo, msg.ReferencesHeader, &msg.FolderID, msg.MessageID)

	res := map[string]any{
		"message":           msg,
		"patch":             parsedDiff,
		"comments":          comments,
		"review_commit_id":  revCommit,
		"parent_info":       parentInfo,
		"primary_parent_id": primaryParentID,
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
	_ = json.NewEncoder(w).Encode(map[string]any{"success": true})
}

func (s *Server) handleSetMessageRead(w http.ResponseWriter, r *http.Request) {
	idParam := chi.URLParam(r, "id")
	msg, err := s.resolveMessage(idParam)
	if err != nil || msg == nil {
		http.Error(w, "Message not found", http.StatusNotFound)
		return
	}

	var body struct {
		Read bool `json:"read"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)

	_ = s.DB.SetMessageRead(msg.ID, body.Read)
	if s.Maildir != nil && msg.FilePath != "" {
		var newPath string
		var flagErr error
		if body.Read {
			newPath, flagErr = s.Maildir.AddFlag(msg.FilePath, constants.FlagSeen)
		} else {
			newPath, flagErr = s.Maildir.RemoveFlag(msg.FilePath, constants.FlagSeen)
		}
		if flagErr == nil && newPath != msg.FilePath {
			_ = s.DB.SetMessageFilePath(msg.ID, newPath)
		}
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"success": true, "is_read": body.Read})
}

func (s *Server) handleSetMessageReplied(w http.ResponseWriter, r *http.Request) {
	idParam := chi.URLParam(r, "id")
	msg, err := s.resolveMessage(idParam)
	if err != nil || msg == nil {
		http.Error(w, "Message not found", http.StatusNotFound)
		return
	}

	var body struct {
		Replied bool `json:"replied"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)

	_ = s.DB.SetMessageReplied(msg.ID, body.Replied)
	if s.Maildir != nil && msg.FilePath != "" {
		var newPath string
		var flagErr error
		if body.Replied {
			newPath, flagErr = s.Maildir.AddFlag(msg.FilePath, constants.FlagReplied)
		} else {
			newPath, flagErr = s.Maildir.RemoveFlag(msg.FilePath, constants.FlagReplied)
		}
		if flagErr == nil && newPath != msg.FilePath {
			_ = s.DB.SetMessageFilePath(msg.ID, newPath)
		}
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"success": true, "is_replied": body.Replied})
}

func (s *Server) handleSetMessageTag(w http.ResponseWriter, r *http.Request) {
	idParam := chi.URLParam(r, "id")
	msg, err := s.resolveMessage(idParam)
	if err != nil || msg == nil {
		http.Error(w, "Message not found", http.StatusNotFound)
		return
	}

	var body struct {
		Tag    string `json:"tag"`
		Action string `json:"action"` // "set", "toggle", "clear"
	}
	_ = json.NewDecoder(r.Body).Decode(&body)

	currentFlags := msg.Flags
	newFlags := currentFlags

	switch body.Action {
	case "set":
		if !strings.Contains(currentFlags, body.Tag) {
			newFlags = strings.TrimSpace(currentFlags + " " + body.Tag)
		}
	case "toggle":
		if strings.Contains(currentFlags, body.Tag) {
			newFlags = strings.TrimSpace(strings.ReplaceAll(currentFlags, body.Tag, ""))
		} else {
			newFlags = strings.TrimSpace(currentFlags + " " + body.Tag)
		}
	case "clear":
		newFlags = strings.TrimSpace(strings.ReplaceAll(currentFlags, body.Tag, ""))
	}

	_ = s.DB.SetMessageFlags(msg.ID, newFlags)

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"success": true, "flags": newFlags})
}

func (s *Server) handleGetComments(w http.ResponseWriter, r *http.Request) {
	idParam := chi.URLParam(r, "id")
	msg, err := s.resolveMessage(idParam)
	if err != nil || msg == nil {
		http.Error(w, "Message not found", http.StatusNotFound)
		return
	}

	comments, err := s.DB.GetMessageComments(msg.MessageID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(comments)
}

func (s *Server) handleSaveComment(w http.ResponseWriter, r *http.Request) {
	idParam := chi.URLParam(r, "id")
	msg, err := s.resolveMessage(idParam)
	if err != nil || msg == nil {
		http.Error(w, "Message not found", http.StatusNotFound)
		return
	}

	var req database.PatchComment
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Invalid comment payload", http.StatusBadRequest)
		return
	}

	req.MessageID = msg.MessageID
	saved, err := s.DB.SavePatchComment(&req)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(saved)
}

func (s *Server) handleDiscardComments(w http.ResponseWriter, r *http.Request) {
	idParam := chi.URLParam(r, "id")
	msg, err := s.resolveMessage(idParam)
	if err != nil || msg == nil {
		http.Error(w, "Message not found", http.StatusNotFound)
		return
	}

	_ = s.DB.DiscardDraftComments(msg.MessageID)
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"success": true})
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

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(results)
}
