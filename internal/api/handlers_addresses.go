package api

import (
	"encoding/json"
	"net/http"
	"strconv"
)

// handleGetAddresses handles GET /api/addresses.
// Query parameters:
// - query / q: substring to match against address or display name
// - limit: max number of results (default 20, max 100)
func (s *Server) handleGetAddresses(w http.ResponseWriter, r *http.Request) {
	if s.DB == nil {
		http.Error(w, "Database not configured", http.StatusInternalServerError)
		return
	}

	query := r.URL.Query().Get("query")
	if query == "" {
		query = r.URL.Query().Get("q")
	}

	limit := 20
	if limitStr := r.URL.Query().Get("limit"); limitStr != "" {
		if l, err := strconv.Atoi(limitStr); err == nil && l > 0 {
			limit = l
		}
	}

	addresses, err := s.DB.SearchKnownAddresses(query, limit)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(addresses)
}

// handleCollectAddresses handles POST /api/addresses/collect.
// Rescans all messages in the database and re-populates the known_addresses table.
func (s *Server) handleCollectAddresses(w http.ResponseWriter, r *http.Request) {
	if s.DB == nil {
		http.Error(w, "Database not configured", http.StatusInternalServerError)
		return
	}

	count, err := s.DB.CollectKnownAddresses()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	total, _ := s.DB.GetKnownAddressCount()

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"success":   true,
		"processed": count,
		"total":     total,
	})
}
