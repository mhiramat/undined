package api

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"undined-go/internal/config"
	"undined-go/internal/constants"
	"undined-go/internal/imap"
	"undined-go/internal/patchwork"
	"undined-go/internal/security"
	"undined-go/internal/storage/database"
	"undined-go/internal/storage/maildir"
)

func setupTestAPIServer(t *testing.T) (*Server, *database.Database, string, string) {
	tmpDir, err := os.MkdirTemp("", "undined-api-test-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}

	dbPath := filepath.Join(tmpDir, "test.db")
	db, err := database.NewDatabase(dbPath)
	if err != nil {
		t.Fatalf("Failed to init database: %v", err)
	}

	mailPath := filepath.Join(tmpDir, "Mail")
	store := maildir.NewMaildirStore(mailPath)

	sec, err := security.NewSecurityManager("127.0.0.1", 8080, tmpDir)
	if err != nil {
		t.Fatalf("Failed to init security manager: %v", err)
	}

	cfg := config.NewDefaultConfig()
	cfg.General.DataDir = tmpDir
	cfg.General.MaildirPath = mailPath
	cfg.General.DBPath = dbPath

	frontendDir := filepath.Join(tmpDir, "frontend")
	_ = os.MkdirAll(filepath.Join(frontendDir, "src"), 0755)
	_ = os.WriteFile(filepath.Join(frontendDir, "index.html"), []byte("<html><body>Undine UI</body></html>"), 0644)

	imapPoller := imap.NewPoller(cfg, store, db)
	server := NewServer(cfg, db, store, sec, imapPoller, nil, nil, nil, nil, nil, frontendDir)
	return server, db, sec.Token, tmpDir
}

func TestSecurityMiddlewareAndAuth(t *testing.T) {
	server, db, token, tmpDir := setupTestAPIServer(t)
	defer os.RemoveAll(tmpDir)
	defer db.Close()

	ts := httptest.NewServer(server.Router())
	defer ts.Close()

	client := ts.Client()

	// 1. Unauthenticated request to /api/folders should be 401
	req, _ := http.NewRequest("GET", ts.URL+"/api/folders", nil)
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("Request failed: %v", err)
	}
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("Expected 401 Unauthorized, got %d", resp.StatusCode)
	}

	// 2. Authenticated request with Bearer token to /api/folders should be 200
	req, _ = http.NewRequest("GET", ts.URL+"/api/folders", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err = client.Do(req)
	if err != nil {
		t.Fatalf("Request failed: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Errorf("Expected 200 OK, got %d", resp.StatusCode)
	}

	// 3. DNS rebinding rejection: invalid Host header should be 403
	req, _ = http.NewRequest("GET", ts.URL+"/api/folders", nil)
	req.Host = "malicious.attacker.com"
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err = client.Do(req)
	if err != nil {
		t.Fatalf("Request failed: %v", err)
	}
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("Expected 403 Forbidden for bad Host, got %d", resp.StatusCode)
	}

	// 4. CSRF origin rejection on POST
	req, _ = http.NewRequest("POST", ts.URL+"/api/composer/preview-quote", strings.NewReader("{}"))
	req.Header.Set("Origin", "http://evil-site.com")
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err = client.Do(req)
	if err != nil {
		t.Fatalf("Request failed: %v", err)
	}
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("Expected 403 Forbidden for bad Origin, got %d", resp.StatusCode)
	}

	// 5. CSRF Referer rejection when Origin header is omitted
	req, _ = http.NewRequest("POST", ts.URL+"/api/composer/preview-quote", strings.NewReader("{}"))
	req.Header.Set("Referer", "http://evil-site.com/attack.html")
	req.AddCookie(&http.Cookie{Name: constants.SessionCookieName, Value: token})
	resp, err = client.Do(req)
	if err != nil {
		t.Fatalf("Request failed: %v", err)
	}
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("Expected 403 Forbidden for bad Referer without Origin, got %d", resp.StatusCode)
	}

	// 6. Cookie-authenticated POST without Origin or Referer should be rejected (403)
	req, _ = http.NewRequest("POST", ts.URL+"/api/composer/preview-quote", strings.NewReader("{}"))
	req.AddCookie(&http.Cookie{Name: constants.SessionCookieName, Value: token})
	resp, err = client.Do(req)
	if err != nil {
		t.Fatalf("Request failed: %v", err)
	}
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("Expected 403 Forbidden for cookie-authenticated POST missing Origin and Referer, got %d", resp.StatusCode)
	}
}

func TestIndexAndLoginFlow(t *testing.T) {
	server, db, token, tmpDir := setupTestAPIServer(t)
	defer os.RemoveAll(tmpDir)
	defer db.Close()

	ts := httptest.NewServer(server.Router())
	defer ts.Close()

	// Disallow auto-redirect to inspect 302 responses
	noRedirectClient := &http.Client{
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}

	// 1. Unauthenticated GET / should render login HTML (status 401)
	resp, err := ts.Client().Get(ts.URL + "/")
	if err != nil {
		t.Fatalf("GET / failed: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("Expected 401 for unauth GET /, got %d", resp.StatusCode)
	}
	if !strings.Contains(string(body), "UndineD") {
		t.Errorf("Expected UndineD login HTML, got %s", string(body))
	}

	// 2. GET /?token=<valid_token> should redirect to / with session cookie
	resp, err = noRedirectClient.Get(ts.URL + "/?token=" + token)
	if err != nil {
		t.Fatalf("GET /?token=... failed: %v", err)
	}
	if resp.StatusCode != http.StatusFound {
		t.Errorf("Expected 302 Redirect for valid token URL, got %d", resp.StatusCode)
	}
	var sessionCookie *http.Cookie
	for _, c := range resp.Cookies() {
		if c.Name == constants.SessionCookieName {
			sessionCookie = c
			break
		}
	}
	if sessionCookie == nil || sessionCookie.Value != token {
		t.Fatalf("Expected session cookie set to token, got %v", sessionCookie)
	}

	// 3. GET / with session cookie should serve frontend/index.html (200 OK)
	req, _ := http.NewRequest("GET", ts.URL+"/", nil)
	req.AddCookie(sessionCookie)
	resp, err = ts.Client().Do(req)
	if err != nil {
		t.Fatalf("GET / with cookie failed: %v", err)
	}
	body, _ = io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Errorf("Expected 200 OK for cookie authenticated GET /, got %d", resp.StatusCode)
	}
	if !strings.Contains(string(body), "Undine UI") {
		t.Errorf("Expected frontend index HTML, got %s", string(body))
	}
}

