package api

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

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
		"From: Masami Hiramatsu <mhiramat@kernel.org>\n" +
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
	cfg.SMTP.FromName = "Masami Hiramatsu"
	cfg.SMTP.FromEmail = "mhiramat@kernel.org"
	cfg.Patchwork.Enabled = true
	cfg.Patchwork.DelegateUsers = []string{"mhiramat", "rostedt"}
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
	if configRes["from_name"] != "Masami Hiramatsu" || configRes["from_email"] != "mhiramat@kernel.org" {
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

