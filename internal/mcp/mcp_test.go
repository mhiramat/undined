package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"undined-go/internal/config"
	"undined-go/internal/constants"
	"undined-go/internal/lkml/b4"
	"undined-go/internal/patchwork"
	"undined-go/internal/storage/database"
	"undined-go/internal/storage/maildir"
)

var sampleEmail = []byte(`From: Kernel Developer <dev@kernel.org>
To: linux-kernel@vger.kernel.org
Subject: [PATCH] ftrace: improve kprobe handling
Message-ID: <ftrace-patch-1@kernel.org>
Date: Sat, 03 Oct 2026 14:00:00 +0000

diff --git a/kernel/trace/ftrace.c b/kernel/trace/ftrace.c
--- a/kernel/trace/ftrace.c
+++ b/kernel/trace/ftrace.c
@@ -50,3 +50,3 @@ static void ftrace_test(void)
-	old_code();
+	new_code();
 }
`)

func setupTestMCPEnv(t *testing.T) (*Server, *database.Database, *maildir.MaildirStore, *config.Config, int64, *bytes.Buffer) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test.db")
	maildirPath := filepath.Join(tmpDir, "maildir")

	cfg := config.NewDefaultConfig()
	cfg.General.DBPath = dbPath
	cfg.General.MaildirPath = maildirPath
	cfg.General.LogFile = filepath.Join(tmpDir, "undined.log")
	cfg.SMTP.FromName = "Dev"
	cfg.SMTP.FromEmail = "dev@kernel.org"

	db, err := database.NewDatabase(dbPath)
	if err != nil {
		t.Fatalf("Failed to create test database: %v", err)
	}

	store := maildir.NewMaildirStore(maildirPath)
	b4Folder, err := db.GetOrCreateFolder(constants.FolderB4Imports, false)
	if err != nil {
		t.Fatalf("Failed to create folder: %v", err)
	}

	savedPath, err := store.AddMessage(constants.FolderB4Imports, sampleEmail, "")
	if err != nil {
		t.Fatalf("Failed to save email: %v", err)
	}

	msgID, err := db.IndexEmailFile(b4Folder.ID, savedPath)
	if err != nil {
		t.Fatalf("Failed to index email: %v", err)
	}

	b4Runner := b4.NewB4Runner(cfg, store, db)
	server := NewServer(cfg, store, db, b4Runner, nil)

	logBuf := &bytes.Buffer{}
	server.SetLogWriter(logBuf)

	return server, db, store, cfg, msgID, logBuf
}

