package e2e

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"undined-go/internal/mcp"
	"undined-go/test/e2e/mocks"
)

// Scenario S1 & S2: Ingestion, Full-Text Search, and Threading
func TestE2E_IngestionSearchAndThreading(t *testing.T) {
	env := setupE2EEnv(t)

	// 1. Seed Mock IMAP server with two patch emails
	uid1 := env.IMAPServer.AddMessage("INBOX", sampleKernelPatch1)
	uid2 := env.IMAPServer.AddMessage("INBOX", sampleKernelPatch2)

	inboxFolder, err := env.DB.GetOrCreateFolder("INBOX", true)
	if err != nil {
		t.Fatalf("failed to get INBOX folder: %v", err)
	}

	// 2. Hydrate messages from IMAP into Maildir & SQLite
	msg1, err := env.Poller.FetchAndHydrateMessage("INBOX", inboxFolder.ID, uid1, 0)
	if err != nil {
		t.Fatalf("FetchAndHydrateMessage 1 failed: %v", err)
	}
	msg2, err := env.Poller.FetchAndHydrateMessage("INBOX", inboxFolder.ID, uid2, 0)
	if err != nil {
		t.Fatalf("FetchAndHydrateMessage 2 failed: %v", err)
	}

	if msg1.Subject != "[PATCH v2 1/2] trace/kprobes: Add support for custom event probes" {
		t.Errorf("unexpected subject 1: %s", msg1.Subject)
	}
	if msg2.InReplyTo != "<patch-e2e-1@kernel.org>" {
		t.Errorf("unexpected in-reply-to 2: %s", msg2.InReplyTo)
	}

	// 3. FTS5 Search via API: GET /api/search?q=probes
	resp, err := env.Request("GET", "/api/search?q=probes", "")
	if err != nil {
		t.Fatalf("search request failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 OK from search, got %d", resp.StatusCode)
	}

	var searchRes []map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&searchRes); err != nil {
		t.Fatalf("failed to decode search response: %v", err)
	}

	if len(searchRes) < 2 {
		t.Fatalf("expected at least 2 search results for 'kprobes', got: %+v", searchRes)
	}

	// 4. Threading: GET /api/messages?folder_id=...&threaded=true
	resp, err = env.Request("GET", fmt.Sprintf("/api/messages?folder_id=%d&threaded=true", inboxFolder.ID), "")
	if err != nil {
		t.Fatalf("threaded messages request failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 OK from threaded messages, got %d", resp.StatusCode)
	}

	var threads []map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&threads); err != nil {
		t.Fatalf("failed to decode threads response: %v", err)
	}

	if len(threads) != 2 {
		t.Fatalf("expected 2 threaded messages, got %d", len(threads))
	}

	root := threads[0]
	child := threads[1]

	if root["message_id"] != "patch-e2e-1@kernel.org" {
		t.Errorf("expected root message_id patch-e2e-1@kernel.org, got %v", root["message_id"])
	}
	if depth, ok := root["thread_depth"].(float64); !ok || int(depth) != 0 {
		t.Errorf("expected root depth 0, got %v", root["thread_depth"])
	}
	if hasChildren, ok := root["has_children"].(bool); !ok || !hasChildren {
		t.Errorf("expected root has_children=true, got %v", root["has_children"])
	}

	if child["message_id"] != "patch-e2e-2@kernel.org" {
		t.Errorf("expected child message_id patch-e2e-2@kernel.org, got %v", child["message_id"])
	}
	if depth, ok := child["thread_depth"].(float64); !ok || int(depth) != 1 {
		t.Errorf("expected child depth 1, got %v", child["thread_depth"])
	}
}

