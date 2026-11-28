package mcp

import (
	"context"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"undined-go/internal/constants"
	"undined-go/internal/events"
	"undined-go/internal/storage/database"
)

// startSMTPTrap starts a local TCP listener that counts any incoming connection attempts.
// MCP tools must never dial SMTP, so the counter must remain 0.
func startSMTPTrap(t *testing.T) (host string, port int, connCount *atomic.Int32) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to start SMTP trap listener: %v", err)
	}
	t.Cleanup(func() { _ = ln.Close() })

	var count atomic.Int32
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			count.Add(1)
			_ = c.Close()
		}
	}()

	addr := ln.Addr().(*net.TCPAddr)
	return addr.IP.String(), addr.Port, &count
}

func TestMCPToolsListIncludesSeriesAndExportTools(t *testing.T) {
	server, _, _, _, _, _ := setupTestMCPEnv(t)
	tools := server.getToolsList()
	found := map[string]bool{
		"create_mail_draft":  false,
		"stage_patch_series": false,
		"export_thread_mbox": false,
	}
	for _, tool := range tools {
		if name, ok := tool["name"].(string); ok {
			if _, want := found[name]; want {
				found[name] = true
			}
		}
	}
	for name, ok := range found {
		if !ok {
			t.Errorf("expected tool %q to be listed by tools/list", name)
		}
	}
}

func TestCreateMailDraftStrictPlainTextAndZeroSMTP(t *testing.T) {
	server, db, _, cfg, _, logBuf := setupTestMCPEnv(t)
	smtpHost, smtpPort, smtpConns := startSMTPTrap(t)
	cfg.SMTP.Host = smtpHost
	cfg.SMTP.Port = smtpPort
	cfg.General.Host = "127.0.0.1"
	cfg.General.Port = 8080

	ctx := context.Background()
	bodyWithTabs := "Hi Maintainer,\n\nPlease pull the following changes:\n\n\tgit://git.kernel.org/pub/scm/linux/kernel/git/dev/linux.git tags/probes-v6.14-rc3\n\n\t- Fix kprobe handler tab indentation\n"

	res, err := server.CallTool(ctx, "create_mail_draft", map[string]interface{}{
		"to":          []interface{}{"Maintainer <maintainer@kernel.org>"},
		"cc":          []interface{}{"linux-kernel@vger.kernel.org"},
		"subject":     "[GIT PULL] probes: Fixes for v6.14-rc3",
		"body":        bodyWithTabs,
		"in_reply_to": "<parent-pr@kernel.org>",
		"references":  []interface{}{"<root-pr@kernel.org>", "<parent-pr@kernel.org>"},
		"extra_headers": map[string]interface{}{
			"X-Kdevkit-Branch": "probes/fixes-v6.14",
		},
		"tags": []interface{}{"PR-Draft"},
	})
	if err != nil {
		t.Fatalf("create_mail_draft failed: %v", err)
	}

	resMap, ok := res.(map[string]interface{})
	if !ok {
		t.Fatalf("expected map response, got %T", res)
	}
	draftID, _ := resMap["draft_id"].(string)
	msgID, _ := resMap["message_id"].(string)
	maildirPath, _ := resMap["maildir_path"].(string)
	webURL, _ := resMap["web_url"].(string)

	if draftID == "" || !strings.HasPrefix(msgID, "<") || !strings.HasSuffix(msgID, ">") {
		t.Fatalf("unexpected draft_id/message_id: draft_id=%q message_id=%q", draftID, msgID)
	}
	if !strings.HasPrefix(webURL, "http://127.0.0.1:8080/#drafts/") {
		t.Errorf("unexpected web_url: %q", webURL)
	}

	rawBytes, err := os.ReadFile(maildirPath)
	if err != nil {
		t.Fatalf("failed to read Maildir draft %s: %v", maildirPath, err)
	}
	raw := string(rawBytes)
	for _, want := range []string{
		"Content-Type: text/plain; charset=utf-8",
		"Content-Transfer-Encoding: 8bit",
		"X-Mailer: " + constants.DefaultXMailer,
		"X-Kdevkit-Branch: probes/fixes-v6.14",
		"In-Reply-To: <parent-pr@kernel.org>",
		"References: <root-pr@kernel.org> <parent-pr@kernel.org>",
		"\tgit://git.kernel.org/pub/scm/linux/kernel/git/dev/linux.git tags/probes-v6.14-rc3",
		"\t- Fix kprobe handler tab indentation",
	} {
		if !strings.Contains(raw, want) {
			t.Errorf("expected raw draft to contain %q, got:\n%s", want, raw)
		}
	}

	// Verify tag stored in DB flags
	dbID, err := strconv.ParseInt(draftID, 10, 64)
	if err != nil {
		t.Fatalf("invalid numeric draft_id %q: %v", draftID, err)
	}
	savedMsg, err := db.GetMessageByID(dbID)
	if err != nil || savedMsg == nil {
		t.Fatalf("GetMessageByID(%d) failed: %v", dbID, err)
	}
	if !strings.Contains(savedMsg.Flags, "PR-Draft") {
		t.Errorf("expected PR-Draft in flags, got %q", savedMsg.Flags)
	}

	// Verify disallowed extra headers are rejected
	if _, err := server.CallTool(ctx, "create_mail_draft", map[string]interface{}{
		"to":            []interface{}{"maintainer@kernel.org"},
		"subject":       "Test",
		"body":          "Hello",
		"extra_headers": map[string]interface{}{"Bcc": "secret@example.com"},
	}); err == nil {
		t.Errorf("expected disallowed extra header Bcc to fail")
	}

	if _, err := server.CallTool(ctx, "create_mail_draft", map[string]interface{}{
		"to":            []interface{}{"maintainer@kernel.org"},
		"subject":       "Test",
		"body":          "Hello",
		"extra_headers": map[string]interface{}{"X-Undine-Series-ID": "spoof"},
	}); err == nil {
		t.Errorf("expected reserved header X-Undine-Series-ID to fail")
	}

	if smtpConns.Load() != 0 {
		t.Fatalf("SECURITY VIOLATION: SMTP transport was dialed %d times during MCP tool call", smtpConns.Load())
	}
	if !strings.Contains(logBuf.String(), "create_mail_draft") {
		t.Errorf("expected audit log entry for create_mail_draft, got %q", logBuf.String())
	}
}