func TestMCPToolsCore(t *testing.T) {
	server, db, _, _, _, _ := setupTestMCPEnv(t)
	ctx := context.Background()

	// 1. list_recent_emails
	res, err := server.CallTool(ctx, "list_recent_emails", map[string]interface{}{"folder": "b4-imports"})
	if err != nil {
		t.Fatalf("list_recent_emails failed: %v", err)
	}
	emails, ok := res.([]map[string]interface{})
	if !ok || len(emails) != 1 {
		t.Fatalf("expected 1 email, got %v", res)
	}
	if emails[0]["message_id"] != "ftrace-patch-1@kernel.org" {
		t.Errorf("expected message_id ftrace-patch-1@kernel.org, got %v", emails[0]["message_id"])
	}
	if emails[0]["is_patch"] != true {
		t.Errorf("expected is_patch true, got %v", emails[0]["is_patch"])
	}

	// 2. search_emails
	searchRes, err := server.CallTool(ctx, "search_emails", map[string]interface{}{"query": "kprobe"})
	if err != nil {
		t.Fatalf("search_emails failed: %v", err)
	}
	matches, ok := searchRes.([]*database.SearchResult)
	if !ok || len(matches) < 1 {
		t.Fatalf("expected at least 1 match, got %v", searchRes)
	}
	if !strings.Contains(strings.ToLower(matches[0].Subject), "kprobe") {
		t.Errorf("expected subject to contain kprobe, got %s", matches[0].Subject)
	}

	// 3. get_thread_context
	ctxRes, err := server.CallTool(ctx, "get_thread_context", map[string]interface{}{"message_id": "ftrace-patch-1@kernel.org"})
	if err != nil {
		t.Fatalf("get_thread_context failed: %v", err)
	}
	threads, ok := ctxRes.([]map[string]interface{})
	if !ok || len(threads) != 1 {
		t.Fatalf("expected 1 thread message, got %v", ctxRes)
	}
	if !strings.Contains(threads[0]["body_plain"].(string), "new_code") {
		t.Errorf("expected body to contain new_code, got %v", threads[0]["body_plain"])
	}

	// 4. create_review_draft
	draftRes, err := server.CallTool(ctx, "create_review_draft", map[string]interface{}{
		"target_message_id": "ftrace-patch-1@kernel.org",
		"general_feedback":  "Looks clean to me.",
		"inline_notes": []interface{}{
			map[string]interface{}{
				"file":    "kernel/trace/ftrace.c",
				"line_no": 50,
				"comment": "Nice refactoring here.",
			},
		},
	})
	if err != nil {
		t.Fatalf("create_review_draft failed: %v", err)
	}
	draftMap, ok := draftRes.(map[string]interface{})
	if !ok || draftMap["success"] != true {
		t.Fatalf("expected draft success, got %v", draftRes)
	}
	draftID := int64(draftMap["draft_id"].(int64))
	draftMsg, err := db.GetMessageByID(draftID)
	if err != nil || draftMsg == nil {
		t.Fatalf("failed to retrieve draft message %d: %v", draftID, err)
	}
	if !strings.Contains(draftMsg.BodyPlain, "Looks clean to me.") {
		t.Errorf("expected draft to contain general feedback, got %s", draftMsg.BodyPlain)
	}
	if !strings.Contains(draftMsg.BodyPlain, "Nice refactoring here.") {
		t.Errorf("expected draft to contain inline note, got %s", draftMsg.BodyPlain)
	}
}

func TestMCPPostReviewCommentMapped(t *testing.T) {
	server, db, _, _, msgID, _ := setupTestMCPEnv(t)
	ctx := context.Background()

	res, err := server.CallTool(ctx, "post_review_comment", map[string]interface{}{
		"message_id": "ftrace-patch-1@kernel.org",
		"target":     "kernel/trace/ftrace.c:50",
		"comment":    "Check new_code() error return handling.",
		"side":       "new",
	})
	if err != nil {
		t.Fatalf("post_review_comment failed: %v", err)
	}
	resMap, ok := res.(map[string]interface{})
	if !ok || resMap["success"] != true {
		t.Fatalf("expected success, got %v", res)
	}
	if resMap["mapped"] != true {
		t.Errorf("expected mapped=true, got %v", resMap["mapped"])
	}
	if resMap["file"] != "kernel/trace/ftrace.c" {
		t.Errorf("expected file kernel/trace/ftrace.c, got %v", resMap["file"])
	}
	if resMap["line_no"] != 50 {
		t.Errorf("expected line_no 50, got %v", resMap["line_no"])
	}

	// Verify DB persistence
	comments, err := db.GetMessageComments("ftrace-patch-1@kernel.org")
	if err != nil || len(comments) != 1 {
		t.Fatalf("expected 1 comment in DB, got %v (err=%v)", len(comments), err)
	}
	c := comments[0]
	if c.FilePath != "kernel/trace/ftrace.c" || c.LineNo != 50 || c.Side != "new" || c.Text != "Check new_code() error return handling." {
		t.Errorf("unexpected comment in DB: %+v", c)
	}
	if c.State != "draft" {
		t.Errorf("expected comment state to be draft, got %q", c.State)
	}

	// Post comment containing [review] prefix - must also be saved in 'draft' state
	_, err = server.CallTool(ctx, "post_review_comment", map[string]interface{}{
		"message_id": "ftrace-patch-1@kernel.org",
		"target":     "kernel/trace/ftrace.c:51",
		"comment":    "[review] Please add the appropriate Fixes tag.",
		"side":       "new",
	})
	if err != nil {
		t.Fatalf("post_review_comment with [review] prefix failed: %v", err)
	}
	comments2, _ := db.GetMessageComments("ftrace-patch-1@kernel.org")
	if len(comments2) != 2 {
		t.Fatalf("expected 2 comments in DB, got %d", len(comments2))
	}
	for _, cmt := range comments2 {
		if cmt.State != "draft" {
			t.Errorf("expected comment state 'draft', got %q for %+v", cmt.State, cmt)
		}
	}

	// Verify has_notes set to 1
	msg, err := db.GetMessageByID(msgID)
	if err != nil || !msg.HasNotes {
		t.Errorf("expected has_notes true on message, got %v", msg)
	}

	// Verify comments can be discarded
	discarded, err := db.DiscardDraftComments("ftrace-patch-1@kernel.org")
	if err != nil || discarded != 2 {
		t.Fatalf("expected 2 discarded comments, got %d (err=%v)", discarded, err)
	}

	// Verify has_notes reset to 0 after discard
	msgAfter, _ := db.GetMessageByID(msgID)
	if msgAfter != nil && msgAfter.HasNotes {
		t.Errorf("expected has_notes false after discard, got %v", msgAfter)
	}
}