func TestFolderAndMessageEndpoints(t *testing.T) {
	server, db, token, tmpDir := setupTestAPIServer(t)
	defer os.RemoveAll(tmpDir)
	defer db.Close()

	ts := httptest.NewServer(server.Router())
	defer ts.Close()

	client := ts.Client()

	// Create test folder and message
	folder, err := db.GetOrCreateFolder("INBOX", true)
	if err != nil {
		t.Fatalf("Failed to create folder: %v", err)
	}

	testEml := []byte("Message-ID: <patch-api-101@kernel.org>\n" +
		"From: Kernel Developer <dev@example.com>\n" +
		"To: linux-trace@vger.kernel.org\n" +
		"Subject: [PATCH] tracing: Fix probe\n" +
		"Date: Sat, 17 Oct 2026 12:00:00 +0000\n\n" +
		"--- a/kernel/trace.c\n+++ b/kernel/trace.c\n@@ -1,3 +1,3 @@\n-old\n+new\n")

	emlPath := filepath.Join(tmpDir, "msg.eml")
	_ = os.WriteFile(emlPath, testEml, 0644)
	msgID, err := db.IndexEmailFile(folder.ID, emlPath)
	if err != nil {
		t.Fatalf("Failed to index message: %v", err)
	}

	// 1. GET /api/messages?folder_id=...
	req, _ := http.NewRequest("GET", fmt.Sprintf("%s/api/messages?folder_id=%d", ts.URL, folder.ID), nil)
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("GET /api/messages failed: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("Expected 200 OK, got %d", resp.StatusCode)
	}

	var msgs []*database.Message
	_ = json.NewDecoder(resp.Body).Decode(&msgs)
	if len(msgs) != 1 || msgs[0].Subject != "[PATCH] tracing: Fix probe" {
		t.Errorf("Unexpected messages list: %v", msgs)
	}

	// 2. GET /api/messages/{id}
	req, _ = http.NewRequest("GET", fmt.Sprintf("%s/api/messages/%d", ts.URL, msgID), nil)
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err = client.Do(req)
	if err != nil {
		t.Fatalf("GET /api/messages/{id} failed: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("Expected 200 OK, got %d", resp.StatusCode)
	}

	var detail map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&detail)
	if detail["message"] == nil {
		t.Errorf("Missing message in detail response: %v", detail)
	}

	// 3. Comments CRUD
	commentPayload := `{"file_path": "kernel/trace.c", "line_no": 2, "side": "new", "text": "Check this lock"}`
	req, _ = http.NewRequest("POST", fmt.Sprintf("%s/api/messages/%d/comments", ts.URL, msgID), strings.NewReader(commentPayload))
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	resp, err = client.Do(req)
	if err != nil {
		t.Fatalf("POST comment failed: %v", err)
	}
	var postCommentRes struct {
		Success bool           `json:"success"`
		Comment map[string]any `json:"comment"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&postCommentRes)
	if !postCommentRes.Success || postCommentRes.Comment == nil || postCommentRes.Comment["text"] != "Check this lock" {
		t.Fatalf("Unexpected POST comment response: %v", postCommentRes)
	}

	req, _ = http.NewRequest("GET", fmt.Sprintf("%s/api/messages/%d/comments", ts.URL, msgID), nil)
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err = client.Do(req)
	if err != nil {
		t.Fatalf("GET comments failed: %v", err)
	}
	var getCommentsRes struct {
		MessageID string           `json:"message_id"`
		Comments  []map[string]any `json:"comments"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&getCommentsRes)
	if len(getCommentsRes.Comments) != 1 || getCommentsRes.Comments[0]["text"] != "Check this lock" {
		t.Errorf("Unexpected comments response: %v", getCommentsRes)
	}

	// 4. Comment on DESCRIPTION
	descPayload := `{"file": "description", "line_no": 1, "side": "new", "text": "Commit title review"}`
	req, _ = http.NewRequest("POST", fmt.Sprintf("%s/api/messages/%d/comments", ts.URL, msgID), strings.NewReader(descPayload))
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	resp, err = client.Do(req)
	if err != nil {
		t.Fatalf("POST description comment failed: %v", err)
	}
	var descCommentRes struct {
		Success bool           `json:"success"`
		Comment map[string]any `json:"comment"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&descCommentRes)
	if !descCommentRes.Success || descCommentRes.Comment["file_path"] != "DESCRIPTION" {
		t.Errorf("expected canonical file_path DESCRIPTION, got %v", descCommentRes.Comment)
	}
}

func TestRule4PatchworkUpdateWithNullDelegateEndpoint(t *testing.T) {
	// Rule 4: Verify that PATCH /api/patchwork/patches/{id} with {"delegate": null}
	// forwards null to the Patchwork upstream API
	var receivedUpstreamBody map[string]any
	mockPatchwork := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		bodyBytes, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(bodyBytes, &receivedUpstreamBody)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"id": 88, "delegate": null, "state": "rfc"}`))
	}))
	defer mockPatchwork.Close()

	server, db, token, tmpDir := setupTestAPIServer(t)
	defer os.RemoveAll(tmpDir)
	defer db.Close()

	// Point server's Patchwork client to mock upstream
	server.Config.Patchwork.URL = mockPatchwork.URL
	server.Config.Patchwork.APIVersion = "1.4"
	server.Config.Patchwork.Token = "upstream-token"
	server.Patchwork = patchwork.NewClient(server.Config, db, server.Maildir)

	ts := httptest.NewServer(server.Router())
	defer ts.Close()

	// Send PATCH /api/patchwork/patches/88 with {"state": "rfc", "delegate": null}
	jsonPayload := `{"state": "rfc", "delegate": null}`
	req, _ := http.NewRequest("PATCH", ts.URL+"/api/patchwork/patches/88", bytes.NewReader([]byte(jsonPayload)))
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")

	resp, err := ts.Client().Do(req)
	if err != nil {
		t.Fatalf("PATCH request failed: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("Expected 200 OK, got %d: %s", resp.StatusCode, string(body))
	}

	val, keyExists := receivedUpstreamBody["delegate"]
	if !keyExists {
		t.Errorf("Rule 4 violation: 'delegate' key was omitted instead of forwarded as null")
	}
	if val != nil {
		t.Errorf("Expected delegate to be null, got %v", val)
	}
}