// Scenario S3 & S4: Patchwork Metadata Enrichment, Delegation, and TODO Sync
func TestE2E_PatchworkEnrichmentAndTodoSync(t *testing.T) {
	env := setupE2EEnv(t)

	// 1. Seed mock Patchwork server
	env.Patchwork.AddPatch(&mocks.MockPatchworkPatch{
		ID:    101,
		MsgID: "patch-e2e-1@kernel.org",
		Name:  "[PATCH v2 1/2] trace/kprobes: Add support for custom event probes",
		State: "under-review",
		Delegate: map[string]any{
			"id":       1,
			"username": "dev",
		},
		Mbox: sampleKernelPatch1,
	})

	// 2. Query Patchwork Info via API: GET /api/patchwork/info?msgid=patch-e2e-1@kernel.org
	resp, err := env.Request("GET", "/api/patchwork/info?msgid=patch-e2e-1@kernel.org", "")
	if err != nil {
		t.Fatalf("patchwork info request failed: %v", err)
	}
	defer resp.Body.Close()

	var pwInfo map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&pwInfo)

	if pwInfo["found"] != true {
		t.Fatalf("expected patch to be found in patchwork, got %+v", pwInfo)
	}
	if pwInfo["is_delegated_to_me"] != true {
		t.Fatalf("expected is_delegated_to_me=true, got %+v", pwInfo)
	}

	delUsers, ok := pwInfo["delegate_users"].([]any)
	if !ok || len(delUsers) != 3 {
		t.Errorf("expected 3 configured delegate users, got %+v", pwInfo["delegate_users"])
	}

	// 3. Synchronize TODO mailbox from Patchwork: POST /api/patchwork/sync-todo
	resp, err = env.Request("POST", "/api/patchwork/sync-todo", `{"states": ["under-review"]}`)
	if err != nil {
		t.Fatalf("sync-todo request failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 OK from sync-todo, got %d", resp.StatusCode)
	}

	var syncRes map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&syncRes)
	if synced, ok := syncRes["synced_count"].(float64); !ok || int(synced) != 1 {
		t.Fatalf("expected 1 patch synced to TODO, got %+v", syncRes)
	}

	// 4. Update Patch Delegate to 'maintainer2' from delegate-users: PATCH /api/patchwork/patches/101
	updatePayload := `{"state": "accepted", "delegate": "maintainer2"}`
	resp, err = env.Request("PATCH", "/api/patchwork/patches/101", updatePayload)
	if err != nil {
		t.Fatalf("update patch request failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 OK from patch update, got %d", resp.StatusCode)
	}

	var updatedPatch map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&updatedPatch)
	if updatedPatch["state"] != "accepted" {
		t.Errorf("expected state=accepted, got %v", updatedPatch["state"])
	}

	delMap, _ := updatedPatch["delegate"].(map[string]any)
	if delMap["username"] != "maintainer2" {
		t.Errorf("expected delegate username=maintainer2, got %+v", delMap)
	}

	// 5. Query Patchwork Info again: verify is_delegated_to_me is now false
	resp, err = env.Request("GET", "/api/patchwork/info?msgid=patch-e2e-1@kernel.org&refresh=true", "")
	if err != nil {
		t.Fatalf("refresh patchwork info failed: %v", err)
	}
	defer resp.Body.Close()

	var refreshedInfo map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&refreshedInfo)
	if refreshedInfo["is_delegated_to_me"] != false {
		t.Errorf("expected is_delegated_to_me=false after delegating to maintainer2, got %+v", refreshedInfo)
	}
}

// Scenario S5: SideDiff, Unified Diff Hunks, and Inline Comments
func TestE2E_DiffCommentsAndSideDiff(t *testing.T) {
	env := setupE2EEnv(t)

	inboxFolder, _ := env.DB.GetOrCreateFolder("INBOX", true)
	uid := env.IMAPServer.AddMessage("INBOX", sampleKernelPatch1)
	msg, err := env.Poller.FetchAndHydrateMessage("INBOX", inboxFolder.ID, uid, 0)
	if err != nil {
		t.Fatalf("hydrate failed: %v", err)
	}

	// 1. GET /api/messages/{id} to verify parsed diff hunks and paired SideDiff rows
	resp, err := env.Request("GET", fmt.Sprintf("/api/messages/%d", msg.ID), "")
	if err != nil {
		t.Fatalf("get message detail failed: %v", err)
	}
	defer resp.Body.Close()

	var detailRes map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&detailRes)

	parsedDiff, ok := detailRes["parsed_diff"].(map[string]any)
	if !ok || parsedDiff == nil {
		t.Fatalf("expected parsed_diff in message detail, got %+v", detailRes)
	}
	files, ok := parsedDiff["files"].([]any)
	if !ok || len(files) == 0 {
		t.Fatalf("expected files in parsed diff, got %+v", parsedDiff)
	}
	fileObj := files[0].(map[string]any)
	hunks, ok := fileObj["hunks"].([]any)
	if !ok || len(hunks) == 0 {
		t.Fatalf("expected hunks in parsed diff file, got %+v", fileObj)
	}
	firstHunk := hunks[0].(map[string]any)
	pairedRows, ok := firstHunk["paired_rows"].([]any)
	if !ok || len(pairedRows) == 0 {
		t.Fatalf("expected paired_rows for SideDiff rendering, got %+v", firstHunk)
	}

	// 2. POST /api/messages/{id}/comments to add line-specific comment
	commentPayload := `{"file_path": "kernel/trace/trace_kprobe.c", "line_no": 102, "side": "right", "text": "Check for lock coverage."}`
	resp, err = env.Request("POST", fmt.Sprintf("/api/messages/%d/comments", msg.ID), commentPayload)
	if err != nil {
		t.Fatalf("post comment failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 OK from post comment, got %d", resp.StatusCode)
	}

	// 3. GET /api/messages/{id}/comments to read comments back
	resp, err = env.Request("GET", fmt.Sprintf("/api/messages/%d/comments", msg.ID), "")
	if err != nil {
		t.Fatalf("get comments failed: %v", err)
	}
	defer resp.Body.Close()

	var commentsRes map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&commentsRes)

	commentsList, ok := commentsRes["comments"].([]any)
	if !ok || len(commentsList) != 1 {
		t.Fatalf("expected 1 comment, got %+v", commentsRes)
	}
	firstComment := commentsList[0].(map[string]any)
	if firstComment["text"] != "Check for lock coverage." {
		t.Errorf("unexpected comment text: %v", firstComment["text"])
	}
	if lineNum, ok := firstComment["line_no"].(float64); !ok || int(lineNum) != 102 {
		t.Errorf("unexpected comment line number: %v", firstComment["line_no"])
	}
}