func TestStagePatchSeriesThreadingReplaceAndZeroSMTP(t *testing.T) {
	server, db, _, cfg, _, _ := setupTestMCPEnv(t)
	smtpHost, smtpPort, smtpConns := startSMTPTrap(t)
	cfg.SMTP.Host = smtpHost
	cfg.SMTP.Port = smtpPort

	hub := events.NewHub()
	server.SetEventHub(hub)

	ctx := context.Background()
	patchBody1 := "Simplify fprobe entry handling.\n\nSigned-off-by: Kernel Developer <dev@kernel.org>\n---\n kernel/trace/fprobe.c | 2 +-\n 1 file changed, 1 insertion(+), 1 deletion(-)\n\ndiff --git a/kernel/trace/fprobe.c b/kernel/trace/fprobe.c\n--- a/kernel/trace/fprobe.c\n+++ b/kernel/trace/fprobe.c\n@@ -10,3 +10,3 @@ static int fprobe_entry(void)\n-\treturn old_fn();\n+\treturn new_fn();\n }\n"
	patchBody2 := "Simplify fprobe exit handling.\n\nSigned-off-by: Kernel Developer <dev@kernel.org>\n---\n kernel/trace/fprobe.c | 2 +-\n 1 file changed, 1 insertion(+), 1 deletion(-)\n\ndiff --git a/kernel/trace/fprobe.c b/kernel/trace/fprobe.c\n--- a/kernel/trace/fprobe.c\n+++ b/kernel/trace/fprobe.c\n@@ -30,3 +30,3 @@ static void fprobe_exit(void)\n-\told_exit();\n+\tnew_exit();\n }\n"

	stageArgs := map[string]interface{}{
		"series_key":  "topic/fprobe-cleanup:v2",
		"branch":      "topic/fprobe-cleanup",
		"version":     2,
		"to":          []interface{}{"linux-kernel@vger.kernel.org"},
		"cc":          []interface{}{"linux-trace-kernel@vger.kernel.org"},
		"in_reply_to": "<bug-report-123@kernel.org>",
		"base_commit": "deadbeefdeadbeefdeadbeefdeadbeefdeadbeef",
		"messages": []interface{}{
			map[string]interface{}{
				"index":           0,
				"is_cover_letter": true,
				"subject":         "[PATCH v2 0/2] fprobe: cleanup entry and exit",
				"body":            "This series cleans up fprobe entry and exit.\n",
			},
			map[string]interface{}{
				"index":      1,
				"subject":    "[PATCH v2 1/2] fprobe: simplify entry",
				"body":       patchBody1,
				"commit_sha": "1111111111111111111111111111111111111111",
				"extra_cc":   []interface{}{"subsys-maintainer@example.com"},
			},
			map[string]interface{}{
				"index":      2,
				"subject":    "[PATCH v2 2/2] fprobe: simplify exit",
				"body":       patchBody2,
				"commit_sha": "2222222222222222222222222222222222222222",
			},
		},
	}

	res, err := server.CallTool(ctx, "stage_patch_series", stageArgs)
	if err != nil {
		t.Fatalf("stage_patch_series failed: %v", err)
	}
	resMap := res.(map[string]interface{})
	seriesID := resMap["series_id"].(string)
	rootMsgID := resMap["root_message_id"].(string)
	if resMap["message_count"].(int) != 3 {
		t.Fatalf("expected message_count=3, got %v", resMap["message_count"])
	}
	if !strings.HasPrefix(resMap["web_url"].(string), "http://127.0.0.1:8080/#series/"+seriesID) {
		t.Errorf("unexpected web_url: %v", resMap["web_url"])
	}

	// Verify SSE event emitted
	evs := hub.EventsSince(0)
	if len(evs) != 1 || evs[0].Type != "series_draft_updated" {
		t.Errorf("expected 1 series_draft_updated SSE event, got %+v", evs)
	}

	// Verify stored DB records and Maildir files
	storedSeries, err := db.GetPatchSeriesDraft(seriesID)
	if err != nil || storedSeries == nil || len(storedSeries.Items) != 3 {
		t.Fatalf("GetPatchSeriesDraft failed: %+v err=%v", storedSeries, err)
	}

	var firstRunFiles []string
	for idx, item := range storedSeries.Items {
		idVal, err := strconv.ParseInt(item.DraftMsgID, 10, 64)
		if err != nil {
			t.Fatalf("invalid numeric DraftMsgID %q on item %d: %v", item.DraftMsgID, idx, err)
		}
		msg, err := db.GetMessageByID(idVal)
		if err != nil || msg == nil {
			t.Fatalf("missing DB message for item %d: %v", idx, err)
		}
		firstRunFiles = append(firstRunFiles, msg.FilePath)
		rawBytes, err := os.ReadFile(msg.FilePath)
		if err != nil {
			t.Fatalf("failed reading Maildir file for item %d: %v", idx, err)
		}
		raw := string(rawBytes)

		if !strings.Contains(raw, "X-Undine-Series-ID: "+seriesID) {
			t.Errorf("item %d missing X-Undine-Series-ID", idx)
		}
		if !strings.Contains(raw, "X-Undine-Series-Total: 3") {
			t.Errorf("item %d missing X-Undine-Series-Total", idx)
		}
		if !strings.Contains(raw, "Content-Transfer-Encoding: 8bit") {
			t.Errorf("item %d missing 8bit transfer encoding", idx)
		}

		if idx == 0 {
			if !strings.Contains(raw, "In-Reply-To: <bug-report-123@kernel.org>") {
				t.Errorf("cover letter missing external In-Reply-To:\n%s", raw)
			}
		} else {
			if !strings.Contains(raw, "In-Reply-To: "+rootMsgID) {
				t.Errorf("patch %d missing In-Reply-To root %s:\n%s", idx, rootMsgID, raw)
			}
			wantRefs := "References: <bug-report-123@kernel.org> " + rootMsgID
			if !strings.Contains(raw, wantRefs) {
				t.Errorf("patch %d missing References %q:\n%s", idx, wantRefs, raw)
			}
		}
		if idx == 1 {
			if !strings.Contains(raw, "subsys-maintainer@example.com") {
				t.Errorf("patch 1 missing extra_cc recipient")
			}
			if !strings.Contains(raw, "-\treturn old_fn();\n+\treturn new_fn();") {
				t.Errorf("patch 1 did not preserve raw tab indentation verbatim:\n%s", raw)
			}
		}
	}

	// Duplicate series_key with replace_existing=false must fail
	if _, err := server.CallTool(ctx, "stage_patch_series", stageArgs); err == nil {
		t.Fatalf("expected error on duplicate series_key without replace_existing=true")
	}

	// Duplicate series_key with replace_existing=true replaces unsent draft cleanly
	stageArgs["replace_existing"] = true
	res2, err := server.CallTool(ctx, "stage_patch_series", stageArgs)
	if err != nil {
		t.Fatalf("stage_patch_series with replace_existing=true failed: %v", err)
	}
	seriesID2 := res2.(map[string]interface{})["series_id"].(string)
	if seriesID2 == seriesID {
		t.Errorf("expected new series_id on replacement")
	}
	for _, oldPath := range firstRunFiles {
		if _, err := os.Stat(oldPath); !os.IsNotExist(err) {
			t.Errorf("expected old Maildir draft %s to be removed on replace", oldPath)
		}
	}

	// Mark item 0 of seriesID2 as sent -> replace_existing=true MUST be refused
	now := time.Now().UTC()
	if err := db.UpdatePatchSeriesItemStatus(seriesID2, 0, database.SeriesItemStatusSent, "", &now); err != nil {
		t.Fatalf("UpdatePatchSeriesItemStatus failed: %v", err)
	}
	if _, err := server.CallTool(ctx, "stage_patch_series", stageArgs); err == nil {
		t.Fatalf("expected stage_patch_series to refuse replacing a series with already-sent messages")
	}

	if smtpConns.Load() != 0 {
		t.Fatalf("SECURITY VIOLATION: SMTP transport was dialed %d times during stage_patch_series", smtpConns.Load())
	}
}