func TestFolderSyncAndThreadedMessagesEndpoints(t *testing.T) {
	server, db, token, tmpDir := setupTestAPIServer(t)
	defer os.RemoveAll(tmpDir)
	defer db.Close()

	ts := httptest.NewServer(server.Router())
	defer ts.Close()
	client := ts.Client()

	folder, err := db.GetOrCreateFolder("INBOX", true)
	if err != nil {
		t.Fatalf("Failed to create folder: %v", err)
	}

	// 1. Test GET /api/folders/{id}/sync-status
	req, _ := http.NewRequest("GET", fmt.Sprintf("%s/api/folders/%d/sync-status", ts.URL, folder.ID), nil)
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("sync-status failed: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 OK for sync-status, got %d", resp.StatusCode)
	}
	var syncSt map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&syncSt)
	if syncSt["status"] != "idle" {
		t.Errorf("expected status idle, got %v", syncSt["status"])
	}

	// 2. Test POST /api/folders/{id}/sync-cancel
	req, _ = http.NewRequest("POST", fmt.Sprintf("%s/api/folders/%d/sync-cancel", ts.URL, folder.ID), nil)
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err = client.Do(req)
	if err != nil {
		t.Fatalf("sync-cancel failed: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 OK for sync-cancel, got %d", resp.StatusCode)
	}
	var cancelRes map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&cancelRes)
	if cancelRes["status"] != "ok" {
		t.Errorf("expected status ok, got %v", cancelRes["status"])
	}

	// 3. Test POST /api/folders/{id}/sync-full (without IMAP username configured, should fail cleanly)
	req, _ = http.NewRequest("POST", fmt.Sprintf("%s/api/folders/%d/sync-full", ts.URL, folder.ID), nil)
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err = client.Do(req)
	if err != nil {
		t.Fatalf("sync-full failed: %v", err)
	}
	if resp.StatusCode != http.StatusInternalServerError {
		t.Errorf("expected 500 when IMAP credentials missing, got %d", resp.StatusCode)
	}

	// 4. Test GET /api/messages with threading and tag resolution
	m1ID, _ := db.InsertHeaderSummary(folder.ID, 1, []byte("Message-ID: <root-msg@kernel.org>\r\nSubject: Test Subject\r\nDate: Mon, 12 Oct 2026 10:00:00 +0000\r\n\r\n"), true, "$cl_1")
	m2ID, _ := db.InsertHeaderSummary(folder.ID, 2, []byte("Message-ID: <reply-msg@kernel.org>\r\nIn-Reply-To: <root-msg@kernel.org>\r\nSubject: Re: Test Subject\r\nDate: Mon, 12 Oct 2026 11:00:00 +0000\r\n\r\n"), false, "")

	if m1ID == 0 || m2ID == 0 {
		t.Fatalf("failed inserting test headers")
	}

	req, _ = http.NewRequest("GET", fmt.Sprintf("%s/api/messages?folder_id=%d", ts.URL, folder.ID), nil)
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err = client.Do(req)
	if err != nil {
		t.Fatalf("GET /api/messages failed: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 OK, got %d", resp.StatusCode)
	}

	var msgs []*database.Message
	_ = json.NewDecoder(resp.Body).Decode(&msgs)
	if len(msgs) != 2 {
		t.Fatalf("expected 2 messages, got %d", len(msgs))
	}

	// Verify root message has children and tag info
	if msgs[0].MessageID != "root-msg@kernel.org" {
		t.Fatalf("expected root-msg first in thread, got %s", msgs[0].MessageID)
	}
	if !msgs[0].HasChildren || msgs[0].ThreadDepth != 0 {
		t.Fatalf("expected root to have children and depth 0: %+v", msgs[0])
	}
	if msgs[0].TagKey == nil || *msgs[0].TagKey != "$cl_1" || msgs[0].TagName == nil || *msgs[0].TagName != "Red" {
		t.Fatalf("expected tag $cl_1 (Red) on root message, got %+v", msgs[0])
	}

	// Verify reply message has depth 1 and prefix
	if msgs[1].MessageID != "reply-msg@kernel.org" {
		t.Fatalf("expected reply-msg second in thread, got %s", msgs[1].MessageID)
	}
	if msgs[1].ThreadDepth != 1 || msgs[1].ThreadPrefix != "└─ " {
		t.Fatalf("expected reply to have depth 1 and prefix '└─ ', got depth %d, prefix %q", msgs[1].ThreadDepth, msgs[1].ThreadPrefix)
	}
}

func TestAPIConsistencyAndReturnValues(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "undined-api-consistency-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	dbPath := filepath.Join(tempDir, "undined.db")
	db, err := database.NewDatabase(dbPath)
	if err != nil {
		t.Fatalf("failed to create database: %v", err)
	}
	defer db.Close()

	maildirPath := filepath.Join(tempDir, "maildir")
	store := maildir.NewMaildirStore(maildirPath)

	cfg := config.NewDefaultConfig()
	cfg.General.DataDir = tempDir
	cfg.General.MaildirPath = maildirPath
	cfg.General.DBPath = dbPath
	cfg.General.KernelGitDir = tempDir
	cfg.SMTP.FromName = "Kernel Developer"
	cfg.SMTP.FromEmail = "dev@example.com"
	cfg.Patchwork.Enabled = true
	cfg.Patchwork.DelegateUsers = []string{"dev", "rostedt"}
	cfg.Patchwork.Token = "test-token"
	cfg.Tags = map[string]config.TagDefinition{
		"$cl_1": {Tag: "Red", Color: "#ff0000"},
	}

	sec, err := security.NewSecurityManager("127.0.0.1", 8080, tempDir)
	if err != nil {
		t.Fatalf("failed to init security manager: %v", err)
	}
	server := NewServer(cfg, db, store, sec, nil, nil, nil, nil, nil, nil, "")
	ts := httptest.NewServer(server.Router())
	defer ts.Close()

	token := sec.Token
	client := &http.Client{}

	// 1. Test GET /api/config returns flat fields for frontend compatibility
	req, _ := http.NewRequest("GET", ts.URL+"/api/config", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("GET /api/config failed: %v", err)
	}
	var configRes map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&configRes)
	if configRes["from_name"] != "Kernel Developer" || configRes["from_email"] != "dev@example.com" {
		t.Errorf("expected flat from_name/from_email in config, got %v", configRes)
	}
	if configRes["patchwork_can_update"] != true || configRes["patchwork_enabled"] != true {
		t.Errorf("expected patchwork_can_update=true and patchwork_enabled=true, got %v", configRes)
	}

	// 2. Test 404 for non-existent folders on sync status and cancel
	req, _ = http.NewRequest("GET", ts.URL+"/api/folders/99999/sync-status", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	resp, _ = client.Do(req)
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("expected 404 for non-existent folder sync status, got %d", resp.StatusCode)
	}

	req, _ = http.NewRequest("POST", ts.URL+"/api/folders/99999/sync-cancel", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	resp, _ = client.Do(req)
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("expected 404 for non-existent folder sync cancel, got %d", resp.StatusCode)
	}

	// 3. Setup a message in inbox
	f, _ := db.GetOrCreateFolder("INBOX", true)
	msgFile, _ := store.AddMessage("INBOX", []byte("Subject: Test Patch\r\nMessage-ID: <patch-1@kernel.org>\r\nFrom: Alice <alice@example.com>\r\nDate: Mon, 12 Oct 2026 10:00:00 +0000\r\n\r\n--- a/file.c\n+++ b/file.c\n@@ -1,1 +1,1 @@\n-old\n+new\n"), "")
	msgID, _ := db.IndexEmailFile(f.ID, msgFile)

	// Test POST /api/messages/{id}/read with {"is_read": true}
	req, _ = http.NewRequest("POST", fmt.Sprintf("%s/api/messages/%d/read", ts.URL, msgID), strings.NewReader(`{"is_read": true}`))
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	resp, _ = client.Do(req)
	var readRes map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&readRes)
	if readRes["is_read"] != true || readRes["status"] != "ok" || readRes["message_db_id"] == nil {
		t.Errorf("unexpected read response: %v", readRes)
	}

	// Test POST /api/messages/{id}/replied with {"is_replied": true}
	req, _ = http.NewRequest("POST", fmt.Sprintf("%s/api/messages/%d/replied", ts.URL, msgID), strings.NewReader(`{"is_replied": true}`))
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	resp, _ = client.Do(req)
	var repliedRes map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&repliedRes)
	if repliedRes["is_replied"] != true || repliedRes["status"] != "ok" || repliedRes["message_db_id"] == nil {
		t.Errorf("unexpected replied response: %v", repliedRes)
	}

	// Test POST /api/messages/{id}/tag with {"tag": "$cl_1"}
	req, _ = http.NewRequest("POST", fmt.Sprintf("%s/api/messages/%d/tag", ts.URL, msgID), strings.NewReader(`{"tag": "$cl_1"}`))
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	resp, _ = client.Do(req)
	var tagRes map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&tagRes)
	if tagRes["tag_key"] != "$cl_1" || tagRes["tag_name"] != "Red" || tagRes["tag_color"] != "#ff0000" {
		t.Errorf("unexpected tag response: %v", tagRes)
	}

	// Test POST /api/composer/preview-quote returns quote_text and body
	previewPayload := fmt.Sprintf(`{"message_id": "%d", "general_comment": "Looks good"}`, msgID)
	req, _ = http.NewRequest("POST", ts.URL+"/api/composer/preview-quote", strings.NewReader(previewPayload))
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	resp, _ = client.Do(req)
	var previewRes map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&previewRes)
	if previewRes["quote_text"] == nil || previewRes["body"] == nil || !strings.Contains(previewRes["quote_text"].(string), "Looks good") {
		t.Errorf("unexpected preview quote response: %v", previewRes)
	}

	// Test DELETE /api/messages/{id} returns deleted_id and status
	req, _ = http.NewRequest("DELETE", fmt.Sprintf("%s/api/messages/%d", ts.URL, msgID), nil)
	req.Header.Set("Authorization", "Bearer "+token)
	resp, _ = client.Do(req)
	var delRes map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&delRes)
	if delRes["status"] != "ok" || delRes["deleted_id"] == nil {
		t.Errorf("unexpected delete response: %v", delRes)
	}
}

