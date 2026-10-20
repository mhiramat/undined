package patchwork

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"undined-go/internal/config"
	"undined-go/internal/constants"
	"undined-go/internal/storage/database"
	"undined-go/internal/storage/maildir"
)

func TestUpdatePatchWithExplicitNullDelegate(t *testing.T) {
	// Rule 4: Verify that unassigning a delegate sends {"delegate": null} in JSON payload
	var receivedBody map[string]any
	var receivedMethod string
	var receivedAuth string

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedMethod = r.Method
		receivedAuth = r.Header.Get("Authorization")

		bodyBytes, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(bodyBytes, &receivedBody)

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"id": 42, "state": "under-review", "delegate": null, "msgid": "test-patch@kernel.org"}`))
	}))
	defer server.Close()

	cfg := config.NewDefaultConfig()
	cfg.Patchwork.URL = server.URL
	cfg.Patchwork.APIVersion = "1.4"
	cfg.Patchwork.Token = "secret-token-123"

	client := NewClient(cfg, nil, nil)

	// Explicitly unset delegate by passing nil
	updates := map[string]any{
		"state":    "under-review",
		"delegate": nil,
	}

	updated, err := client.UpdatePatch(42, updates)
	if err != nil {
		t.Fatalf("UpdatePatch failed: %v", err)
	}

	if receivedMethod != "PATCH" {
		t.Errorf("Expected PATCH method, got %s", receivedMethod)
	}
	if receivedAuth != "Token secret-token-123" {
		t.Errorf("Expected Token secret-token-123, got %s", receivedAuth)
	}

	// Verify key 'delegate' was sent in payload and its value is nil (null in JSON)
	val, keyExists := receivedBody["delegate"]
	if !keyExists {
		t.Errorf("Rule 4 violation: 'delegate' key was omitted instead of sent as null")
	}
	if val != nil {
		t.Errorf("Expected delegate to be nil/null, got %v", val)
	}

	if updated["id"].(float64) != 42 {
		t.Errorf("Unexpected updated response: %v", updated)
	}
}

func TestChoosePreferredItem(t *testing.T) {
	cfg := config.NewDefaultConfig()
	cfg.Patchwork.PreferredArchives = []string{"linux-trace-kernel", "netdev"}
	cfg.Patchwork.Username = "dev"
	cfg.Patchwork.Email = "dev@example.com"

	client := NewClient(cfg, nil, nil)

	items := []map[string]any{
		{
			"id":       101,
			"project":  map[string]any{"link_name": "netdev"},
			"delegate": map[string]any{"username": "other_user"},
		},
		{
			"id":       102,
			"project":  map[string]any{"link_name": "linux-trace-kernel"},
			"delegate": map[string]any{"username": "dev"},
		},
		{
			"id":       103,
			"project":  map[string]any{"link_name": "lkml"},
			"delegate": nil,
		},
	}

	chosen := client.ChoosePreferredItem(items, "dev@example.com")
	if chosen == nil || chosen["id"].(int) != 102 {
		t.Errorf("Expected item 102 (preferred archive + delegated to me), got %v", chosen)
	}
}

func TestSyncTodoFromPatchwork(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "undined-todo-test-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	dbPath := filepath.Join(tmpDir, "test.db")
	db, err := database.NewDatabase(dbPath)
	if err != nil {
		t.Fatalf("Failed to init database: %v", err)
	}
	defer db.Close()

	mailPath := filepath.Join(tmpDir, "Mail")
	store := maildir.NewMaildirStore(mailPath)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if strings.HasPrefix(r.URL.Path, "/api/1.4/users/") {
			w.Write([]byte(`[{"id": 99, "username": "dev", "email": "dev@example.com"}]`))
			return
		}
		if strings.HasPrefix(r.URL.Path, "/api/1.4/patches/777/mbox/") {
			w.Header().Set("Content-Type", "text/plain")
			w.Write([]byte("From: dev@kernel.org\nSubject: [PATCH] tracing: Fix probe\nMessage-ID: <patch-777@kernel.org>\nDate: Sat, 17 Oct 2026 12:00:00 +0000\n\nFix it\n"))
			return
		}
		if strings.HasPrefix(r.URL.Path, "/api/1.4/patches/") {
			w.Write([]byte(`[{"id": 777, "name": "[PATCH] tracing: Fix probe", "msgid": "patch-777@kernel.org", "state": "new"}]`))
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer server.Close()

	cfg := config.NewDefaultConfig()
	cfg.Patchwork.URL = server.URL
	cfg.Patchwork.APIVersion = "1.4"
	cfg.Patchwork.Username = "dev"
	cfg.Patchwork.Email = "dev@example.com"
	cfg.Patchwork.Token = "test-token"

	client := NewClient(cfg, db, store)

	res, err := client.SyncTodoFromPatchwork([]string{"new"})
	if err != nil {
		t.Fatalf("SyncTodoFromPatchwork failed: %v", err)
	}

	if res.SyncedCount != 1 {
		t.Errorf("Expected 1 synced patch, got %d", res.SyncedCount)
	}

	todoFolder, err := db.GetFolderByName(constants.FolderTODO)
	if err != nil || todoFolder == nil {
		t.Fatalf("TODO folder not found in database: %v", err)
	}

	msgs, err := db.ListMessages(&todoFolder.ID, 10, 0)
	if err != nil || len(msgs) == 0 {
		t.Fatalf("Expected message in TODO folder, got %d msgs", len(msgs))
	}

	if msgs[0].MessageID != "patch-777@kernel.org" {
		t.Errorf("Expected message_id 'patch-777@kernel.org', got %s", msgs[0].MessageID)
	}
}

func TestGetPatchInfoEnrichmentAndCache(t *testing.T) {
	callCount := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount++
		w.Header().Set("Content-Type", "application/json")
		if strings.HasPrefix(r.URL.Path, "/api/1.4/patches/") {
			msgid := r.URL.Query().Get("msgid")
			if msgid == "test-msgid@kernel.org" || msgid == "<test-msgid@kernel.org>" {
				w.Write([]byte(`[{
					"id": 555,
					"name": "[PATCH] tracing: Fix test",
					"msgid": "<test-msgid@kernel.org>",
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
	defer server.Close()

	cfg := config.NewDefaultConfig()
	cfg.Patchwork.URL = server.URL
	cfg.Patchwork.APIVersion = "1.4"
	cfg.Patchwork.Username = "dev"
	cfg.Patchwork.Email = "dev@example.com"
	cfg.Patchwork.Token = "test-token"

	client := NewClient(cfg, nil, nil)

	// 1. Fetch info and verify enrichment
	info, err := client.GetPatchInfo("test-msgid@kernel.org", "")
	if err != nil {
		t.Fatalf("GetPatchInfo failed: %v", err)
	}
	if info == nil {
		t.Fatalf("expected patch info, got nil")
	}
	if info["found"] != true {
		t.Errorf("expected found=true, got %v", info["found"])
	}
	if info["enabled"] != true {
		t.Errorf("expected enabled=true, got %v", info["enabled"])
	}
	if info["is_delegated_to_me"] != true {
		t.Errorf("expected is_delegated_to_me=true, got %v", info["is_delegated_to_me"])
	}
	if info["can_update"] != true {
		t.Errorf("expected can_update=true, got %v", info["can_update"])
	}
	if info["type"] != "patch" {
		t.Errorf("expected type=patch, got %v", info["type"])
	}

	// 2. Cache hit: callCount should remain unchanged
	prevCalls := callCount
	info2, err := client.GetPatchInfo("test-msgid@kernel.org", "")
	if err != nil || info2 == nil {
		t.Fatalf("GetPatchInfo cached failed: %v", err)
	}
	if callCount != prevCalls {
		t.Errorf("expected cache hit with 0 additional calls, got callCount=%d vs %d", callCount, prevCalls)
	}

	// 3. ClearCache: next query should invoke upstream
	client.ClearCache("test-msgid@kernel.org")
	_, _ = client.GetPatchInfo("test-msgid@kernel.org", "")
	if callCount <= prevCalls {
		t.Errorf("expected upstream call after ClearCache, got callCount=%d", callCount)
	}

	// 4. Non-existent patch returns nil, nil
	notfound, err := client.GetPatchInfo("does-not-exist@kernel.org", "")
	if err != nil {
		t.Fatalf("expected nil err for not found, got %v", err)
	}
	if notfound != nil {
		t.Errorf("expected nil for not found, got %+v", notfound)
	}
}
