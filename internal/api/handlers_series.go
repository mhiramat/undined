package api

import (
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"

	"undined-go/internal/series"
	"undined-go/internal/storage/database"
)

func writeJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	// Best-effort JSON response write after HTTP headers are already committed.
	_ = json.NewEncoder(w).Encode(payload)
}

func writeJSONError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]any{
		"detail": message,
		"error":  message,
	})
}

func (s *Server) mapSeriesError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, series.ErrNotFound), errors.Is(err, series.ErrItemNotFound):
		writeJSONError(w, http.StatusNotFound, err.Error())
	case errors.Is(err, series.ErrAlreadySending), errors.Is(err, series.ErrAlreadySent):
		writeJSONError(w, http.StatusConflict, err.Error())
	case errors.Is(err, series.ErrInvalidInput):
		writeJSONError(w, http.StatusBadRequest, err.Error())
	default:
		log.Printf("[api] internal series error: %v", err)
		writeJSONError(w, http.StatusInternalServerError, "Internal series operation failed")
	}
}

// GET /api/series
func (s *Server) handleListSeries(w http.ResponseWriter, r *http.Request) {
	if s.DB == nil {
		writeJSONError(w, http.StatusInternalServerError, "Database not initialized")
		return
	}
	list, err := s.DB.ListPatchSeriesDrafts()
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if list == nil {
		list = make([]*database.PatchSeriesDraft, 0)
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"series": list,
	})
}

// GET /api/series/{id}
func (s *Server) handleGetSeries(w http.ResponseWriter, r *http.Request) {
	if s.Series == nil {
		writeJSONError(w, http.StatusInternalServerError, "Series manager not initialized")
		return
	}
	seriesID := chi.URLParam(r, "id")
	detail, err := s.Series.LoadDetail(seriesID)
	if err != nil {
		s.mapSeriesError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, detail)
}

type updateSeriesHeadersRequest struct {
	ToAddrs      []string `json:"to_addrs"`
	CcAddrs      []string `json:"cc_addrs"`
	InReplyTo    string   `json:"in_reply_to"`
	SendDelaySec int      `json:"send_delay_sec"`
}

// PUT /api/series/{id}/headers
func (s *Server) handleUpdateSeriesHeaders(w http.ResponseWriter, r *http.Request) {
	if s.Series == nil {
		writeJSONError(w, http.StatusInternalServerError, "Series manager not initialized")
		return
	}
	seriesID := chi.URLParam(r, "id")
	var req updateSeriesHeadersRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSONError(w, http.StatusBadRequest, "Invalid JSON payload")
		return
	}

	detail, err := s.Series.UpdateHeaders(seriesID, req.ToAddrs, req.CcAddrs, req.InReplyTo, req.SendDelaySec)
	if err != nil {
		s.mapSeriesError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, detail)
}

type updateSeriesItemRequest struct {
	Subject string   `json:"subject"`
	Body    string   `json:"body"`
	ExtraTo []string `json:"extra_to"`
	ExtraCc []string `json:"extra_cc"`
}

// PUT /api/series/{id}/items/{index}
func (s *Server) handleUpdateSeriesItem(w http.ResponseWriter, r *http.Request) {
	if s.Series == nil {
		writeJSONError(w, http.StatusInternalServerError, "Series manager not initialized")
		return
	}
	seriesID := chi.URLParam(r, "id")
	idxStr := chi.URLParam(r, "index")
	seqIndex, err := strconv.Atoi(idxStr)
	if err != nil || seqIndex < 0 {
		writeJSONError(w, http.StatusBadRequest, "Invalid item index")
		return
	}

	var req updateSeriesItemRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSONError(w, http.StatusBadRequest, "Invalid JSON payload")
		return
	}

	itemDetail, warnings, err := s.Series.UpdateItem(seriesID, seqIndex, req.Subject, req.Body, req.ExtraTo, req.ExtraCc)
	if err != nil {
		s.mapSeriesError(w, err)
		return
	}
	if warnings == nil {
		warnings = make([]series.ValidationWarning, 0)
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"item":     itemDetail,
		"warnings": warnings,
	})
}

// POST /api/series/{id}/test-send-self
func (s *Server) handleTestSendSeriesSelf(w http.ResponseWriter, r *http.Request) {
	if s.Series == nil {
		writeJSONError(w, http.StatusInternalServerError, "Series manager not initialized")
		return
	}
	seriesID := chi.URLParam(r, "id")
	sentCount, err := s.Series.TestSendSelf(seriesID)
	if err != nil {
		s.mapSeriesError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"success":    true,
		"sent_count": sentCount,
	})
}

// POST /api/series/{id}/send
func (s *Server) handleSendSeries(w http.ResponseWriter, r *http.Request) {
	if s.Series == nil {
		writeJSONError(w, http.StatusInternalServerError, "Series manager not initialized")
		return
	}
	seriesID := chi.URLParam(r, "id")
	if err := s.Series.StartSend(seriesID); err != nil {
		s.mapSeriesError(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{
		"success":   true,
		"series_id": seriesID,
		"status":    database.SeriesStatusSending,
	})
}

// POST /api/series/{id}/cancel
func (s *Server) handleCancelSeries(w http.ResponseWriter, r *http.Request) {
	if s.Series == nil {
		writeJSONError(w, http.StatusInternalServerError, "Series manager not initialized")
		return
	}
	seriesID := chi.URLParam(r, "id")
	cancelled := s.Series.CancelSend(seriesID)
	writeJSON(w, http.StatusOK, map[string]any{
		"success":   true,
		"series_id": seriesID,
		"cancelled": cancelled,
	})
}

// DELETE /api/series/{id}
func (s *Server) handleDeleteSeries(w http.ResponseWriter, r *http.Request) {
	if s.Series == nil {
		writeJSONError(w, http.StatusInternalServerError, "Series manager not initialized")
		return
	}
	seriesID := chi.URLParam(r, "id")
	if err := s.Series.DiscardSeries(seriesID); err != nil {
		s.mapSeriesError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"success":   true,
		"series_id": seriesID,
	})
}