func TestAIReviewMessageDetailAndLinks(t *testing.T) {
	server, db, token, tmpDir := setupTestAPIServer(t)
	defer os.RemoveAll(tmpDir)
	defer db.Close()

	ts := httptest.NewServer(server.Router())
	defer ts.Close()

	client := &http.Client{}

	// 1. Create a patch message in b4-imports
	b4Folder, err := db.GetOrCreateFolder("b4-imports", true)
	if err != nil {
		t.Fatalf("failed creating b4 folder: %v", err)
	}

	origID, err := db.InsertHeaderSummary(
		b4Folder.ID,
		501,
		[]byte("Message-ID: <reviewed-patch@kernel.org>\r\nSubject: [PATCH] mm: fix slab memory leak\r\nFrom: Dev <dev@kernel.org>\r\nDate: Sat, 17 Oct 2026 10:00:00 +0000\r\n\r\n"),
		true,
		"",
	)
	if err != nil {
		t.Fatalf("failed creating orig patch: %v", err)
	}

	// 2. Create AI review message in AI-reviews folder linking to reviewed-patch@kernel.org
	aiFolder, err := db.GetOrCreateFolder("AI-reviews", false)
	if err != nil {
		t.Fatalf("failed creating AI-reviews folder: %v", err)
	}

	aiReviewID, err := db.InsertHeaderSummary(
		aiFolder.ID,
		0,
		[]byte("From: AI review\r\nSubject: AI review: [PATCH] mm: fix slab memory leak\r\nDate: Sat, 17 Oct 2026 10:05:00 +0000\r\nIn-Reply-To: <reviewed-patch@kernel.org>\r\nReferences: <reviewed-patch@kernel.org>\r\n\r\n"),
		false,
		"",
	)
	if err != nil {
		t.Fatalf("failed creating AI review message: %v", err)
	}

	// 3. Fetch message detail via GET /api/messages/{id}
	req, _ := http.NewRequest("GET", fmt.Sprintf("%s/api/messages/%d", ts.URL, aiReviewID), nil)
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("GET /api/messages/{id} failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 OK, got %d", resp.StatusCode)
	}

	var detail struct {
		Message struct {
			ID              int64          `json:"id"`
			Subject         string         `json:"subject"`
			IsAIReview      bool           `json:"is_ai_review"`
			IsPatch         bool           `json:"is_patch"`
			ReviewedMessage map[string]any `json:"reviewed_message"`
		} `json:"message"`
		ReviewedMessage map[string]any `json:"reviewed_message"`
		Patch           any            `json:"patch"`
		ParsedDiff      any            `json:"parsed_diff"`
		MissingParent   bool           `json:"missing_parent"`
		LoreURL         string         `json:"lore_url"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&detail); err != nil {
		t.Fatalf("failed to decode message detail: %v", err)
	}

	// Verify AI-review flags
	if !detail.Message.IsAIReview {
		t.Errorf("expected is_ai_review=true, got %v", detail.Message.IsAIReview)
	}
	if detail.Message.IsPatch {
		t.Errorf("expected is_patch=false for AI review message, got %v", detail.Message.IsPatch)
	}
	if detail.Patch != nil || detail.ParsedDiff != nil {
		t.Errorf("expected patch and parsed_diff to be nil for AI review message")
	}
	if detail.MissingParent {
		t.Errorf("expected missing_parent=false for AI review message")
	}

	// Verify top-level reviewed_message
	if detail.ReviewedMessage == nil {
		t.Fatalf("expected reviewed_message in detail response, got nil")
	}
	if fmt.Sprintf("%v", detail.ReviewedMessage["id"]) != fmt.Sprintf("%d", origID) {
		t.Errorf("expected reviewed_message.id=%d, got %v", origID, detail.ReviewedMessage["id"])
	}
	if detail.ReviewedMessage["folder_name"] != "b4-imports" {
		t.Errorf("expected reviewed_message.folder_name='b4-imports', got %v", detail.ReviewedMessage["folder_name"])
	}
	if detail.ReviewedMessage["message_id"] != "reviewed-patch@kernel.org" {
		t.Errorf("expected reviewed_message.message_id='reviewed-patch@kernel.org', got %v", detail.ReviewedMessage["message_id"])
	}

	// Verify nested message.reviewed_message
	if detail.Message.ReviewedMessage == nil {
		t.Fatalf("expected detail.message.reviewed_message to be populated, got nil")
	}
	if fmt.Sprintf("%v", detail.Message.ReviewedMessage["id"]) != fmt.Sprintf("%d", origID) {
		t.Errorf("expected detail.message.reviewed_message.id=%d, got %v", origID, detail.Message.ReviewedMessage["id"])
	}
}

func TestRESTAPIAccessLogging(t *testing.T) {
	server, db, token, tmpDir := setupTestAPIServer(t)
	defer os.RemoveAll(tmpDir)
	defer db.Close()

	logFile := filepath.Join(tmpDir, "api_access.log")
	server.Config.General.LogFile = logFile

	logBuf := &bytes.Buffer{}
	server.SetLogWriter(logBuf)

	ts := httptest.NewServer(server.Router())
	defer ts.Close()

	client := ts.Client()

	// 1. Valid API call: GET /api/folders
	req1, err := http.NewRequest("GET", ts.URL+"/api/folders", nil)
	if err != nil {
		t.Fatalf("failed to create req1: %v", err)
	}
	req1.Header.Set("Authorization", "Bearer "+token)
	resp1, err := client.Do(req1)
	if err != nil {
		t.Fatalf("req1 failed: %v", err)
	}
	_ = resp1.Body.Close()
	if resp1.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 OK, got %d", resp1.StatusCode)
	}

	// 2. Unauthorized API call: GET /api/messages
	req2, err := http.NewRequest("GET", ts.URL+"/api/messages", nil)
	if err != nil {
		t.Fatalf("failed to create req2: %v", err)
	}
	resp2, err := client.Do(req2)
	if err != nil {
		t.Fatalf("req2 failed: %v", err)
	}
	_ = resp2.Body.Close()
	if resp2.StatusCode != http.StatusUnauthorized {
		t.Fatalf("expected 401 Unauthorized, got %d", resp2.StatusCode)
	}

	// 3. Static asset call: GET /favicon.svg
	_ = os.WriteFile(filepath.Join(server.FrontendDir, "favicon.svg"), []byte("<svg></svg>"), 0644)
	req3, err := http.NewRequest("GET", ts.URL+"/favicon.svg", nil)
	if err != nil {
		t.Fatalf("failed to create req3: %v", err)
	}
	resp3, err := client.Do(req3)
	if err != nil {
		t.Fatalf("req3 failed: %v", err)
	}
	_ = resp3.Body.Close()
	if resp3.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 OK, got %d", resp3.StatusCode)
	}

	// 4. Access with ?token= query parameter
	req4, err := http.NewRequest("GET", ts.URL+"/api/folders?token="+token, nil)
	if err != nil {
		t.Fatalf("failed to create req4: %v", err)
	}
	resp4, err := client.Do(req4)
	if err != nil {
		t.Fatalf("req4 failed: %v", err)
	}
	_ = resp4.Body.Close()

	// Verify log writer output
	bufStr := logBuf.String()
	if !strings.Contains(bufStr, "[INFO] [undine.api] REST API access: GET /api/folders - 200 OK") {
		t.Errorf("expected 200 log in buffer, got:\n%s", bufStr)
	}
	if !strings.Contains(bufStr, "[WARN] [undine.api] REST API access: GET /api/messages - 401 Unauthorized") {
		t.Errorf("expected 401 log in buffer, got:\n%s", bufStr)
	}
	if strings.Contains(bufStr, "favicon.svg") {
		t.Errorf("did not expect static /favicon.svg to be logged in REST API access log, got:\n%s", bufStr)
	}
	if strings.Contains(bufStr, token) {
		t.Errorf("auth token must be redacted in logs, but found in buffer: %s", bufStr)
	}
	if !strings.Contains(bufStr, "token=REDACTED") {
		t.Errorf("expected token=REDACTED in log buffer, got:\n%s", bufStr)
	}

	// Verify log file output and permissions
	fi, err := os.Stat(logFile)
	if err != nil {
		t.Fatalf("failed to stat log file %s: %v", logFile, err)
	}
	if fi.Mode().Perm() != 0600 {
		t.Errorf("expected log file permissions 0600, got %o", fi.Mode().Perm())
	}

	fileData, err := os.ReadFile(logFile)
	if err != nil {
		t.Fatalf("failed to read log file %s: %v", logFile, err)
	}
	fileStr := string(fileData)
	if !strings.Contains(fileStr, "[INFO] [undine.api] REST API access: GET /api/folders - 200 OK") {
		t.Errorf("expected 200 log in log file, got:\n%s", fileStr)
	}
	if !strings.Contains(fileStr, "[WARN] [undine.api] REST API access: GET /api/messages - 401 Unauthorized") {
		t.Errorf("expected 401 log in log file, got:\n%s", fileStr)
	}
	if strings.Contains(fileStr, token) {
		t.Errorf("auth token must be redacted in log file, but found in file: %s", fileStr)
	}
	if !strings.Contains(fileStr, "token=REDACTED") {
		t.Errorf("expected token=REDACTED in log file, got:\n%s", fileStr)
	}
}

func startAPIMockIMAPServer(t *testing.T) (net.Listener, int, *[]string) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Failed to start mock IMAP server: %v", err)
	}

	var commands []string
	var mu sync.Mutex

	go func() {
		for {
			conn, err := l.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer c.Close()
				fmt.Fprintf(c, "* OK [CAPABILITY IMAP4rev1] Mock IMAP Server Ready\r\n")
				r := bufio.NewReader(c)
				for {
					line, err := r.ReadString('\n')
					if err != nil {
						return
					}
					line = strings.TrimRight(line, "\r\n")
					parts := strings.SplitN(line, " ", 3)
					if len(parts) < 2 {
						continue
					}
					tag := parts[0]
					cmd := strings.ToUpper(parts[1])

					mu.Lock()
					commands = append(commands, line)
					mu.Unlock()

					switch cmd {
					case "CAPABILITY":
						fmt.Fprintf(c, "* CAPABILITY IMAP4rev1\r\n%s OK CAPABILITY completed\r\n", tag)
					case "LOGIN":
						fmt.Fprintf(c, "%s OK [CAPABILITY IMAP4rev1] LOGIN completed\r\n", tag)
					case "SELECT":
						fmt.Fprintf(c, "* 10 EXISTS\r\n* 0 RECENT\r\n* FLAGS (\\Seen \\Answered $cl_1 $cl_2 $cl_3)\r\n* OK [PERMANENTFLAGS (\\Seen \\Answered $cl_1 $cl_2 $cl_3 \\*)] Flags permitted.\r\n* OK [UIDVALIDITY 12345678] UIDs valid\r\n%s OK [READ-WRITE] SELECT completed\r\n", tag)
					case "UID":
						arg := ""
						if len(parts) > 2 {
							arg = parts[2]
						}
						fmt.Fprintf(c, "* 1 FETCH (UID 777 FLAGS (%s))\r\n%s OK UID STORE completed\r\n", arg, tag)
					case "LOGOUT":
						fmt.Fprintf(c, "* BYE Mock IMAP logging out\r\n%s OK LOGOUT completed\r\n", tag)
						return
					default:
						fmt.Fprintf(c, "%s OK %s completed\r\n", tag, cmd)
					}
				}
			}(conn)
		}
	}()

	return l, l.Addr().(*net.TCPAddr).Port, &commands
}

func TestAPIIMAPFlagAndTagSync(t *testing.T) {
	listener, port, commands := startAPIMockIMAPServer(t)
	defer listener.Close()

	server, db, token, tmpDir := setupTestAPIServer(t)
	defer os.RemoveAll(tmpDir)

	// Configure server.IMAP with mock IMAP connection
	server.Config.IMAP.Host = "127.0.0.1"
	server.Config.IMAP.Port = port
	server.Config.IMAP.UseSSL = false
	server.Config.IMAP.Username = "dev@example.com"
	server.Config.IMAP.Password = "secret"
	server.IMAP = imap.NewPoller(server.Config, server.Maildir, db)

	ts := httptest.NewServer(server.Router())
	defer ts.Close()

	client := &http.Client{}

	// Insert test message in folder INBOX with imap_uid 777
	folder, err := db.GetOrCreateFolder("INBOX", true)
	if err != nil {
		t.Fatalf("failed to create folder: %v", err)
	}

	headers := []byte("Message-ID: <patch-imap-777@kernel.org>\r\n" +
		"Date: Sat, 17 Oct 2026 12:00:00 +0000\r\n" +
		"From: Kernel Developer <dev@example.com>\r\n" +
		"Subject: [PATCH] test imap flag sync\r\n\r\n")

	msgID, err := db.InsertHeaderSummary(folder.ID, 777, headers, false, "")
	if err != nil {
		t.Fatalf("failed to insert header summary: %v", err)
	}

	// 1. Mark as read
	req, _ := http.NewRequest("POST", fmt.Sprintf("%s/api/messages/%d/read", ts.URL, msgID), strings.NewReader(`{"is_read": true}`))
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("read request failed: %v", err)
	}
	_ = resp.Body.Close()

	// 2. Set custom color tag $cl_3 (Green)
	req, _ = http.NewRequest("POST", fmt.Sprintf("%s/api/messages/%d/tag", ts.URL, msgID), strings.NewReader(`{"tag": "$cl_3"}`))
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	resp, err = client.Do(req)
	if err != nil {
		t.Fatalf("tag request failed: %v", err)
	}
	_ = resp.Body.Close()

	// Wait briefly for background goroutines to finish
	time.Sleep(100 * time.Millisecond)

	var seenFound, tagFound bool
	for _, cmd := range *commands {
		if strings.Contains(cmd, "UID STORE 777 +FLAGS.SILENT (\\Seen)") {
			seenFound = true
		}
		if strings.Contains(cmd, "UID STORE 777 +FLAGS.SILENT ($cl_3)") {
			tagFound = true
		}
	}

	if !seenFound {
		t.Errorf("expected IMAP server to receive read flag (\\Seen), commands received: %v", *commands)
	}
	if !tagFound {
		t.Errorf("expected IMAP server to receive custom tag ($cl_3), commands received: %v", *commands)
	}
}

func TestKnownAddressesAPI(t *testing.T) {
	server, db, token, tmpDir := setupTestAPIServer(t)
	defer os.RemoveAll(tmpDir)
	defer db.Close()

	ts := httptest.NewServer(server.Router())
	defer ts.Close()

	// Seed known addresses
	_ = db.RecordKnownAddress("Kernel Developer", "dev@example.com")
	_ = db.RecordKnownAddress("Reviewer One", "reviewer1@kernel.org")
	_ = db.RecordKnownAddress("Reviewer One", "reviewer1@kernel.org") // freq 2
	_ = db.RecordKnownAddress("", "linux-trace-kernel@vger.kernel.org")

	client := &http.Client{Timeout: 5 * time.Second}

	// 1. Query addresses with filter
	req, _ := http.NewRequest("GET", ts.URL+"/api/addresses?query=reviewer", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("GET /api/addresses failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 OK, got %d", resp.StatusCode)
	}

	var results []struct {
		ID        int64  `json:"id"`
		Name      string `json:"name"`
		Address   string `json:"address"`
		Frequency int    `json:"frequency"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&results); err != nil {
		t.Fatalf("failed to decode addresses: %v", err)
	}

	if len(results) != 1 || results[0].Address != "reviewer1@kernel.org" {
		t.Errorf("expected reviewer1@kernel.org, got %+v", results)
	}
	if results[0].Frequency != 2 {
		t.Errorf("expected frequency=2, got %d", results[0].Frequency)
	}

	// 2. Query all addresses (no query param)
	reqAll, _ := http.NewRequest("GET", ts.URL+"/api/addresses?limit=10", nil)
	reqAll.Header.Set("Authorization", "Bearer "+token)
	respAll, err := client.Do(reqAll)
	if err != nil {
		t.Fatalf("GET /api/addresses (all) failed: %v", err)
	}
	defer respAll.Body.Close()

	var allResults []struct {
		Address   string `json:"address"`
		Frequency int    `json:"frequency"`
	}
	_ = json.NewDecoder(respAll.Body).Decode(&allResults)
	if len(allResults) != 3 {
		t.Errorf("expected 3 addresses, got %d", len(allResults))
	}
	if allResults[0].Address != "reviewer1@kernel.org" {
		t.Errorf("expected highest frequency first, got %s", allResults[0].Address)
	}

	// 3. POST /api/addresses/collect
	reqCollect, _ := http.NewRequest("POST", ts.URL+"/api/addresses/collect", nil)
	reqCollect.Header.Set("Authorization", "Bearer "+token)
	respCollect, err := client.Do(reqCollect)
	if err != nil {
		t.Fatalf("POST /api/addresses/collect failed: %v", err)
	}
	defer respCollect.Body.Close()

	if respCollect.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 OK for collect, got %d", respCollect.StatusCode)
	}

	var collectRes map[string]any
	_ = json.NewDecoder(respCollect.Body).Decode(&collectRes)
	if collectRes["success"] != true {
		t.Errorf("expected success=true, got %+v", collectRes)
	}
}

