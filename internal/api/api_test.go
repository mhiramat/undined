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

	server := NewServer(cfg, db, store, sec, nil, nil, nil, nil, nil, nil, frontendDir)
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
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("Expected 200 OK for comment, got %d", resp.StatusCode)
	}

	req, _ = http.NewRequest("GET", fmt.Sprintf("%s/api/messages/%d/comments", ts.URL, msgID), nil)
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err = client.Do(req)
	if err != nil {
		t.Fatalf("GET comments failed: %v", err)
	}
	var comments []map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&comments)
	if len(comments) != 1 || comments[0]["text"] != "Check this lock" {
		t.Errorf("Unexpected comments response: %v", comments)
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