func TestMCPPostDiffCommentAlias(t *testing.T) {
	server, db, _, _, _, _ := setupTestMCPEnv(t)
	ctx := context.Background()

	// 1. post_diff_comment
	res1, err := server.CallTool(ctx, "post_diff_comment", map[string]interface{}{
		"message_id": "ftrace-patch-1@kernel.org",
		"target":     "kernel/trace/ftrace.c:50",
		"comment":    "Verify removal of old_code().",
		"side":       "old",
	})
	if err != nil {
		t.Fatalf("post_diff_comment failed: %v", err)
	}
	res1Map := res1.(map[string]interface{})
	if res1Map["success"] != true || res1Map["side"] != "old" {
		t.Errorf("unexpected res1: %v", res1)
	}

	// 2. add_diff_comment with target_message_id
	res2, err := server.CallTool(ctx, "add_diff_comment", map[string]interface{}{
		"target_message_id": "ftrace-patch-1@kernel.org",
		"target":            "kernel/trace/ftrace.c:51",
		"comment":           "Context note via target_message_id parameter.",
		"side":              "new",
	})
	if err != nil {
		t.Fatalf("add_diff_comment failed: %v", err)
	}
	res2Map := res2.(map[string]interface{})
	if res2Map["success"] != true {
		t.Errorf("unexpected res2: %v", res2)
	}

	comments, _ := db.GetMessageComments("ftrace-patch-1@kernel.org")
	if len(comments) != 2 {
		t.Errorf("expected 2 comments in DB, got %d", len(comments))
	}
}