func TestGetMessageRawHeader(t *testing.T) {
	server, db, token, tmpDir := setupTestAPIServer(t)
	defer os.RemoveAll(tmpDir)

	ts := httptest.NewServer(server.Router())
	defer ts.Close()

	client := &http.Client{}

	folder, err := db.GetOrCreateFolder("INBOX", true)
	if err != nil {
		t.Fatalf("Failed to create folder: %v", err)
	}

	testEml := []byte("Message-ID: <patch-header-test@kernel.org>\r\n" +
		"From: Kernel Developer <dev@example.com>\r\n" +
		"To: maintainer@kernel.org\r\n" +
		"Cc: list@kernel.org\r\n" +
		"Subject: [PATCH] tracing: Fix header viewer\r\n" +
		"Date: Sat, 17 Oct 2026 12:00:00 +0000\r\n\r\n" +
		"This is body text that should not appear in raw headers.\r\n")

	emlPath := filepath.Join(tmpDir, "msg_headers.eml")
	_ = os.WriteFile(emlPath, testEml, 0644)
	msgID, err := db.IndexEmailFile(folder.ID, emlPath)
	if err != nil {
		t.Fatalf("Failed to index message: %v", err)
	}

	// 1. GET /api/messages/{id}/raw-header (JSON default)
	reqJSON, _ := http.NewRequest("GET", fmt.Sprintf("%s/api/messages/%d/raw-header", ts.URL, msgID), nil)
	reqJSON.Header.Set("Authorization", "Bearer "+token)
	respJSON, err := client.Do(reqJSON)
	if err != nil {
		t.Fatalf("GET /api/messages/{id}/raw-header failed: %v", err)
	}
	defer respJSON.Body.Close()

	if respJSON.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 OK, got %d", respJSON.StatusCode)
	}
	contentType := respJSON.Header.Get("Content-Type")
	if !strings.Contains(contentType, "application/json") {
		t.Errorf("expected application/json Content-Type, got %s", contentType)
	}

	var jsonRes struct {
		MessageID  string `json:"message_id"`
		RawHeaders string `json:"raw_headers"`
	}
	if err := json.NewDecoder(respJSON.Body).Decode(&jsonRes); err != nil {
		t.Fatalf("failed to decode JSON response: %v", err)
	}

	if jsonRes.MessageID != "<patch-header-test@kernel.org>" {
		t.Errorf("expected <patch-header-test@kernel.org>, got %s", jsonRes.MessageID)
	}
	if !strings.Contains(jsonRes.RawHeaders, "Subject: [PATCH] tracing: Fix header viewer") {
		t.Errorf("raw headers missing Subject line: %s", jsonRes.RawHeaders)
	}
	if !strings.Contains(jsonRes.RawHeaders, "From: Kernel Developer <dev@example.com>") {
		t.Errorf("raw headers missing From line: %s", jsonRes.RawHeaders)
	}
	if strings.Contains(jsonRes.RawHeaders, "This is body text") {
		t.Errorf("raw headers leaked body content: %s", jsonRes.RawHeaders)
	}

	// 2. Query by Message-ID (without angle brackets)
	reqMsgID, _ := http.NewRequest("GET", fmt.Sprintf("%s/api/messages/patch-header-test@kernel.org/raw-header", ts.URL), nil)
	reqMsgID.Header.Set("Authorization", "Bearer "+token)
	respMsgID, err := client.Do(reqMsgID)
	if err != nil {
		t.Fatalf("GET by msgid failed: %v", err)
	}
	defer respMsgID.Body.Close()
	if respMsgID.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 OK for msgid lookup, got %d", respMsgID.StatusCode)
	}

	// 3. GET /api/messages/{id}/raw-header with Accept: text/plain
	reqPlain, _ := http.NewRequest("GET", fmt.Sprintf("%s/api/messages/%d/raw-header", ts.URL, msgID), nil)
	reqPlain.Header.Set("Authorization", "Bearer "+token)
	reqPlain.Header.Set("Accept", "text/plain")
	respPlain, err := client.Do(reqPlain)
	if err != nil {
		t.Fatalf("GET with Accept: text/plain failed: %v", err)
	}
	defer respPlain.Body.Close()

	if respPlain.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 OK, got %d", respPlain.StatusCode)
	}
	plainContentType := respPlain.Header.Get("Content-Type")
	if !strings.Contains(plainContentType, "text/plain") {
		t.Errorf("expected text/plain Content-Type, got %s", plainContentType)
	}
	plainBytes, _ := io.ReadAll(respPlain.Body)
	if !strings.Contains(string(plainBytes), "Subject: [PATCH] tracing: Fix header viewer") {
		t.Errorf("plain text missing Subject line: %s", string(plainBytes))
	}

	// 4. Non-existent message returns 404
	req404, _ := http.NewRequest("GET", fmt.Sprintf("%s/api/messages/nonexistent-99999/raw-header", ts.URL), nil)
	req404.Header.Set("Authorization", "Bearer "+token)
	resp404, err := client.Do(req404)
	if err != nil {
		t.Fatalf("GET 404 failed: %v", err)
	}
	defer resp404.Body.Close()
	if resp404.StatusCode != http.StatusNotFound {
		t.Errorf("expected 404 Not Found, got %d", resp404.StatusCode)
	}
}

