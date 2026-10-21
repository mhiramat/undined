package patchwork

import (
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
	if res.TotalDelegated != 1 {
		t.Errorf("Expected 1 total delegated patch, got %d", res.TotalDelegated)
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

func TestListMyPatchesPagination(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if strings.HasPrefix(r.URL.Path, "/api/1.4/users/") {
			w.Write([]byte(`[{"id": 10, "username": "dev", "email": "dev@example.com"}]`))
			return
		}
		if strings.HasPrefix(r.URL.Path, "/api/1.4/patches/") {
			page := r.URL.Query().Get("page")
			if page == "2" {
				var items []map[string]any
				for i := 101; i <= 110; i++ {
					items = append(items, map[string]any{
						"id":    i,
						"name":  fmt.Sprintf("[PATCH] Test %d", i),
						"msgid": fmt.Sprintf("patch-%d@kernel.org", i),
						"state": "new",
					})
				}
				_ = json.NewEncoder(w).Encode(items)
				return
			}
			// Page 1: returns 100 items
			var items []map[string]any
			for i := 1; i <= 100; i++ {
				items = append(items, map[string]any{
					"id":    i,
					"name":  fmt.Sprintf("[PATCH] Test %d", i),
					"msgid": fmt.Sprintf("patch-%d@kernel.org", i),
					"state": "new",
				})
			}
			_ = json.NewEncoder(w).Encode(items)
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

	client := NewClient(cfg, nil, nil)

	// Fetch all patches (unlimited)
	all, err := client.ListMyPatches([]string{"new"}, "", 0, "dev@example.com")
	if err != nil {
		t.Fatalf("ListMyPatches failed: %v", err)
	}
	if len(all) != 110 {
		t.Errorf("Expected 110 patches across 2 pages, got %d", len(all))
	}

	// Fetch with limit 50
	limited, err := client.ListMyPatches([]string{"new"}, "", 50, "dev@example.com")
	if err != nil {
		t.Fatalf("ListMyPatches with limit failed: %v", err)
	}
	if len(limited) != 50 {
		t.Errorf("Expected 50 patches with limit, got %d", len(limited))
	}
}

func TestDownloadPatchMboxFallback(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/direct-mbox/123" {
			w.Header().Set("Content-Type", "text/plain")
			w.Write([]byte("From: dev@kernel.org\nSubject: Direct Mbox\n\nContent\n"))
			return
		}
		if r.URL.Path == "/patch/456/mbox/" {
			w.Header().Set("Content-Type", "text/plain")
			w.Write([]byte("From: dev@kernel.org\nSubject: Web Mbox\n\nContent\n"))
			return
		}
		if r.URL.Path == "/api/1.4/patches/789/mbox/" {
			w.Header().Set("Content-Type", "text/plain")
			w.Write([]byte("From: dev@kernel.org\nSubject: API Mbox\n\nContent\n"))
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer server.Close()

	cfg := config.NewDefaultConfig()
	cfg.Patchwork.URL = server.URL
	cfg.Patchwork.APIVersion = "1.4"
	client := NewClient(cfg, nil, nil)

	// 1. Direct mboxURL
	data1, err := client.DownloadPatchMbox(123, server.URL+"/direct-mbox/123")
	if err != nil || !strings.Contains(string(data1), "Direct Mbox") {
		t.Errorf("Direct mboxURL download failed: %v, got %s", err, string(data1))
	}

	// 2. Web fallback /patch/{id}/mbox/
	data2, err := client.DownloadPatchMbox(456)
	if err != nil || !strings.Contains(string(data2), "Web Mbox") {
		t.Errorf("Web mbox fallback failed: %v, got %s", err, string(data2))
	}

	// 3. API endpoint fallback /api/1.4/patches/{id}/mbox/
	data3, err := client.DownloadPatchMbox(789)
	if err != nil || !strings.Contains(string(data3), "API Mbox") {
		t.Errorf("API mbox fallback failed: %v, got %s", err, string(data3))
	}
}