func TestMCPPostReviewCommentDescription(t *testing.T) {
	server, db, store, _, _, _ := setupTestMCPEnv(t)
	ctx := context.Background()

	descEmail := []byte(`From: Kernel Developer <dev@kernel.org>
To: linux-kernel@vger.kernel.org
Subject: [PATCH] ftrace: update description
Message-ID: <desc-email-1@kernel.org>
Date: Sat, 03 Oct 2026 14:00:00 +0000

This is line 1 of the patch description.
This is line 2 of the patch description.
This is line 3 of the patch description.

diff --git a/kernel/trace/ftrace.c b/kernel/trace/ftrace.c
--- a/kernel/trace/ftrace.c
+++ b/kernel/trace/ftrace.c
@@ -50,3 +50,3 @@ static void ftrace_test(void)
-	old_code();
+	new_code();
 }
`)
	b4Folder, _ := db.GetOrCreateFolder(constants.FolderB4Imports, false)
	savedPath, err := store.AddMessage(constants.FolderB4Imports, descEmail, "")
	if err != nil {
		t.Fatalf("Failed to save email: %v", err)
	}
	_, err = db.IndexEmailFile(b4Folder.ID, savedPath)
	if err != nil {
		t.Fatalf("Failed to index email: %v", err)
	}

	// 1. Post comment on DESCRIPTION:2 (within bounds -> mapped: true)
	res, err := server.CallTool(ctx, "post_review_comment", map[string]interface{}{
		"message_id": "desc-email-1@kernel.org",
		"target":     "DESCRIPTION:2",
		"comment":    "Clarify line 2 in description.",
	})
	if err != nil {
		t.Fatalf("post_review_comment failed: %v", err)
	}
	resMap, ok := res.(map[string]interface{})
	if !ok || resMap["success"] != true {
		t.Fatalf("expected success, got %v", res)
	}
	if resMap["file"] != "DESCRIPTION" {
		t.Errorf("expected file DESCRIPTION, got %v", resMap["file"])
	}
	if resMap["line_no"] != 2 {
		t.Errorf("expected line_no 2, got %v", resMap["line_no"])
	}
	if resMap["mapped"] != true {
		t.Errorf("expected mapped=true for valid description line, got %v", resMap["mapped"])
	}

	// 2. Post comment with lowercase "description:1" (case insensitivity)
	resLower, err := server.CallTool(ctx, "post_review_comment", map[string]interface{}{
		"message_id": "desc-email-1@kernel.org",
		"target":     "description:1",
		"comment":    "Good title summary.",
	})
	if err != nil {
		t.Fatalf("post_review_comment failed: %v", err)
	}
	resLowerMap := resLower.(map[string]interface{})
	if resLowerMap["file"] != "DESCRIPTION" || resLowerMap["mapped"] != true {
		t.Errorf("expected canonical DESCRIPTION and mapped=true, got %v", resLower)
	}

	// 3. Post comment out-of-bounds on DESCRIPTION:99 (mapped: false)
	resOOB, err := server.CallTool(ctx, "post_review_comment", map[string]interface{}{
		"message_id": "desc-email-1@kernel.org",
		"target":     "DESCRIPTION:99",
		"comment":    "Line out of range.",
	})
	if err != nil {
		t.Fatalf("post_review_comment failed: %v", err)
	}
	resOOBMap := resOOB.(map[string]interface{})
	if resOOBMap["file"] != "DESCRIPTION" || resOOBMap["mapped"] != false {
		t.Errorf("expected mapped=false for line out of range, got %v", resOOB)
	}

	// 4. Verify DB comments
	comments, err := db.GetMessageComments("desc-email-1@kernel.org")
	if err != nil || len(comments) != 3 {
		t.Fatalf("expected 3 comments, got %d (err=%v)", len(comments), err)
	}

	// 5. Create review draft containing DESCRIPTION inline note
	draftRes, err := server.CallTool(ctx, "create_review_draft", map[string]interface{}{
		"target_message_id": "desc-email-1@kernel.org",
		"general_feedback":  "Patch looks good, small remarks on description.",
		"inline_notes": []interface{}{
			map[string]interface{}{
				"file":    "DESCRIPTION",
				"line_no": 2,
				"comment": "Line 2 needs more detail.",
			},
		},
	})
	if err != nil {
		t.Fatalf("create_review_draft failed: %v", err)
	}
	draftMap := draftRes.(map[string]interface{})
	draftID := int64(draftMap["draft_id"].(int64))
	draftMsg, err := db.GetMessageByID(draftID)
	if err != nil || draftMsg == nil {
		t.Fatalf("failed to retrieve draft message: %v", err)
	}
	if !strings.Contains(draftMsg.BodyPlain, "> This is line 2 of the patch description.") {
		t.Errorf("expected draft to quote line 2 of description, got %s", draftMsg.BodyPlain)
	}
	if !strings.Contains(draftMsg.BodyPlain, "Line 2 needs more detail.") {
		t.Errorf("expected draft to contain comment under line 2, got %s", draftMsg.BodyPlain)
	}
}

func TestMCPTargetNormalization(t *testing.T) {
	server, db, _, _, _, _ := setupTestMCPEnv(t)
	ctx := context.Background()

	// Target with b/ prefix
	resB, err := server.CallTool(ctx, "post_review_comment", map[string]interface{}{
		"message_id": "ftrace-patch-1@kernel.org",
		"target":     "b/kernel/trace/ftrace.c:50",
		"comment":    "Normalized b/ target prefix.",
		"side":       "new",
	})
	if err != nil || resB.(map[string]interface{})["file"] != "kernel/trace/ftrace.c" {
		t.Errorf("b/ normalization failed: %v", resB)
	}

	// Target with a/ prefix
	resA, err := server.CallTool(ctx, "post_review_comment", map[string]interface{}{
		"message_id": "ftrace-patch-1@kernel.org",
		"target":     "a/kernel/trace/ftrace.c:50",
		"comment":    "Normalized a/ target prefix.",
		"side":       "old",
	})
	if err != nil || resA.(map[string]interface{})["file"] != "kernel/trace/ftrace.c" {
		t.Errorf("a/ normalization failed: %v", resA)
	}

	comments, _ := db.GetMessageComments("ftrace-patch-1@kernel.org")
	for _, c := range comments {
		if strings.HasPrefix(c.FilePath, "a/") || strings.HasPrefix(c.FilePath, "b/") {
			t.Errorf("comment file path retains prefix: %s", c.FilePath)
		}
	}
}