func TestPatchworkInfoEndpoint(t *testing.T) {
	mockPatchwork := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if strings.HasPrefix(r.URL.Path, "/api/1.4/patches/") {
			msgid := r.URL.Query().Get("msgid")
			if msgid == "patchwork-test@kernel.org" || msgid == "<patchwork-test@kernel.org>" {
				w.Write([]byte(`[{
					"id": 12345,
					"name": "[PATCH] tracing: Fix test",
					"msgid": "<patchwork-test@kernel.org>",
					"state": "under-review",
					"check": "success",
					"delegate": {"username": "dev"},
					"project": {"link_name": "linux-trace-kernel"}
				}]`))
				return
			}
		}
		w.Write([]byte(`[]`))
	}))
	defer mockPatchwork.Close()

	server, db, token, tmpDir := setupTestAPIServer(t)
	defer os.RemoveAll(tmpDir)
	defer db.Close()

	server.Config.Patchwork.URL = mockPatchwork.URL
	server.Config.Patchwork.APIVersion = "1.4"
	server.Config.Patchwork.Username = "dev"
	server.Config.Patchwork.Email = "dev@example.com"
	server.Config.Patchwork.Token = "token"
	server.Patchwork = patchwork.NewClient(server.Config, db, server.Maildir)

	ts := httptest.NewServer(server.Router())
	defer ts.Close()

	client := &http.Client{}

	// 1. Successful lookup
	req, _ := http.NewRequest("GET", ts.URL+"/api/patchwork/info?msgid=patchwork-test@kernel.org", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("GET /api/patchwork/info failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 OK, got %d", resp.StatusCode)
	}

	var res map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&res); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}

	if res["found"] != true {
		t.Errorf("expected found=true, got %v", res["found"])
	}
	if res["enabled"] != true {
		t.Errorf("expected enabled=true, got %v", res["enabled"])
	}
	if res["is_delegated_to_me"] != true {
		t.Errorf("expected is_delegated_to_me=true, got %v", res["is_delegated_to_me"])
	}

	// 2. Not found lookup
	reqNF, _ := http.NewRequest("GET", ts.URL+"/api/patchwork/info?msgid=notfound@kernel.org", nil)
	reqNF.Header.Set("Authorization", "Bearer "+token)
	respNF, err := client.Do(reqNF)
	if err != nil {
		t.Fatalf("GET not found failed: %v", err)
	}
	defer respNF.Body.Close()

	if respNF.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 OK for not found, got %d", respNF.StatusCode)
	}

	var resNF map[string]any
	_ = json.NewDecoder(respNF.Body).Decode(&resNF)
	if resNF["found"] != false {
		t.Errorf("expected found=false for not found patch, got %v", resNF["found"])
	}
}