// Scenario S6: Review Quoting and SMTP Dispatch
func TestE2E_ReviewQuotingAndSMTPSending(t *testing.T) {
	env := setupE2EEnv(t)

	inboxFolder, _ := env.DB.GetOrCreateFolder("INBOX", true)
	uid := env.IMAPServer.AddMessage("INBOX", sampleKernelPatch1)
	_, err := env.Poller.FetchAndHydrateMessage("INBOX", inboxFolder.ID, uid, 0)
	if err != nil {
		t.Fatalf("hydrate failed: %v", err)
	}

	// 1. Preview quote: POST /api/composer/preview-quote
	quoteReq := `{"message_id": "<patch-e2e-1@kernel.org>"}`
	resp, err := env.Request("POST", "/api/composer/preview-quote", quoteReq)
	if err != nil {
		t.Fatalf("preview quote failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 OK from preview quote, got %d", resp.StatusCode)
	}

	var quoteRes map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&quoteRes)

	quoteBody, _ := quoteRes["quote"].(string)
	if !strings.Contains(quoteBody, "> Signed-off-by: Kernel Developer <dev@example.com>") {
		t.Fatalf("expected full RFC quote with > prefixes, got:\n%s", quoteBody)
	}

	// 2. Dispatch review response: POST /api/smtp/send
	sendPayload := map[string]any{
		"to":           "linux-trace@vger.kernel.org",
		"subject":      "Re: [PATCH v2 1/2] trace/kprobes: Add support for custom event probes",
		"body":         "Looks good to me.\n\nAcked-by: Kernel Developer <dev@example.com>\n",
		"in_reply_to":  "<patch-e2e-1@kernel.org>",
		"references":   "<patch-e2e-1@kernel.org>",
	}
	sendBytes, _ := json.Marshal(sendPayload)

	resp, err = env.Request("POST", "/api/smtp/send", string(sendBytes))
	if err != nil {
		t.Fatalf("send message failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 OK from send message, got %d", resp.StatusCode)
	}

	// 3. Verify Mock SMTP Server captured the message with correct headers
	captured := env.SMTPServer.GetCapturedEmails()
	if len(captured) != 1 {
		t.Fatalf("expected 1 captured email in mock SMTP, got %d", len(captured))
	}

	email := captured[0]
	if email.From != "dev@example.com" {
		t.Errorf("expected From: dev@example.com, got %q", email.From)
	}
	if len(email.To) == 0 || email.To[0] != "linux-trace@vger.kernel.org" {
		t.Errorf("expected To: linux-trace@vger.kernel.org, got %+v", email.To)
	}
	if email.InReplyTo != "<patch-e2e-1@kernel.org>" {
		t.Errorf("expected In-Reply-To: <patch-e2e-1@kernel.org>, got %q", email.InReplyTo)
	}
	if email.References != "<patch-e2e-1@kernel.org>" {
		t.Errorf("expected References: <patch-e2e-1@kernel.org>, got %q", email.References)
	}
	if !strings.Contains(email.Body, "Acked-by: Kernel Developer <dev@example.com>") {
		t.Errorf("expected Acked-by in email body, got:\n%s", email.Body)
	}

	// 4. Verify message saved to local Sent folder
	sentFolder, err := env.DB.GetOrCreateFolder("Sent", false)
	if err != nil {
		t.Fatalf("failed to get Sent folder: %v", err)
	}
	sentMsgs, err := env.DB.ListMessages(&sentFolder.ID, 10, 0)
	if err != nil || len(sentMsgs) != 1 {
		t.Fatalf("expected 1 message in Sent folder, got %d (err: %v)", len(sentMsgs), err)
	}
}