func TestMCPUnmappedLinesOutsideHunks(t *testing.T) {
	server, db, _, _, msgID, _ := setupTestMCPEnv(t)
	ctx := context.Background()

	res, err := server.CallTool(ctx, "post_review_comment", map[string]interface{}{
		"message_id": "ftrace-patch-1@kernel.org",
		"target":     "kernel/trace/ftrace.c:999",
		"comment":    "Feedback on line outside hunk boundary.",
		"side":       "new",
	})
	if err != nil {
		t.Fatalf("post_review_comment failed: %v", err)
	}
	resMap := res.(map[string]interface{})
	if resMap["success"] != true {
		t.Fatalf("expected success, got %v", res)
	}
	if resMap["mapped"] != false {
		t.Errorf("expected mapped=false, got %v", resMap["mapped"])
	}

	// Comment must still be persisted
	comments, _ := db.GetMessageComments("ftrace-patch-1@kernel.org")
	if len(comments) != 1 || comments[0].LineNo != 999 {
		t.Errorf("expected 1 comment at line 999, got %v", comments)
	}

	// Flag must be updated
	msg, _ := db.GetMessageByID(msgID)
	if !msg.HasNotes {
		t.Errorf("expected has_notes true")
	}
}

func TestMCPMalformedTargetsAndMissingMessages(t *testing.T) {
	server, db, _, _, _, _ := setupTestMCPEnv(t)
	ctx := context.Background()

	malformedTargets := []string{
		"kernel/trace/ftrace.c",
		"kernel/trace/ftrace.c:abc",
		"",
		":50",
		"kernel/trace/ftrace.c:0",
		"kernel/trace/ftrace.c:-10",
	}

	for _, target := range malformedTargets {
		res, err := server.CallTool(ctx, "post_review_comment", map[string]interface{}{
			"message_id": "ftrace-patch-1@kernel.org",
			"target":     target,
			"comment":    "Should not be persisted",
		})
		if err != nil {
			t.Fatalf("unexpected call error: %v", err)
		}
		resMap := res.(map[string]interface{})
		if resMap["success"] != false || resMap["error"] == "" {
			t.Errorf("expected failure for target %q, got %v", target, res)
		}
	}

	// Non-existent message
	resNon, _ := server.CallTool(ctx, "post_review_comment", map[string]interface{}{
		"message_id": "nonexistent@kernel.org",
		"target":     "kernel/trace/ftrace.c:50",
		"comment":    "Non-existent message",
	})
	resNonMap := resNon.(map[string]interface{})
	if resNonMap["success"] != false {
		t.Errorf("expected failure for nonexistent message, got %v", resNon)
	}

	// Empty message ID
	resEmpty, _ := server.CallTool(ctx, "post_review_comment", map[string]interface{}{
		"message_id": "",
		"target":     "kernel/trace/ftrace.c:50",
		"comment":    "Empty ID",
	})
	resEmptyMap := resEmpty.(map[string]interface{})
	if resEmptyMap["success"] != false {
		t.Errorf("expected failure for empty message ID, got %v", resEmpty)
	}

	// No comments persisted
	comments, _ := db.GetMessageComments("ftrace-patch-1@kernel.org")
	if len(comments) != 0 {
		t.Errorf("expected 0 comments, got %d", len(comments))
	}
}

