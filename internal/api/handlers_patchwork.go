package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"
)

func (s *Server) handlePatchworkInfo(w http.ResponseWriter, r *http.Request) {
	msgID := r.URL.Query().Get("msgid")
	if msgID == "" {
		http.Error(w, "msgid is required", http.StatusBadRequest)
		return
	}
	project := r.URL.Query().Get("project")
	refresh := r.URL.Query().Get("refresh") == "true"

	if s.Patchwork == nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"enabled": false,
			"found":   false,
			"error":   "Patchwork client not configured",
			"msgid":   msgID,
		})
		return
	}

	if refresh {
		s.Patchwork.ClearCache(msgID)
	}

	info, err := s.Patchwork.GetPatchInfo(msgID, project)
	if err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"enabled": true,
			"found":   false,
			"error":   err.Error(),
			"msgid":   msgID,
		})
		return
	}

	w.Header().Set("Content-Type", "application/json")
	if info == nil {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"enabled": true,
			"found":   false,
			"msgid":   msgID,
		})
		return
	}

	// In case info was retrieved from raw cache without enrichment
	info["enabled"] = true
	info["found"] = true
	info["can_update"] = s.Config.Patchwork.GetToken() != ""
	if info["type"] == nil {
		info["type"] = "patch"
	}
	info["is_delegated_to_me"] = s.Config.Patchwork.IsMe(info["delegate"], s.Config.SMTP.FromEmail)
	info["delegate_users"] = s.Config.Patchwork.GetDelegateUsers()

	_ = json.NewEncoder(w).Encode(info)
}

func (s *Server) handlePatchworkMyPatches(w http.ResponseWriter, r *http.Request) {
	stateStr := r.URL.Query().Get("state")
	var states []string
	if stateStr != "" {
		for _, st := range strings.Split(stateStr, ",") {
			if clean := strings.TrimSpace(st); clean != "" {
				states = append(states, clean)
			}
		}
	}
	project := r.URL.Query().Get("project")
	limit := 50
	if lStr := r.URL.Query().Get("limit"); lStr != "" {
		if l, err := strconv.Atoi(lStr); err == nil && l >= 0 {
			limit = l
		}
	}

	if s.Patchwork == nil {
		http.Error(w, "Patchwork client not configured", http.StatusInternalServerError)
		return
	}

	patches, err := s.Patchwork.ListMyPatches(states, project, limit, s.Config.SMTP.FromEmail)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	for _, p := range patches {
		if msgID, ok := p["msgid"].(string); ok && msgID != "" {
			if localMsg, err := s.DB.GetMessageByMsgID(msgID); err == nil && localMsg != nil {
				p["local_message_id"] = localMsg.ID
			}
		}
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(patches)
}

func (s *Server) handlePatchworkSyncTodo(w http.ResponseWriter, r *http.Request) {
	var states []string
	if stateStr := r.URL.Query().Get("states"); stateStr != "" {
		for _, st := range strings.Split(stateStr, ",") {
			if clean := strings.TrimSpace(st); clean != "" {
				states = append(states, clean)
			}
		}
	}
	if len(states) == 0 && r.Body != nil {
		var reqBody struct {
			States any `json:"states"`
		}
		// Attempt to parse JSON request body if present; ignore empty or non-JSON payloads
		// to allow fallback to configured patchwork.todo_states.
		if err := json.NewDecoder(r.Body).Decode(&reqBody); err == nil && reqBody.States != nil {
			switch v := reqBody.States.(type) {
			case string:
				for _, st := range strings.Split(v, ",") {
					if clean := strings.TrimSpace(st); clean != "" {
						states = append(states, clean)
					}
				}
			case []any:
				for _, item := range v {
					if str, ok := item.(string); ok {
						if clean := strings.TrimSpace(str); clean != "" {
							states = append(states, clean)
						}
					}
				}
			case []string:
				for _, st := range v {
					if clean := strings.TrimSpace(st); clean != "" {
						states = append(states, clean)
					}
				}
			}
		}
	}

	if s.Patchwork == nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"detail": "Patchwork client not configured",
			"error":  "Patchwork client not configured",
		})
		return
	}

	res, err := s.Patchwork.SyncTodoFromPatchwork(states)
	if err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"detail": err.Error(),
			"error":  err.Error(),
		})
		return
	}

	if s.Events != nil && (res.SyncedCount > 0 || res.RemovedCount > 0) {
		s.Events.Publish("mail.sync_completed", map[string]any{
			"folder":        "TODO",
			"new_count":     res.SyncedCount,
			"removed_count": res.RemovedCount,
		})
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(res)
}

