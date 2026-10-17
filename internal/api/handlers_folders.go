package api

import (
	"encoding/json"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"
)

func (s *Server) handleGetConfig(w http.ResponseWriter, r *http.Request) {
	editorCmd := s.Config.General.GetExternalEditorCmd()
	var reviewPromptFile any
	if s.Config.Review.PromptFile != "" {
		reviewPromptFile = s.Config.Review.PromptFile
	}

	res := map[string]any{
		"host":                      s.Config.General.Host,
		"port":                      s.Config.General.Port,
		"editor_cmd":                editorCmd,
		"has_external_editor":       editorCmd != "",
		"reply_use_external_editor": s.Config.General.ReplyUseExternalEditor,
		"kernel_git_dir":            s.Config.General.KernelGitDir,
		"from_name":                 s.Config.SMTP.FromName,
		"from_email":                s.Config.SMTP.FromEmail,
		"signature":                 s.Config.SMTP.EffectiveSignature(),
		"patchwork_enabled":         s.Config.Patchwork.Enabled,
		"patchwork_url":             s.Config.Patchwork.URL,
		"patchwork_my_username":     s.Config.Patchwork.GetMyUsername(s.Config.SMTP.FromEmail),
		"patchwork_delegate_users":  s.Config.Patchwork.GetDelegateUsers(),
		"patchwork_can_update":      s.Config.Patchwork.GetToken() != "",
		"review_cmd":                s.Config.Review.Command,
		"review_prompt_file":        reviewPromptFile,
		"tags":                      s.Config.Tags,
		"patchwork": map[string]any{
			"enabled":         s.Config.Patchwork.Enabled,
			"url":             s.Config.Patchwork.URL,
			"default_project": s.Config.Patchwork.DefaultProject,
			"username":        s.Config.Patchwork.Username,
		},
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(res)
}

func (s *Server) handleGetTags(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(s.Config.Tags)
}

func (s *Server) handleListFolders(w http.ResponseWriter, r *http.Request) {
	folders, err := s.DB.ListFolders()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(folders)
}

func (s *Server) handleSyncFullFolder(w http.ResponseWriter, r *http.Request) {
	idStr := chi.URLParam(r, "folderID")
	folderID, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		http.Error(w, "Invalid folder ID", http.StatusBadRequest)
		return
	}

	folder, err := s.DB.GetFolderByID(folderID)
	if err != nil || folder == nil {
		http.Error(w, "Folder not found", http.StatusNotFound)
		return
	}

	if s.IMAP == nil {
		http.Error(w, "IMAP synchronizer not configured", http.StatusInternalServerError)
		return
	}

	if s.IMAP.IsFolderSyncing(folderID) {
		http.Error(w, "Sync already in progress for this folder", http.StatusConflict)
		return
	}

	count, err := s.IMAP.SyncFullFolder(folderID, folder.Name)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	syncStatus := s.IMAP.GetSyncStatus(folderID)
	statusStr := syncStatus.Status
	if statusStr == "" {
		statusStr = "completed"
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"status":        statusStr,
		"folder_id":     folderID,
		"folder_name":   folder.Name,
		"synced_count":  count,
		"removed_count": syncStatus.Removed,
	})
}

func (s *Server) handleGetFolderSyncStatus(w http.ResponseWriter, r *http.Request) {
	idStr := chi.URLParam(r, "folderID")
	folderID, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		http.Error(w, "Invalid folder ID", http.StatusBadRequest)
		return
	}

	folder, err := s.DB.GetFolderByID(folderID)
	if err != nil || folder == nil {
		http.Error(w, "Folder not found", http.StatusNotFound)
		return
	}

	if s.IMAP == nil {
		http.Error(w, "IMAP not configured", http.StatusInternalServerError)
		return
	}

	st := s.IMAP.GetSyncStatus(folderID)
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(st)
}

func (s *Server) handleCancelFolderSync(w http.ResponseWriter, r *http.Request) {
	idStr := chi.URLParam(r, "folderID")
	folderID, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		http.Error(w, "Invalid folder ID", http.StatusBadRequest)
		return
	}

	folder, err := s.DB.GetFolderByID(folderID)
	if err != nil || folder == nil {
		http.Error(w, "Folder not found", http.StatusNotFound)
		return
	}

	if s.IMAP == nil {
		http.Error(w, "IMAP not configured", http.StatusInternalServerError)
		return
	}

	cancelled := s.IMAP.CancelFolderSync(folderID)
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"status":    "ok",
		"cancelled": cancelled,
		"folder_id": folderID,
	})
}