func TestAttachmentsEndpoint(t *testing.T) {
	server, db, token, tmpDir := setupTestAPIServer(t)
	defer os.RemoveAll(tmpDir)
	defer db.Close()

	ts := httptest.NewServer(server.Router())
	defer ts.Close()

	client := ts.Client()

	// 1. Create a folder and save a message with attachments
	folder, err := db.GetOrCreateFolder("INBOX", true)
	if err != nil {
		t.Fatalf("Failed to create folder: %v", err)
	}

	rawEmail := `From: "Kernel Developer" <dev@example.com>
To: linux-trace-kernel@vger.kernel.org
Subject: [PATCH] trace: sample with attachments
Date: Fri, 24 Oct 2026 12:00:00 +0000
Message-ID: <attach-test-1234@example.com>
MIME-Version: 1.0
Content-Type: multipart/mixed; boundary="ATTACH_BOUND_XYZ"

--ATTACH_BOUND_XYZ
Content-Type: text/plain; charset=utf-8

Please inspect the attached patch and report.

--ATTACH_BOUND_XYZ
Content-Type: text/x-diff; name="0001-fix.patch"
Content-Disposition: attachment; filename="0001-fix.patch"
Content-Transfer-Encoding: base64

ZGlmZiAtLWdpdCBhL2tlcm5lbC90cmFjZS5jIGIvcmVwb3J0LmMKLS0tIGEvZmlsZQorKysg
Yi9maWxlCg==

--ATTACH_BOUND_XYZ
Content-Type: text/plain; name="log.txt"
Content-Disposition: attachment; filename="log.txt"
Content-Transfer-Encoding: quoted-printable

Test log content output=0A

--ATTACH_BOUND_XYZ--
`

	mailPath := filepath.Join(tmpDir, "Mail")
	store := maildir.NewMaildirStore(mailPath)
	filePath, err := store.AddMessage(folder.Name, []byte(rawEmail), "")
	if err != nil {
		t.Fatalf("Failed to save message: %v", err)
	}

	msgID, err := db.IndexEmailFile(folder.ID, filePath)
	if err != nil {
		t.Fatalf("Failed to index message: %v", err)
	}

	// 2. Fetch message detail and verify attachments metadata
	reqDetail, _ := http.NewRequest("GET", fmt.Sprintf("%s/api/messages/%d", ts.URL, msgID), nil)
	reqDetail.Header.Set("Authorization", "Bearer "+token)
	respDetail, err := client.Do(reqDetail)
	if err != nil {
		t.Fatalf("GET /api/messages/:id failed: %v", err)
	}
	defer respDetail.Body.Close()

	if respDetail.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 OK, got %d", respDetail.StatusCode)
	}

	var detailRes map[string]any
	if err := json.NewDecoder(respDetail.Body).Decode(&detailRes); err != nil {
		t.Fatalf("failed to decode detail JSON: %v", err)
	}

	rawAtts, ok := detailRes["attachments"].([]any)
	if !ok || len(rawAtts) != 2 {
		t.Fatalf("expected 2 attachments in detail response, got: %v", detailRes["attachments"])
	}

	firstAtt := rawAtts[0].(map[string]any)
	if firstAtt["filename"] != "0001-fix.patch" {
		t.Errorf("expected filename 0001-fix.patch, got %v", firstAtt["filename"])
	}
	if firstAtt["content_type"] != "text/x-diff" {
		t.Errorf("expected content_type text/x-diff, got %v", firstAtt["content_type"])
	}

	// 3. Download attachment 0 (Base64 patch)
	reqDL0, _ := http.NewRequest("GET", fmt.Sprintf("%s/api/messages/%d/attachments/0", ts.URL, msgID), nil)
	reqDL0.Header.Set("Authorization", "Bearer "+token)
	respDL0, err := client.Do(reqDL0)
	if err != nil {
		t.Fatalf("GET attachment 0 failed: %v", err)
	}
	defer respDL0.Body.Close()

	if respDL0.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 OK, got %d", respDL0.StatusCode)
	}

	dl0Bytes, _ := io.ReadAll(respDL0.Body)
	if !strings.Contains(string(dl0Bytes), "diff --git a/kernel/trace.c") {
		t.Errorf("expected decoded patch in downloaded body, got: %s", string(dl0Bytes))
	}
	if !strings.Contains(respDL0.Header.Get("Content-Disposition"), "0001-fix.patch") {
		t.Errorf("unexpected Content-Disposition: %s", respDL0.Header.Get("Content-Disposition"))
	}
	if respDL0.Header.Get("X-Content-Type-Options") != "nosniff" {
		t.Errorf("missing nosniff header")
	}

	// 4. Download attachment 1 with friendly filename path URL
	reqDL1, _ := http.NewRequest("GET", fmt.Sprintf("%s/api/messages/%d/attachments/1/log.txt", ts.URL, msgID), nil)
	reqDL1.Header.Set("Authorization", "Bearer "+token)
	respDL1, err := client.Do(reqDL1)
	if err != nil {
		t.Fatalf("GET attachment 1 failed: %v", err)
	}
	defer respDL1.Body.Close()

	if respDL1.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 OK, got %d", respDL1.StatusCode)
	}
	dl1Bytes, _ := io.ReadAll(respDL1.Body)
	if !strings.Contains(string(dl1Bytes), "Test log content output") {
		t.Errorf("expected decoded log content, got: %s", string(dl1Bytes))
	}

	// 5. Out of bounds index
	reqOOB, _ := http.NewRequest("GET", fmt.Sprintf("%s/api/messages/%d/attachments/99", ts.URL, msgID), nil)
	reqOOB.Header.Set("Authorization", "Bearer "+token)
	respOOB, err := client.Do(reqOOB)
	if err != nil {
		t.Fatalf("GET attachment 99 failed: %v", err)
	}
	defer respOOB.Body.Close()
	if respOOB.StatusCode != http.StatusNotFound {
		t.Errorf("expected 404 for out of bounds attachment, got %d", respOOB.StatusCode)
	}

	// 6. Invalid non-integer index
	reqBad, _ := http.NewRequest("GET", fmt.Sprintf("%s/api/messages/%d/attachments/invalid", ts.URL, msgID), nil)
	reqBad.Header.Set("Authorization", "Bearer "+token)
	respBad, err := client.Do(reqBad)
	if err != nil {
		t.Fatalf("GET invalid attachment index failed: %v", err)
	}
	defer respBad.Body.Close()
	if respBad.StatusCode != http.StatusBadRequest {
		t.Errorf("expected 400 for bad index, got %d", respBad.StatusCode)
	}
}