func TestExportThreadMboxOrderingTrailersAndPathSafety(t *testing.T) {
	server, db, store, _, _, _ := setupTestMCPEnv(t)
	b4Folder, err := db.GetOrCreateFolder(constants.FolderB4Imports, false)
	if err != nil {
		t.Fatalf("GetOrCreateFolder failed: %v", err)
	}

	emails := []string{
		// Cover letter 0/2 with base-commit
		`From: Kernel Developer <dev@kernel.org>
To: linux-kernel@vger.kernel.org
Subject: [PATCH 0/2] tracing: demo series
Message-ID: <series-cover@kernel.org>
Date: Sat, 03 Oct 2026 15:00:00 +0000

Cover letter text.

base-commit: abcdef0123456789abcdef0123456789abcdef01
`,
		// Patch 2/2 inserted BEFORE patch 1/2 to verify sorting by [PATCH x/N]
		`From: Kernel Developer <dev@kernel.org>
To: linux-kernel@vger.kernel.org
Subject: [PATCH 2/2] tracing: second change
Message-ID: <series-p2@kernel.org>
In-Reply-To: <series-cover@kernel.org>
References: <series-cover@kernel.org>
Date: Sat, 03 Oct 2026 15:01:00 +0000

Second patch description.

From the commit log line that starts with From.

Signed-off-by: Kernel Developer <dev@kernel.org>
---
 kernel/trace/trace.c | 2 +-
 1 file changed, 1 insertion(+), 1 deletion(-)

diff --git a/kernel/trace/trace.c b/kernel/trace/trace.c
--- a/kernel/trace/trace.c
+++ b/kernel/trace/trace.c
@@ -20,3 +20,3 @@ void trace_two(void)
-	old_two();
+	new_two();
 }
`,
		// Patch 1/2
		`From: Kernel Developer <dev@kernel.org>
To: linux-kernel@vger.kernel.org
Subject: [PATCH 1/2] tracing: first change
Message-ID: <series-p1@kernel.org>
In-Reply-To: <series-cover@kernel.org>
References: <series-cover@kernel.org>
Date: Sat, 03 Oct 2026 15:02:00 +0000

First patch description.

Signed-off-by: Kernel Developer <dev@kernel.org>
---
 kernel/trace/trace.c | 2 +-
 1 file changed, 1 insertion(+), 1 deletion(-)

diff --git a/kernel/trace/trace.c b/kernel/trace/trace.c
--- a/kernel/trace/trace.c
+++ b/kernel/trace/trace.c
@@ -10,3 +10,3 @@ void trace_one(void)
-	old_one();
+	new_one();
 }
`,
		// Review reply to patch 1/2 with Reviewed-by trailer
		`From: Reviewer <reviewer@example.com>
To: Kernel Developer <dev@kernel.org>
Subject: Re: [PATCH 1/2] tracing: first change
Message-ID: <review-p1@example.com>
In-Reply-To: <series-p1@kernel.org>
References: <series-cover@kernel.org> <series-p1@kernel.org>
Date: Sat, 03 Oct 2026 16:00:00 +0000

Looks good to me!

Reviewed-by: Reviewer <reviewer@example.com>
`,
		// Review reply to cover letter with Acked-by (applies to all patches)
		`From: Maintainer <maintainer@kernel.org>
To: Kernel Developer <dev@kernel.org>
Subject: Re: [PATCH 0/2] tracing: demo series
Message-ID: <ack-cover@kernel.org>
In-Reply-To: <series-cover@kernel.org>
References: <series-cover@kernel.org>
Date: Sat, 03 Oct 2026 16:05:00 +0000

Nice cleanup.

Acked-by: Maintainer <maintainer@kernel.org>
`,
	}

	for _, raw := range emails {
		p, err := store.AddMessage(constants.FolderB4Imports, []byte(raw), "")
		if err != nil {
			t.Fatalf("AddMessage failed: %v", err)
		}
		if _, err := db.IndexEmailFile(b4Folder.ID, p); err != nil {
			t.Fatalf("IndexEmailFile failed: %v", err)
		}
	}

	ctx := context.Background()
	res, err := server.CallTool(ctx, "export_thread_mbox", map[string]interface{}{
		"message_id": "<series-p2@kernel.org>",
	})
	if err != nil {
		t.Fatalf("export_thread_mbox failed: %v", err)
	}
	resMap := res.(map[string]interface{})
	mboxPath := resMap["mbox_path"].(string)
	t.Cleanup(func() { _ = os.Remove(mboxPath) })

	if resMap["patch_count"].(int) != 2 {
		t.Fatalf("expected patch_count=2, got %v", resMap["patch_count"])
	}
	if resMap["base_commit"].(string) != "abcdef0123456789abcdef0123456789abcdef01" {
		t.Errorf("unexpected base_commit: %v", resMap["base_commit"])
	}
	if resMap["root_message_id"].(string) != "<series-cover@kernel.org>" {
		t.Errorf("unexpected root_message_id: %v", resMap["root_message_id"])
	}

	mboxBytes, err := os.ReadFile(mboxPath)
	if err != nil {
		t.Fatalf("failed to read exported mbox %s: %v", mboxPath, err)
	}
	mbox := string(mboxBytes)

	idxP1 := strings.Index(mbox, "Subject: [PATCH 1/2] tracing: first change")
	idxP2 := strings.Index(mbox, "Subject: [PATCH 2/2] tracing: second change")
	if idxP1 == -1 || idxP2 == -1 || idxP1 > idxP2 {
		t.Fatalf("expected PATCH 1/2 before PATCH 2/2 in mbox, got idxP1=%d idxP2=%d", idxP1, idxP2)
	}
	if strings.Contains(mbox, "Subject: [PATCH 0/2]") || strings.Contains(mbox, "Subject: Re:") {
		t.Errorf("expected patches_only=true to exclude cover letter and replies")
	}

	// Verify trailers were injected before '---'
	p1Section := mbox[idxP1:idxP2]
	if !strings.Contains(p1Section, "Reviewed-by: Reviewer <reviewer@example.com>") {
		t.Errorf("expected Reviewed-by trailer in patch 1:\n%s", p1Section)
	}
	if !strings.Contains(p1Section, "Acked-by: Maintainer <maintainer@kernel.org>") {
		t.Errorf("expected Acked-by cover trailer in patch 1:\n%s", p1Section)
	}
	// Verify body line starting with "From " was escaped to ">From " in patch 2
	p2Section := mbox[idxP2:]
	if !strings.Contains(p2Section, ">From the commit log line") {
		t.Errorf("expected mbox From-line escaping in patch 2:\n%s", p2Section)
	}

	// Verify patches_only=false exports all 5 messages
	outAll := filepath.Join(t.TempDir(), "all-thread.mbx")
	resAll, err := server.CallTool(ctx, "export_thread_mbox", map[string]interface{}{
		"message_id":   "series-cover@kernel.org",
		"output_path":  outAll,
		"patches_only": false,
	})
	if err != nil {
		t.Fatalf("export_thread_mbox patches_only=false failed: %v", err)
	}
	if resAll.(map[string]interface{})["patch_count"].(int) != 5 {
		t.Errorf("expected 5 messages with patches_only=false, got %v", resAll.(map[string]interface{})["patch_count"])
	}

	// Verify disallowed output_path outside temp/cache dirs is rejected
	if _, err := server.CallTool(ctx, "export_thread_mbox", map[string]interface{}{
		"message_id":  "series-cover@kernel.org",
		"output_path": "/etc/undined-forbidden.mbx",
	}); err == nil {
		t.Errorf("expected output_path outside allowed dirs to be rejected")
	}

	// Verify symlink inside temp dir pointing outside allowed dirs is rejected
	symlinkPath := filepath.Join(t.TempDir(), "escape.mbx")
	if err := os.Symlink("/etc/hosts", symlinkPath); err == nil {
		if _, err := server.CallTool(ctx, "export_thread_mbox", map[string]interface{}{
			"message_id":  "series-cover@kernel.org",
			"output_path": symlinkPath,
		}); err == nil {
			t.Errorf("expected symlink output_path escaping allowed dirs to be rejected")
		}
	}
}

