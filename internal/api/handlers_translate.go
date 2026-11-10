package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"

	"undined-go/internal/translate"
)

type translateRequestPayload struct {
	Text       string `json:"text"`
	Subject    string `json:"subject"`
	SourceLang string `json:"source_lang"`
	TargetLang string `json:"target_lang"`
	Style      string `json:"style"`
	MaxTokens  int    `json:"max_tokens"`
}

func (s *Server) writeTranslateError(w http.ResponseWriter, err error) {
	w.Header().Set("Content-Type", "application/json")
	if errors.Is(err, translate.ErrServerUnreachable) {
		urlStr := "http://127.0.0.1:8787"
		if s.Config != nil && strings.TrimSpace(s.Config.Translate.URL) != "" {
			urlStr = strings.TrimSpace(s.Config.Translate.URL)
		}
		w.WriteHeader(http.StatusServiceUnavailable)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"error": fmt.Sprintf("Local translation server is not running at %s (%v). Please start local-jp-translate and try again.", urlStr, err),
			"code":  "translate_server_unreachable",
		})
		return
	}
	w.WriteHeader(http.StatusBadGateway)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"error": err.Error(),
		"code":  "translate_failed",
	})
}

func (s *Server) handleTranslateMessage(w http.ResponseWriter, r *http.Request) {
	if s.Config != nil && !s.Config.Translate.Enabled {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusForbidden)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"error": "Message translation is disabled in configuration ([translate] enabled = false)",
		})
		return
	}

	idParam := chi.URLParam(r, "id")
	msg, err := s.resolveMessage(idParam)
	if err != nil || msg == nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNotFound)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"error": "Message not found",
		})
		return
	}

	msg = s.ensureMessageHydrated(msg)

	var req translateRequestPayload
	if r.Body != nil {
		// Request body is optional; defaults from config are used when body is empty.
		_ = json.NewDecoder(io.LimitReader(r.Body, 1024*1024)).Decode(&req)
	}

	client := s.Translate
	if client == nil {
		client = translate.NewClient(s.Config)
	}

	opts := translate.Options{
		SourceLang: req.SourceLang,
		TargetLang: req.TargetLang,
		Style:      req.Style,
		MaxTokens:  req.MaxTokens,
	}

	res, err := client.TranslateEmail(r.Context(), msg.Subject, msg.BodyPlain, opts)
	if err != nil {
		s.writeTranslateError(w, err)
		return
	}

	preamble, _ := translate.ExtractPatchPreamble(res.TranslatedBody)
	formattedPreamble, _ := translate.ExtractPatchPreamble(res.FormattedBody)

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"id":                  msg.ID,
		"message_id":          msg.MessageID,
		"source_lang":         res.SourceLang,
		"target_lang":         res.TargetLang,
		"model":               res.Model,
		"formatted_body":      res.FormattedBody,
		"translated_body":     res.TranslatedBody,
		"formatted_preamble":  strings.TrimSpace(formattedPreamble),
		"translated_preamble": strings.TrimSpace(preamble),
		"translated_subject":  res.TranslatedSubject,
	})
}

func (s *Server) handleTranslateText(w http.ResponseWriter, r *http.Request) {
	if s.Config != nil && !s.Config.Translate.Enabled {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusForbidden)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"error": "Message translation is disabled in configuration ([translate] enabled = false)",
		})
		return
	}

	var req translateRequestPayload
	if err := json.NewDecoder(io.LimitReader(r.Body, 1024*1024)).Decode(&req); err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"error": "Invalid JSON request body",
		})
		return
	}
	if strings.TrimSpace(req.Text) == "" {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"error": "Field 'text' is required",
		})
		return
	}

	client := s.Translate
	if client == nil {
		client = translate.NewClient(s.Config)
	}

	opts := translate.Options{
		SourceLang: req.SourceLang,
		TargetLang: req.TargetLang,
		Style:      req.Style,
		MaxTokens:  req.MaxTokens,
	}

	res, err := client.TranslateEmail(r.Context(), req.Subject, req.Text, opts)
	if err != nil {
		s.writeTranslateError(w, err)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"source_lang":        res.SourceLang,
		"target_lang":        res.TargetLang,
		"model":              res.Model,
		"formatted_body":     res.FormattedBody,
		"translated_body":    res.TranslatedBody,
		"translated_subject": res.TranslatedSubject,
	})
}
