package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"undined-go/internal/config"
	"undined-go/internal/constants"
	"undined-go/internal/security"
	"undined-go/internal/series"
	"undined-go/internal/smtp"
	"undined-go/internal/storage/database"
	"undined-go/internal/storage/maildir"
)

func TestSeriesRESTEndpoints(t *testing.T) {
	tmpDir := t.TempDir()
	db, err := database.NewDatabase(filepath.Join(tmpDir, "api-series.db"))
	if err != nil {
		t.Fatalf("NewDatabase failed: %v", err)
	}
	defer db.Close()

	store := maildir.NewMaildirStore(filepath.Join(tmpDir, "maildir"))
	cfg := config.NewDefaultConfig()
	cfg.General.DataDir = tmpDir
	cfg.SMTP.FromName = "Kernel Developer"
	cfg.SMTP.FromEmail = "dev@kernel.org"

	sec, err := security.NewSecurityManager("127.0.0.1", 8080, tmpDir, false)
	if err != nil {
		t.Fatalf("NewSecurityManager failed: %v", err)
	}
	smtpClient := smtp.NewClient(cfg, store, db)
	srv := NewServer(cfg, db, store, sec, nil, smtpClient, nil, nil, nil, nil, "")
	ts := httptest.NewServer(srv.Router())
	defer ts.Close()

	// Seed a 2-message series in DB + Maildir Drafts
	draftFolder, err := db.GetOrCreateFolder(constants.FolderDrafts, false)
	if err != nil {
		t.Fatalf("GetOrCreateFolder failed: %v", err)
	}
	now := time.Now().UTC()
	rootID := smtp.GenerateGitSendEmailMessageID(now, 22222, 1, "dev@kernel.org")
	p1ID := smtp.GenerateGitSendEmailMessageID(now, 22222, 2, "dev@kernel.org")

	raw0, _ := smtpClient.BuildPlainTextMessage(smtp.PlainTextMessageOptions{
		To:        "linux-kernel@vger.kernel.org",
		Subject:   "[PATCH 0/1] demo: cover",
		Body:      "Cover overview.\n",
		MessageID: rootID,
	})
	raw1, _ := smtpClient.BuildPlainTextMessage(smtp.PlainTextMessageOptions{
		To:         "linux-kernel@vger.kernel.org",
		Subject:    "[PATCH 1/1] demo: fix",
		Body:       "Fix description.\n\nSigned-off-by: Kernel Developer <dev@kernel.org>\n---\ndiff --git a/a.c b/a.c\n--- a/a.c\n+++ b/a.c\n@@ -1,1 +1,1 @@\n-\told();\n+\tnew();\n",
		InReplyTo:  rootID,
		References: rootID,
		MessageID:  p1ID,
	})
	p0Path, err := store.AddMessage(constants.FolderDrafts, []byte(raw0), constants.FlagDraft)
	if err != nil {
		t.Fatalf("AddMessage 0 failed: %v", err)
	}
	p1Path, err := store.AddMessage(constants.FolderDrafts, []byte(raw1), constants.FlagDraft)
	if err != nil {
		t.Fatalf("AddMessage 1 failed: %v", err)
	}
	id0, err := db.IndexEmailFile(draftFolder.ID, p0Path)
	if err != nil {
		t.Fatalf("IndexEmailFile 0 failed: %v", err)
	}
	id1, err := db.IndexEmailFile(draftFolder.ID, p1Path)
	if err != nil {
		t.Fatalf("IndexEmailFile 1 failed: %v", err)
	}

	seriesID := "ser_api_001"
	err = db.CreatePatchSeriesDraft(&database.PatchSeriesDraft{
		SeriesID:     seriesID,
		SeriesKey:    "topic/demo:v1",
		Branch:       "topic/demo",
		Version:      1,
		ToAddrs:      []string{"linux-kernel@vger.kernel.org"},
		CcAddrs:      []string{},
		Status:       database.SeriesStatusDraft,
		SendDelaySec: 2,
	}, []*database.PatchSeriesItem{
		{SeriesID: seriesID, SeqIndex: 0, IsCoverLetter: true, DraftMsgID: strconv.FormatInt(id0, 10), RFCMessageID: rootID, Subject: "[PATCH 0/1] demo: cover", Status: database.SeriesItemStatusDraft},
		{SeriesID: seriesID, SeqIndex: 1, DraftMsgID: strconv.FormatInt(id1, 10), RFCMessageID: p1ID, Subject: "[PATCH 1/1] demo: fix", Status: database.SeriesItemStatusDraft},
	})
	if err != nil {
		t.Fatalf("CreatePatchSeriesDraft failed: %v", err)
	}

	doReq := func(method, path string, body any) *http.Response {
		t.Helper()
		var rdr *bytes.Reader
		if body != nil {
			b, err := json.Marshal(body)
			if err != nil {
				t.Fatalf("json.Marshal failed: %v", err)
			}
			rdr = bytes.NewReader(b)
		} else {
			rdr = bytes.NewReader(nil)
		}
		req, err := http.NewRequest(method, ts.URL+path, rdr)
		if err != nil {
			t.Fatalf("NewRequest failed: %v", err)
		}
		req.Header.Set("Authorization", "Bearer "+sec.Token)
		req.Header.Set("Content-Type", "application/json")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("%s %s failed: %v", method, path, err)
		}
		return resp
	}

	// 1. GET /api/series
	resp := doReq(http.MethodGet, "/api/series", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /api/series status = %d, want 200", resp.StatusCode)
	}
	var listResp struct {
		Series []*database.PatchSeriesDraft `json:"series"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&listResp); err != nil {
		t.Fatalf("decode list failed: %v", err)
	}
	resp.Body.Close()
	if len(listResp.Series) != 1 || listResp.Series[0].SeriesID != seriesID {
		t.Fatalf("unexpected series list: %+v", listResp.Series)
	}

	// 2. GET /api/series/{id}
	resp = doReq(http.MethodGet, "/api/series/"+seriesID, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /api/series/{id} status = %d, want 200", resp.StatusCode)
	}
	var detail series.Detail
	if err := json.NewDecoder(resp.Body).Decode(&detail); err != nil {
		t.Fatalf("decode detail failed: %v", err)
	}
	resp.Body.Close()
	if len(detail.Items) != 2 || !strings.Contains(detail.Items[1].Body, "\tnew();") {
		t.Fatalf("unexpected detail items: %+v", detail.Items)
	}

	// 3. PUT /api/series/{id}/headers
	resp = doReq(http.MethodPut, "/api/series/"+seriesID+"/headers", map[string]any{
		"to_addrs":       []string{"maintainer@kernel.org"},
		"cc_addrs":       []string{"linux-kernel@vger.kernel.org"},
		"in_reply_to":    "<prev@kernel.org>",
		"send_delay_sec": 5,
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("PUT /api/series/{id}/headers status = %d, want 200", resp.StatusCode)
	}
	resp.Body.Close()

	// 4. PUT /api/series/{id}/items/1 with broken diff hunk -> returns non-blocking warning
	resp = doReq(http.MethodPut, "/api/series/"+seriesID+"/items/1", map[string]any{
		"subject": "[PATCH 1/1] demo: edited fix",
		"body":    "Edited without signoff.\n---\ndiff --git a/a.c b/a.c\n--- a/a.c\n+++ b/a.c\n@@ -1,1 +1,4 @@\n-\told();\n+\tnew();\n",
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("PUT /api/series/{id}/items/1 status = %d, want 200", resp.StatusCode)
	}
	var itemResp struct {
		Warnings []series.ValidationWarning `json:"warnings"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&itemResp); err != nil {
		t.Fatalf("decode item update failed: %v", err)
	}
	resp.Body.Close()
	if len(itemResp.Warnings) < 2 {
		t.Errorf("len(warnings) = %d (%+v), want >= 2", len(itemResp.Warnings), itemResp.Warnings)
	}

	// 5. DELETE /api/series/{id}
	resp = doReq(http.MethodDelete, "/api/series/"+seriesID, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("DELETE /api/series/{id} status = %d, want 200", resp.StatusCode)
	}
	resp.Body.Close()

	resp = doReq(http.MethodGet, "/api/series/"+seriesID, nil)
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("GET deleted series status = %d, want 404", resp.StatusCode)
	}
}