func TestMCPAccessLogging(t *testing.T) {
	server, _, _, cfg, _, logBuf := setupTestMCPEnv(t)
	ctx := context.Background()

	_, _ = server.CallTool(ctx, "list_recent_emails", map[string]interface{}{"folder": "b4-imports"})
	_, _ = server.CallTool(ctx, "get_thread_context", map[string]interface{}{"message_id": "ftrace-patch-1@kernel.org"})
	_, _ = server.CallTool(ctx, "post_review_comment", map[string]interface{}{
		"message_id": "ftrace-patch-1@kernel.org",
		"target":     "kernel/trace/ftrace.c:50",
		"comment":    "Audit test comment",
	})
	_, _ = server.CallTool(ctx, "post_review_comment", map[string]interface{}{
		"message_id": "ftrace-patch-1@kernel.org",
		"target":     "bad_target_no_colon",
		"comment":    "Should fail",
	})

	logText := logBuf.String()
	if !strings.Contains(logText, "MCP access: list_recent_emails") {
		t.Errorf("expected list_recent_emails in access log, got:\n%s", logText)
	}
	if !strings.Contains(logText, "MCP access: get_thread_context") {
		t.Errorf("expected get_thread_context in access log, got:\n%s", logText)
	}
	if !strings.Contains(logText, "MCP access: post_review_comment") {
		t.Errorf("expected post_review_comment in access log, got:\n%s", logText)
	}
	if !strings.Contains(logText, "REJECTED (Invalid target format)") {
		t.Errorf("expected REJECTED (Invalid target format) in access log, got:\n%s", logText)
	}

	// Verify file logging
	fileBytes, err := os.ReadFile(cfg.General.LogFile)
	if err != nil || !strings.Contains(string(fileBytes), "MCP access:") {
		t.Errorf("expected file log to contain MCP access: %v", err)
	}
}

func TestMCPSTDIOJSONRPC(t *testing.T) {
	server, _, _, _, _, _ := setupTestMCPEnv(t)

	var inBuf bytes.Buffer
	var outBuf bytes.Buffer

	// 1. initialize
	inBuf.WriteString(`{"jsonrpc": "2.0", "id": 1, "method": "initialize", "params": {"protocolVersion": "2024-11-05"}}` + "\n")
	// 2. notifications/initialized (notification - no response expected)
	inBuf.WriteString(`{"jsonrpc": "2.0", "method": "notifications/initialized"}` + "\n")
	// 3. ping
	inBuf.WriteString(`{"jsonrpc": "2.0", "id": 2, "method": "ping"}` + "\n")
	// 4. tools/list
	inBuf.WriteString(`{"jsonrpc": "2.0", "id": 3, "method": "tools/list"}` + "\n")
	// 5. tools/call
	inBuf.WriteString(`{"jsonrpc": "2.0", "id": 4, "method": "tools/call", "params": {"name": "list_recent_emails", "arguments": {"limit": 5}}}` + "\n")
	// 6. invalid method
	inBuf.WriteString(`{"jsonrpc": "2.0", "id": 5, "method": "unknown_method"}` + "\n")

	err := server.HandleSTDIO(&inBuf, &outBuf)
	if err != nil {
		t.Fatalf("HandleSTDIO error: %v", err)
	}

	lines := strings.Split(strings.TrimSpace(outBuf.String()), "\n")
	if len(lines) != 5 {
		t.Fatalf("expected 5 response lines (1 init, 1 ping, 1 tools/list, 1 tools/call, 1 error), got %d:\n%s", len(lines), outBuf.String())
	}

	// Verify initialize response
	var initResp map[string]interface{}
	if err := json.Unmarshal([]byte(lines[0]), &initResp); err != nil {
		t.Fatalf("failed to parse initResp: %v", err)
	}
	resultMap := initResp["result"].(map[string]interface{})
	serverInfo := resultMap["serverInfo"].(map[string]interface{})
	if serverInfo["name"] != "undined" {
		t.Errorf("expected server name undined, got %v", serverInfo["name"])
	}

	// Verify ping
	var pingResp map[string]interface{}
	_ = json.Unmarshal([]byte(lines[1]), &pingResp)
	if pingResp["id"].(float64) != 2 {
		t.Errorf("expected ping id 2, got %v", pingResp["id"])
	}

	// Verify tools/list
	var toolsResp map[string]interface{}
	_ = json.Unmarshal([]byte(lines[2]), &toolsResp)
	toolsList := toolsResp["result"].(map[string]interface{})["tools"].([]interface{})
	if len(toolsList) != 14 {
		t.Errorf("expected 14 tools, got %d", len(toolsList))
	}

	// Verify tools/call
	var callResp map[string]interface{}
	_ = json.Unmarshal([]byte(lines[3]), &callResp)
	callResult := callResp["result"].(map[string]interface{})
	if callResult["isError"] != false {
		t.Errorf("expected isError=false, got %v", callResult["isError"])
	}
	content := callResult["content"].([]interface{})
	if len(content) == 0 {
		t.Errorf("expected non-empty content in call response")
	}

	// Verify unknown method error
	var errResp map[string]interface{}
	_ = json.Unmarshal([]byte(lines[4]), &errResp)
	if errResp["error"] == nil {
		t.Errorf("expected error response for unknown method")
	}
}