func TestExportThreadMboxStripsLeadingEnvelopeFromForGitAm(t *testing.T) {
	server, db, store, _, _, _ := setupTestMCPEnv(t)
	todoFolder, err := db.GetOrCreateFolder(constants.FolderTODO, false)
	if err != nil {
		t.Fatalf("GetOrCreateFolder failed: %v", err)
	}

	// Patch 1 mimics a Patchwork-synced mbox file starting with "From patchwork ..."
	patchworkRaw := "From patchwork Sat Nov 28 10:00:00 2026\r\n" +
		"Return-Path: <dev@kernel.org>\r\n" +
		"From: Kernel Developer <dev@kernel.org>\r\n" +
		"To: linux-kernel@vger.kernel.org\r\n" +
		"Subject: [PATCH 1/2] demo: update line one\r\n" +
		"Message-ID: <pw-p1@kernel.org>\r\n" +
		"Date: Sat, 28 Nov 2026 10:00:00 +0000\r\n\r\n" +
		"Update line one.\n\n" +
		"Signed-off-by: Kernel Developer <dev@kernel.org>\n" +
		"---\n" +
		" demo.txt | 2 +-\n" +
		" 1 file changed, 1 insertion(+), 1 deletion(-)\n\n" +
		"diff --git a/demo.txt b/demo.txt\n" +
		"--- a/demo.txt\n" +
		"+++ b/demo.txt\n" +
		"@@ -1,2 +1,2 @@\n" +
		"-line1\n" +
		"+line1-updated\n" +
		" line2\n"

	// Patch 2 mimics a b4/lore-synced mboxrd file starting with "From mboxrd@z ..."
	mboxrdRaw := "From mboxrd@z Thu Jan  1 00:00:00 1970\n" +
		"From: Kernel Developer <dev@kernel.org>\n" +
		"To: linux-kernel@vger.kernel.org\n" +
		"Subject: [PATCH 2/2] demo: update line two\n" +
		"Message-ID: <pw-p2@kernel.org>\n" +
		"In-Reply-To: <pw-p1@kernel.org>\n" +
		"References: <pw-p1@kernel.org>\n" +
		"Date: Sat, 28 Nov 2026 10:01:00 +0000\n\n" +
		"Update line two.\n\n" +
		"Signed-off-by: Kernel Developer <dev@kernel.org>\n" +
		"---\n" +
		" demo.txt | 2 +-\n" +
		" 1 file changed, 1 insertion(+), 1 deletion(-)\n\n" +
		"diff --git a/demo.txt b/demo.txt\n" +
		"--- a/demo.txt\n" +
		"+++ b/demo.txt\n" +
		"@@ -1,2 +1,2 @@\n" +
		" line1-updated\n" +
		"-line2\n" +
		"+line2-updated\n"

	for _, raw := range []string{patchworkRaw, mboxrdRaw} {
		p, addErr := store.AddMessage(constants.FolderTODO, []byte(raw), "")
		if addErr != nil {
			t.Fatalf("AddMessage failed: %v", addErr)
		}
		if _, idxErr := db.IndexEmailFile(todoFolder.ID, p); idxErr != nil {
			t.Fatalf("IndexEmailFile failed: %v", idxErr)
		}
	}

	outPath := filepath.Join(t.TempDir(), "clean-series.mbx")
	res, err := server.CallTool(context.Background(), "export_thread_mbox", map[string]interface{}{
		"message_id":  "pw-p1@kernel.org",
		"output_path": outPath,
	})
	if err != nil {
		t.Fatalf("export_thread_mbox failed: %v", err)
	}
	if got := res.(map[string]interface{})["patch_count"].(int); got != 2 {
		t.Fatalf("patch_count = %d, want 2", got)
	}

	mboxBytes, err := os.ReadFile(outPath)
	if err != nil {
		t.Fatalf("read exported mbox: %v", err)
	}
	mbox := string(mboxBytes)
	if strings.Contains(mbox, "From patchwork ") {
		t.Errorf("exported mbox retained duplicate Patchwork envelope From line:\n%s", mbox)
	}
	if strings.Contains(mbox, "From mboxrd@z ") {
		t.Errorf("exported mbox retained duplicate mboxrd envelope From line:\n%s", mbox)
	}

	// Verify git am applies both patches cleanly without "Patch is empty."
	if _, lookErr := exec.LookPath("git"); lookErr == nil {
		repoDir := t.TempDir()
		runGit := func(args ...string) {
			t.Helper()
			cmd := exec.Command("git", args...)
			cmd.Dir = repoDir
			out, runErr := cmd.CombinedOutput()
			if runErr != nil {
				t.Fatalf("git %v failed: %v\noutput:\n%s", args, runErr, string(out))
			}
		}
		runGit("init")
		runGit("config", "user.name", "Kernel Developer")
		runGit("config", "user.email", "dev@example.com")
		if err := os.WriteFile(filepath.Join(repoDir, "demo.txt"), []byte("line1\nline2\n"), 0644); err != nil {
			t.Fatalf("write seed file: %v", err)
		}
		runGit("add", "demo.txt")
		runGit("commit", "-m", "initial")

		runGit("am", outPath)

		gotContent, err := os.ReadFile(filepath.Join(repoDir, "demo.txt"))
		if err != nil {
			t.Fatalf("read patched file: %v", err)
		}
		wantContent := "line1-updated\nline2-updated\n"
		if string(gotContent) != wantContent {
			t.Errorf("patched demo.txt = %q, want %q", string(gotContent), wantContent)
		}
	}
}