// Scenario S7: b4 Series Apply
func TestE2E_B4SeriesApply(t *testing.T) {
	env := setupE2EEnv(t)

	applyPayload := fmt.Sprintf(`{"message_id": "patch-e2e-1@kernel.org", "target_git_repo": %q}`, env.Config.General.KernelGitDir)
	resp, err := env.Request("POST", "/api/patch/apply", applyPayload)
	if err != nil {
		t.Fatalf("b4 apply request failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 OK from /api/patch/apply, got %d", resp.StatusCode)
	}

	var res map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&res)
	if success, ok := res["success"].(bool); !ok || !success {
		t.Fatalf("expected apply success=true, got %+v", res)
	}

	invocations := env.B4Runner.GetInvocations()
	if len(invocations) == 0 {
		t.Fatalf("expected mock b4 to be invoked, got 0 invocations")
	}

	foundAm := false
	for _, inv := range invocations {
		if strings.Contains(inv, "am") && strings.Contains(inv, "patch-e2e-1@kernel.org") {
			foundAm = true
			break
		}
	}
	if !foundAm {
		t.Errorf("expected b4 am invocation for patch-e2e-1@kernel.org, got: %+v", invocations)
	}
}

// Scenario S8: MCP Agent Review Tools
func TestE2E_MCPAgentReview(t *testing.T) {
	env := setupE2EEnv(t)

	inboxFolder, _ := env.DB.GetOrCreateFolder("INBOX", true)
	uid := env.IMAPServer.AddMessage("INBOX", sampleKernelPatch1)
	_, err := env.Poller.FetchAndHydrateMessage("INBOX", inboxFolder.ID, uid, 0)
	if err != nil {
		t.Fatalf("hydrate failed: %v", err)
	}

	mcpServer := mcp.NewServer(env.Config, env.Maildir, env.DB, env.B4Client, env.PWClient)
	logBuf := &bytes.Buffer{}
	mcpServer.SetLogWriter(logBuf)

	var inBuf, outBuf bytes.Buffer

	// 1. initialize
	inBuf.WriteString(`{"jsonrpc": "2.0", "id": 1, "method": "initialize"}` + "\n")
	// 2. tools/list
	inBuf.WriteString(`{"jsonrpc": "2.0", "id": 2, "method": "tools/list"}` + "\n")
	// 3. tools/call: list_recent_emails
	inBuf.WriteString(`{"jsonrpc": "2.0", "id": 3, "method": "tools/call", "params": {"name": "list_recent_emails", "arguments": {"limit": 5}}}` + "\n")
	// 4. tools/call: search_emails
	inBuf.WriteString(`{"jsonrpc": "2.0", "id": 4, "method": "tools/call", "params": {"name": "search_emails", "arguments": {"query": "probes"}}}` + "\n")
	// 5. tools/call: post_review_comment
	inBuf.WriteString(`{"jsonrpc": "2.0", "id": 5, "method": "tools/call", "params": {"name": "post_review_comment", "arguments": {"message_id": "patch-e2e-1@kernel.org", "target": "kernel/trace/trace_kprobe.c:102", "comment": "MCP review comment"}}}` + "\n")

	if err := mcpServer.HandleSTDIO(&inBuf, &outBuf); err != nil {
		t.Fatalf("HandleSTDIO failed: %v", err)
	}

	lines := strings.Split(strings.TrimSpace(outBuf.String()), "\n")
	if len(lines) != 5 {
		t.Fatalf("expected 5 JSON-RPC responses, got %d:\n%s", len(lines), outBuf.String())
	}

	// Verify tools/list
	var toolsResp map[string]any
	_ = json.Unmarshal([]byte(lines[1]), &toolsResp)
	toolsResult := toolsResp["result"].(map[string]any)
	toolsList := toolsResult["tools"].([]any)
	if len(toolsList) != 14 {
		t.Errorf("expected 14 MCP tools, got %d", len(toolsList))
	}

	// Verify list_recent_emails
	var recentResp map[string]any
	_ = json.Unmarshal([]byte(lines[2]), &recentResp)
	recentResult, ok := recentResp["result"].(map[string]any)
	if !ok {
		t.Fatalf("expected result object in recentResp: %+v", recentResp)
	}
	structContent, ok := recentResult["structuredContent"].(map[string]any)
	if !ok {
		t.Fatalf("expected structuredContent in recentResult: %+v", recentResult)
	}
	items, ok := structContent["result"].([]any)
	if !ok || len(items) == 0 {
		t.Errorf("expected at least 1 recent email in structuredContent, got %+v", structContent)
	}

	// Verify post_review_comment saved to database
	comments, err := env.DB.GetMessageComments("patch-e2e-1@kernel.org")
	if err != nil {
		t.Fatalf("failed to retrieve comments: %v", err)
	}
	if len(comments) != 1 || comments[0].Text != "MCP review comment" {
		t.Errorf("expected 1 MCP comment with content, got %+v", comments)
	}

	// Verify MCP access logging
	if !strings.Contains(logBuf.String(), "[undine.mcp]") {
		t.Errorf("expected [undine.mcp] log output, got:\n%s", logBuf.String())
	}
}