func TestMCPPatchworkAndB4Tools(t *testing.T) {
	mockPW := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.Contains(r.URL.Path, "/patches/"):
			if r.Method == "PATCH" {
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write([]byte(`{"id": 123, "state": "under-review"}`))
				return
			}
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`[{"id": 123, "state": "new", "msgid": "ftrace-patch-1@kernel.org"}]`))
		case strings.Contains(r.URL.Path, "/users/"):
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`[{"id": 99, "username": "dev"}]`))
		default:
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`[]`))
		}
	}))
	defer mockPW.Close()

	server, db, store, cfg, _, _ := setupTestMCPEnv(t)
	cfg.Patchwork.URL = mockPW.URL
	cfg.Patchwork.APIVersion = "1.4"
	cfg.Patchwork.Token = "mock-token"
	server.Patchwork = patchwork.NewClient(cfg, db, store)

	ctx := context.Background()

	// 1. get_patchwork_patch_info
	pwInfo, err := server.CallTool(ctx, "get_patchwork_patch_info", map[string]interface{}{"message_id": "ftrace-patch-1@kernel.org"})
	if err != nil {
		t.Fatalf("get_patchwork_patch_info failed: %v", err)
	}
	pwMap, ok := pwInfo.(map[string]any)
	if !ok || pwMap["id"] == nil {
		t.Fatalf("expected patch info with id, got %v", pwInfo)
	}

	// 2. update_patchwork_patch
	pwUpdate, err := server.CallTool(ctx, "update_patchwork_patch", map[string]interface{}{
		"patch_id": 123,
		"state":    "under-review",
	})
	if err != nil {
		t.Fatalf("update_patchwork_patch failed: %v", err)
	}
	updateMap, ok := pwUpdate.(map[string]any)
	if !ok || updateMap["state"] != "under-review" {
		t.Errorf("expected state under-review, got %v", pwUpdate)
	}

	// 3. list_my_delegated_patches
	listPatches, err := server.CallTool(ctx, "list_my_delegated_patches", map[string]interface{}{
		"limit": 10,
	})
	if err != nil {
		t.Fatalf("list_my_delegated_patches failed: %v", err)
	}
	if listPatches == nil {
		t.Errorf("expected slice result from list_my_delegated_patches")
	}

	// 4. import_patch_series_b4 with empty/failing b4
	b4Res, err := server.CallTool(ctx, "import_patch_series_b4", map[string]interface{}{
		"message_id_or_url": "nonexistent-b4-url",
	})
	if err != nil {
		t.Fatalf("import_patch_series_b4 returned unexpected fatal error: %v", err)
	}
	b4Map, ok := b4Res.(map[string]interface{})
	if !ok {
		t.Fatalf("expected map result from import_patch_series_b4, got %v", b4Res)
	}
	if b4Map["message_id"] != "nonexistent-b4-url" {
		t.Errorf("expected message_id reflected, got %v", b4Map)
	}
}