func (s *Server) handlePatchworkGetPatch(w http.ResponseWriter, r *http.Request) {
	idStr := chi.URLParam(r, "patchID")
	pID, err := strconv.Atoi(idStr)
	if err != nil {
		http.Error(w, "Invalid patch ID", http.StatusBadRequest)
		return
	}

	if s.Patchwork == nil {
		http.Error(w, "Patchwork client not configured", http.StatusInternalServerError)
		return
	}

	patch, err := s.Patchwork.GetPatch(pID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(patch)
}

func (s *Server) handlePatchworkGetChecks(w http.ResponseWriter, r *http.Request) {
	idStr := chi.URLParam(r, "patchID")
	pID, err := strconv.Atoi(idStr)
	if err != nil {
		http.Error(w, "Invalid patch ID", http.StatusBadRequest)
		return
	}

	if s.Patchwork == nil {
		http.Error(w, "Patchwork client not configured", http.StatusInternalServerError)
		return
	}

	checks, err := s.Patchwork.GetChecks(pID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(checks)
}

func (s *Server) handlePatchworkGetSeries(w http.ResponseWriter, r *http.Request) {
	idStr := chi.URLParam(r, "seriesID")
	sID, err := strconv.Atoi(idStr)
	if err != nil {
		http.Error(w, "Invalid series ID", http.StatusBadRequest)
		return
	}

	if s.Patchwork == nil {
		http.Error(w, "Patchwork client not configured", http.StatusInternalServerError)
		return
	}

	series, err := s.Patchwork.GetSeries(sID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(series)
}

// handlePatchworkUpdatePatch handles partial updates to patches.
// Rule 4: Preserves explicit null values (e.g. unassigning delegate via "delegate": null)
func (s *Server) handlePatchworkUpdatePatch(w http.ResponseWriter, r *http.Request) {
	idStr := chi.URLParam(r, "patchID")
	pID, err := strconv.Atoi(idStr)
	if err != nil {
		http.Error(w, "Invalid patch ID", http.StatusBadRequest)
		return
	}

	if s.Patchwork == nil {
		http.Error(w, "Patchwork client not configured", http.StatusInternalServerError)
		return
	}

	var updates map[string]any
	if err := json.NewDecoder(r.Body).Decode(&updates); err != nil {
		http.Error(w, "Invalid JSON payload", http.StatusBadRequest)
		return
	}

	updated, err := s.Patchwork.UpdatePatch(pID, updates)
	if err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		_ = json.NewEncoder(w).Encode(map[string]any{"detail": err.Error()})
		return
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(updated)
}

func (s *Server) handlePatchworkDelegateToMe(w http.ResponseWriter, r *http.Request) {
	idStr := chi.URLParam(r, "patchID")
	pID, err := strconv.Atoi(idStr)
	if err != nil {
		http.Error(w, "Invalid patch ID", http.StatusBadRequest)
		return
	}

	if s.Patchwork == nil {
		http.Error(w, "Patchwork client not configured", http.StatusInternalServerError)
		return
	}

	updated, err := s.Patchwork.DelegateToMe(pID, s.Config.SMTP.FromEmail)
	if err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		_ = json.NewEncoder(w).Encode(map[string]any{"detail": err.Error()})
		return
	}

	myUser := s.Config.Patchwork.GetMyUsername(s.Config.SMTP.FromEmail)
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"success": true,
		"patch":   updated,
		"message": fmt.Sprintf("Patch #%d delegated to %s", pID, myUser),
	})
}

func (s *Server) handlePatchworkProjects(w http.ResponseWriter, r *http.Request) {
	res := []map[string]any{
		{
			"link_name": s.Config.Patchwork.DefaultProject,
			"name":      s.Config.Patchwork.DefaultProject,
		},
	}
	for _, p := range s.Config.Patchwork.PreferredArchives {
		if p != s.Config.Patchwork.DefaultProject {
			res = append(res, map[string]any{
				"link_name": p,
				"name":      p,
			})
		}
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(res)
}