func TestMCPWriteDraftMessage(t *testing.T) {
	server, db, _, _, _, _ := setupTestMCPEnv(t)
	ctx := context.Background()

	// 1. Write draft as a reply to an existing patch (auto-deriving To, Cc, Subject)
	replyRes, err := server.CallTool(ctx, "write_draft_message", map[string]interface{}{
		"in_reply_to": "ftrace-patch-1@kernel.org",
		"body":        "Looks good to me.\n\nReviewed-by: Kernel Developer <dev@example.com>",
	})
	if err != nil {
		t.Fatalf("write_draft_message failed: %v", err)
	}
	replyMap, ok := replyRes.(map[string]interface{})
	if !ok || replyMap["success"] != true {
		t.Fatalf("expected write_draft_message success, got %v", replyRes)
	}
	draftID := int64(replyMap["draft_id"].(int64))
	draftMsg, err := db.GetMessageByID(draftID)
	if err != nil || draftMsg == nil {
		t.Fatalf("failed to retrieve draft message %d: %v", draftID, err)
	}
	if !strings.HasPrefix(draftMsg.Subject, "Re: ") {
		t.Errorf("expected subject prefixed with 'Re: ', got %s", draftMsg.Subject)
	}
	if !strings.Contains(draftMsg.BodyPlain, "Reviewed-by: Kernel Developer") {
		t.Errorf("expected draft body to contain review tag, got %s", draftMsg.BodyPlain)
	}
	if !strings.Contains(draftMsg.InReplyTo, "ftrace-patch-1@kernel.org") {
		t.Errorf("expected in_reply_to to contain parent msgid, got %s", draftMsg.InReplyTo)
	}

	// 2. Write standalone new draft with explicit To and Subject
	newDraftRes, err := server.CallTool(ctx, "save_draft", map[string]interface{}{
		"to":      "linux-trace-kernel@vger.kernel.org",
		"subject": "[RFC] tracing: Proposal for new probe feature",
		"body":    "Hi all,\n\nHere is a proposal for discussion.",
	})
	if err != nil {
		t.Fatalf("save_draft failed: %v", err)
	}
	newMap, ok := newDraftRes.(map[string]interface{})
	if !ok || newMap["success"] != true {
		t.Fatalf("expected save_draft success, got %v", newDraftRes)
	}
	newDraftID := int64(newMap["draft_id"].(int64))
	newMsg, err := db.GetMessageByID(newDraftID)
	if err != nil || newMsg == nil {
		t.Fatalf("failed to retrieve new draft %d: %v", newDraftID, err)
	}
	if newMsg.Subject != "[RFC] tracing: Proposal for new probe feature" {
		t.Errorf("expected exact subject, got %s", newMsg.Subject)
	}
	if newMsg.ToAddr != "linux-trace-kernel@vger.kernel.org" {
		t.Errorf("expected exact to_addr, got %s", newMsg.ToAddr)
	}

	// 3. Write draft via write_draft alias
	aliasRes, err := server.CallTool(ctx, "write_draft", map[string]interface{}{
		"to":      "dev@example.org",
		"subject": "Ping on patch status",
		"body":    "Any update on this?",
	})
	if err != nil {
		t.Fatalf("write_draft alias failed: %v", err)
	}
	aliasMap, ok := aliasRes.(map[string]interface{})
	if !ok || aliasMap["success"] != true {
		t.Fatalf("expected write_draft success, got %v", aliasRes)
	}
}

func TestMCPWriteDraftValidation(t *testing.T) {
	server, _, _, _, _, _ := setupTestMCPEnv(t)
	ctx := context.Background()

	// 1. Missing body
	emptyRes, err := server.CallTool(ctx, "write_draft_message", map[string]interface{}{
		"to":      "dev@example.com",
		"subject": "Empty body test",
		"body":    "",
	})
	if err != nil {
		t.Fatalf("unexpected fatal error: %v", err)
	}
	emptyMap := emptyRes.(map[string]interface{})
	if emptyMap["success"] == true {
		t.Errorf("expected failure on empty body, got %v", emptyMap)
	}

	// 2. Nonexistent parent message when to/subject omitted
	missingRes, err := server.CallTool(ctx, "write_draft_message", map[string]interface{}{
		"in_reply_to": "nonexistent-message-id@example.com",
		"body":        "Reply to nothing",
	})
	if err != nil {
		t.Fatalf("unexpected fatal error: %v", err)
	}
	missingMap := missingRes.(map[string]interface{})
	if missingMap["success"] == true {
		t.Errorf("expected failure on nonexistent parent message, got %v", missingMap)
	}
}

