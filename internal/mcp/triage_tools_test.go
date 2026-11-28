package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"undined-go/internal/config"
	"undined-go/internal/constants"
	"undined-go/internal/events"
	"undined-go/internal/storage/database"
	"undined-go/internal/storage/maildir"
)

func setupTriageTestServer(t *testing.T) (*Server, *database.Database, *maildir.MaildirStore, *bytes.Buffer, *events.Hub) {
	t.Helper()
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "triage.db")
	mdPath := filepath.Join(tmpDir, "maildir")
	logPath := filepath.Join(tmpDir, "undined.log")

	cfg := config.NewDefaultConfig()
	cfg.General.DBPath = dbPath
	cfg.General.MaildirPath = mdPath
	cfg.General.LogFile = logPath
	cfg.SMTP.FromName = "Kernel Developer"
	cfg.SMTP.FromEmail = "dev@kernel.org"
	cfg.SMTP.Aliases = []string{"dev@example.com"}
	cfg.SMTP.Password = "super-secret-smtp-pass"
	cfg.IMAP.Username = "dev@kernel.org"
	cfg.IMAP.Password = "super-secret-imap-pass"
	cfg.Patchwork.Token = "super-secret-pw-token"

	db, err := database.NewDatabase(dbPath)
	if err != nil {
		t.Fatalf("failed to init db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	store := maildir.NewMaildirStore(mdPath)
	hub := events.NewHub()
	srv := NewServer(cfg, store, db, nil, nil)
	srv.SetEventHub(hub)

	var logBuf bytes.Buffer
	srv.SetLogWriter(&logBuf)

	return srv, db, store, &logBuf, hub
}

func indexRawMail(t *testing.T, db *database.Database, store *maildir.MaildirStore, folderName, raw string, imapUID ...uint32) int64 {
	t.Helper()
	if err := store.EnsureFolder(folderName); err != nil {
		t.Fatalf("ensure folder %s: %v", folderName, err)
	}
	folder, err := db.GetOrCreateFolder(folderName, !database.IsLocalOnlyFolder(folderName))
	if err != nil {
		t.Fatalf("get/create folder %s: %v", folderName, err)
	}
	fp, err := store.AddMessage(folderName, []byte(raw), "")
	if err != nil {
		t.Fatalf("add maildir msg: %v", err)
	}
	id, err := db.IndexEmailFile(folder.ID, fp, imapUID...)
	if err != nil {
		t.Fatalf("index email: %v", err)
	}
	return id
}

func TestValidateTagKeyword(t *testing.T) {
	valid := []string{"$cl_1", "$cl_2", "$cl_3", "$cl_5", "$label1", "PR-Draft", "custom_tag.v2", strings.Repeat("a", 64)}
	for _, tag := range valid {
		if err := ValidateTagKeyword(tag); err != nil {
			t.Errorf("expected valid tag %q, got error: %v", tag, err)
		}
	}

	invalid := []string{
		"",
		`\Deleted`,
		`\Seen`,
		`\Draft`,
		"seen",
		"draft",
		"deleted",
		"PR",
		"RT",
		"bad tag",
		"tab\ttag",
		"line\nbreak",
		"cr\rbreak",
		"wild*card",
		"pct%tag",
		"slash/tag",
		strings.Repeat("a", 65),
	}
	for _, tag := range invalid {
		err := ValidateTagKeyword(tag)
		if err == nil {
			t.Errorf("expected error for invalid tag %q, got nil", tag)
			continue
		}
		var invalidErr *InvalidParamsError
		if !errors.As(err, &invalidErr) {
			t.Errorf("expected InvalidParamsError for %q, got %T: %v", tag, err, err)
		}
	}
}

func TestUpdateEmailTagsValidationAndJSONRPC(t *testing.T) {
	srv, db, store, logBuf, _ := setupTriageTestServer(t)
	ctx := context.Background()

	id := indexRawMail(t, db, store, constants.FolderTODO,
		"Message-ID: <val-1@example.com>\r\nFrom: Alice <alice@example.com>\r\nSubject: [PATCH] test\r\nDate: Mon, 16 Nov 2026 10:00:00 +0000\r\n\r\nhello\r\n")
	idStr := strconv.FormatInt(id, 10)

	cases := []struct {
		name string
		args map[string]interface{}
	}{
		{
			name: "missing_message_ids",
			args: map[string]interface{}{"add_tags": []interface{}{"$cl_1"}},
		},
		{
			name: "empty_message_ids",
			args: map[string]interface{}{"message_ids": []interface{}{}, "add_tags": []interface{}{"$cl_1"}},
		},
		{
			name: "no_tag_operations",
			args: map[string]interface{}{"message_ids": []interface{}{idStr}},
		},
		{
			name: "replace_tags_combined_with_add_tags",
			args: map[string]interface{}{
				"message_ids":  []interface{}{idStr},
				"add_tags":     []interface{}{"$cl_1"},
				"replace_tags": []interface{}{"$cl_2"},
			},
		},
		{
			name: "replace_tags_combined_with_remove_tags",
			args: map[string]interface{}{
				"message_ids":  []interface{}{idStr},
				"remove_tags":  []interface{}{"$cl_1"},
				"replace_tags": []interface{}{"$cl_2"},
			},
		},
		{
			name: "overlap_between_add_tags_and_remove_tags",
			args: map[string]interface{}{
				"message_ids": []interface{}{idStr},
				"add_tags":    []interface{}{"$cl_1"},
				"remove_tags": []interface{}{"$CL_1"},
			},
		},
		{
			name: "system_flag_disallowed",
			args: map[string]interface{}{
				"message_ids": []interface{}{idStr},
				"add_tags":    []interface{}{`\Deleted`},
			},
		},
		{
			name: "exceeds_max_32_tags",
			args: func() map[string]interface{} {
				many := make([]interface{}, 33)
				for i := range many {
					many[i] = fmt.Sprintf("tag_%d", i+1)
				}
				return map[string]interface{}{
					"message_ids":  []interface{}{idStr},
					"replace_tags": many,
				}
			}(),
		},
		{
			name: "invalid_scope",
			args: map[string]interface{}{
				"message_ids": []interface{}{idStr},
				"add_tags":    []interface{}{"$cl_1"},
				"scope":       "folder",
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := srv.CallTool(ctx, "update_email_tags", tc.args)
			if err == nil {
				t.Fatalf("expected error for %s, got nil", tc.name)
			}
			var inv *InvalidParamsError
			if !errors.As(err, &inv) {
				t.Fatalf("expected *InvalidParamsError for %s, got %T (%v)", tc.name, err, err)
			}
		})
	}

	if !strings.Contains(logBuf.String(), "REJECTED") {
		t.Errorf("expected REJECTED entries in MCP access log, got:\n%s", logBuf.String())
	}

	// Verify JSON-RPC STDIO maps InvalidParamsError to error code -32602.
	reqLine := fmt.Sprintf(`{"jsonrpc":"2.0","id":99,"method":"tools/call","params":{"name":"update_email_tags","arguments":{"message_ids":[%q],"add_tags":["\\Deleted"]}}}`, idStr)
	var outBuf bytes.Buffer
	if err := srv.HandleSTDIO(ctx, strings.NewReader(reqLine+"\n"), &outBuf); err != nil {
		t.Fatalf("HandleSTDIO failed: %v", err)
	}
	var rpcResp map[string]interface{}
	if err := json.Unmarshal(bytes.TrimSpace(outBuf.Bytes()), &rpcResp); err != nil {
		t.Fatalf("unmarshal stdio response: %v (raw=%s)", err, outBuf.String())
	}
	errObj, ok := rpcResp["error"].(map[string]interface{})
	if !ok {
		t.Fatalf("expected JSON-RPC error object, got %v", rpcResp)
	}
	if int(errObj["code"].(float64)) != -32602 {
		t.Errorf("expected JSON-RPC error code -32602, got %v", errObj["code"])
	}
}

func TestStripUnifiedDiffAndExtractors(t *testing.T) {
	body := strings.Join([]string{
		"Fix race condition when registering probes inside LR/SC.",
		"",
		"Fixes: 123456789abc (\"riscv: add kprobes support\")",
		"Reported-by: Bot <bot@example.com>",
		"Reviewed-by: Reviewer One <rev1@example.com>",
		"Acked-by: Maintainer <maint@example.com>",
		"Cc: stable@vger.kernel.org",
		"Signed-off-by: Alice <alice@example.com>",
		"---",
		"v2:",
		"- Addressed review feedback",
		"",
		" arch/riscv/kernel/probes/kprobes.c | 12 +++++++++---",
		" 1 file changed, 9 insertions(+), 3 deletions(-)",
		"",
		"diff --git a/arch/riscv/kernel/probes/kprobes.c b/arch/riscv/kernel/probes/kprobes.c",
		"index 1111111..2222222 100644",
		"--- a/arch/riscv/kernel/probes/kprobes.c",
		"+++ b/arch/riscv/kernel/probes/kprobes.c",
		"@@ -10,3 +10,9 @@ int arch_prepare_kprobe(struct kprobe *p)",
		"-    return 0;",
		"+    if (in_lr_sc(p->addr))",
		"+        return -EINVAL;",
		"+    return 0;",
	}, "\n")

	stripped := StripUnifiedDiff(body)
	if strings.Contains(stripped, "diff --git") || strings.Contains(stripped, "+    if (in_lr_sc") {
		t.Errorf("expected unified diff to be stripped, got:\n%s", stripped)
	}
	for _, mustKeep := range []string{
		"Fix race condition",
		"Fixes: 123456789abc",
		"Reviewed-by: Reviewer One <rev1@example.com>",
		"Cc: stable@vger.kernel.org",
		"arch/riscv/kernel/probes/kprobes.c | 12 +++++++++---",
		"1 file changed, 9 insertions(+), 3 deletions(-)",
	} {
		if !strings.Contains(stripped, mustKeep) {
			t.Errorf("expected stripped body to keep %q, got:\n%s", mustKeep, stripped)
		}
	}

	trailers := ExtractEmailTrailers(body)
	if len(trailers["Fixes"]) != 1 || !strings.Contains(trailers["Fixes"][0], "123456789abc") {
		t.Errorf("unexpected Fixes trailer: %v", trailers["Fixes"])
	}
	if len(trailers["Reviewed-by"]) != 1 {
		t.Errorf("unexpected Reviewed-by trailer: %v", trailers["Reviewed-by"])
	}
	if len(trailers["Acked-by"]) != 1 {
		t.Errorf("unexpected Acked-by trailer: %v", trailers["Acked-by"])
	}
	if len(trailers["Reported-by"]) != 1 {
		t.Errorf("unexpected Reported-by trailer: %v", trailers["Reported-by"])
	}
	if len(trailers["Cc-stable"]) != 1 {
		t.Errorf("expected Cc-stable trailer, got: %v", trailers)
	}

	diffstat := ExtractDiffstat(body)
	if !strings.Contains(diffstat, "arch/riscv/kernel/probes/kprobes.c | 12 +++++++++---") ||
		!strings.Contains(diffstat, "1 file changed, 9 insertions(+), 3 deletions(-)") {
		t.Errorf("unexpected diffstat:\n%s", diffstat)
	}
	if strings.Contains(diffstat, "v2:") {
		t.Errorf("changelog prose should not leak into diffstat:\n%s", diffstat)
	}
}

func TestUpdateEmailTagsCrossFolderIMAPQueueDryRunAndSSE(t *testing.T) {
	srv, db, store, _, hub := setupTriageTestServer(t)
	ctx := context.Background()

	coverRaw := "Message-ID: <series-cover@example.com>\r\n" +
		"From: Alice <alice@example.com>\r\n" +
		"Subject: [PATCH 0/2] tracing: cover\r\n" +
		"Date: Tue, 17 Nov 2026 10:00:00 +0000\r\n\r\nCover letter body.\r\n"
	patch1Raw := "Message-ID: <series-p1@example.com>\r\n" +
		"In-Reply-To: <series-cover@example.com>\r\n" +
		"References: <series-cover@example.com>\r\n" +
		"From: Alice <alice@example.com>\r\n" +
		"Subject: [PATCH 1/2] tracing: part 1\r\n" +
		"Date: Tue, 17 Nov 2026 10:01:00 +0000\r\n\r\ndiff --git a/a.c b/a.c\n--- a/a.c\n+++ b/a.c\n@@ -1 +1 @@\n-a\n+b\n"

	// Put both in TODO (local) and also in an IMAP folder with valid IMAP UIDs.
	todoCoverID := indexRawMail(t, db, store, constants.FolderTODO, coverRaw)
	todoP1ID := indexRawMail(t, db, store, constants.FolderTODO, patch1Raw)

	imapFolder, err := db.GetOrCreateFolder("kernel.org/maintainer", true)
	if err != nil {
		t.Fatalf("create imap folder: %v", err)
	}
	imapCoverID, err := db.InsertHeaderSummary(imapFolder.ID, 501, []byte(coverRaw), true, `\Seen`)
	if err != nil {
		t.Fatalf("insert imap cover: %v", err)
	}
	imapP1ID, err := db.InsertHeaderSummary(imapFolder.ID, 502, []byte(patch1Raw), true, `\Seen`)
	if err != nil {
		t.Fatalf("insert imap p1: %v", err)
	}

	// 1. dry_run=true must NOT mutate DB, queue, or SSE.
	dryResRaw, err := srv.CallTool(ctx, "update_email_tags", map[string]interface{}{
		"message_ids": []interface{}{strconv.FormatInt(todoP1ID, 10)},
		"scope":       "thread",
		"add_tags":    []interface{}{"$cl_1"},
		"remove_tags": []interface{}{"$cl_2", "$cl_3", "$cl_5"},
		"dry_run":     true,
	})
	if err != nil {
		t.Fatalf("dry_run failed: %v", err)
	}
	dryRes := dryResRaw.(map[string]interface{})
	if dryRes["dry_run"] != true || dryRes["updated_count"] != 4 || dryRes["imap_sync_queued_count"] != 0 {
		t.Fatalf("unexpected dry_run result: %+v", dryRes)
	}
	if cnt, _ := db.CountPendingIMAPSyncQueue(); cnt != 0 {
		t.Fatalf("dry_run must not enqueue imap_sync_queue, got %d", cnt)
	}
	if hub.RingSize() != 0 {
		t.Fatalf("dry_run must not publish SSE events, got %d", hub.RingSize())
	}
	if m, _ := db.GetMessageByID(todoCoverID); len(database.ExtractUserTagsFromFlags(m.Flags)) != 0 {
		t.Fatalf("dry_run must not mutate DB flags, got %q", m.Flags)
	}

	// 2. Real call with default folder=null updates all 4 copies across TODO + IMAP folder.
	resRaw, err := srv.CallTool(ctx, "update_email_tags", map[string]interface{}{
		"message_ids": []interface{}{strconv.FormatInt(todoP1ID, 10)},
		"scope":       "thread",
		"add_tags":    []interface{}{"$cl_1"},
		"remove_tags": []interface{}{"$cl_2", "$cl_3", "$cl_5"},
	})
	if err != nil {
		t.Fatalf("update_email_tags failed: %v", err)
	}
	res := resRaw.(map[string]interface{})
	if res["updated_count"] != 4 {
		t.Errorf("expected updated_count=4, got %v", res["updated_count"])
	}
	if res["imap_sync_queued_count"] != 2 {
		t.Errorf("expected imap_sync_queued_count=2 (IMAP copies only), got %v", res["imap_sync_queued_count"])
	}

	// Verify both TODO and IMAP copies have $cl_1 and IMAP copies preserved \Seen.
	for _, checkID := range []int64{todoCoverID, todoP1ID, imapCoverID, imapP1ID} {
		m, _ := db.GetMessageByID(checkID)
		tags := database.ExtractUserTagsFromFlags(m.Flags)
		if len(tags) != 1 || tags[0] != "$cl_1" {
			t.Errorf("msg %d expected tags [$cl_1], got %v (flags=%q)", checkID, tags, m.Flags)
		}
	}
	if m, _ := db.GetMessageByID(imapCoverID); !strings.Contains(m.Flags, `\Seen`) {
		t.Errorf("expected \\Seen preserved on IMAP copy, got %q", m.Flags)
	}

	// Verify imap_sync_queue contains exactly 2 pending rows for the IMAP folder.
	qItems, err := db.ListPendingIMAPSyncQueue(10)
	if err != nil || len(qItems) != 2 {
		t.Fatalf("expected 2 queued IMAP sync items, got %d (err=%v)", len(qItems), err)
	}
	for _, qi := range qItems {
		if qi.FolderName != "kernel.org/maintainer" {
			t.Errorf("expected IMAP folder in queue, got %q", qi.FolderName)
		}
		if len(qi.AddTags) != 1 || qi.AddTags[0] != "$cl_1" {
			t.Errorf("expected AddTags=[$cl_1], got %v", qi.AddTags)
		}
	}

	// Verify SSE email_tags_updated event was emitted.
	evs := hub.EventsSince(0)
	if len(evs) != 1 || evs[0].Type != "email_tags_updated" {
		t.Fatalf("expected 1 email_tags_updated SSE event, got %+v", evs)
	}

	// 3. Idempotent re-run produces updated_count=0, unchanged_count=4, imap_sync_queued_count=0.
	idemRaw, err := srv.CallTool(ctx, "update_email_tags", map[string]interface{}{
		"message_ids": []interface{}{strconv.FormatInt(todoP1ID, 10)},
		"scope":       "thread",
		"add_tags":    []interface{}{"$cl_1"},
	})
	if err != nil {
		t.Fatalf("idempotent re-run failed: %v", err)
	}
	idem := idemRaw.(map[string]interface{})
	if idem["updated_count"] != 0 || idem["unchanged_count"] != 4 || idem["imap_sync_queued_count"] != 0 {
		t.Errorf("expected idempotent no-op, got %+v", idem)
	}

	// 4. Explicit folder="TODO" restricts update to TODO copies only and queues 0 IMAP ops.
	folderResRaw, err := srv.CallTool(ctx, "update_email_tags", map[string]interface{}{
		"message_ids": []interface{}{strconv.FormatInt(todoP1ID, 10)},
		"scope":       "thread",
		"folder":      constants.FolderTODO,
		"add_tags":    []interface{}{"$cl_5"},
		"remove_tags": []interface{}{"$cl_1"},
	})
	if err != nil {
		t.Fatalf("folder-scoped update failed: %v", err)
	}
	folderRes := folderResRaw.(map[string]interface{})
	if folderRes["updated_count"] != 2 || folderRes["imap_sync_queued_count"] != 0 {
		t.Errorf("expected updated_count=2, imap_sync_queued_count=0, got %+v", folderRes)
	}
	// IMAP copy still has $cl_1, TODO copy now has $cl_5.
	if m, _ := db.GetMessageByID(todoP1ID); strings.Join(database.ExtractUserTagsFromFlags(m.Flags), ",") != "$cl_5" {
		t.Errorf("expected TODO copy to have $cl_5, got %q", m.Flags)
	}
	if m, _ := db.GetMessageByID(imapP1ID); strings.Join(database.ExtractUserTagsFromFlags(m.Flags), ",") != "$cl_1" {
		t.Errorf("expected IMAP copy to remain $cl_1, got %q", m.Flags)
	}
}

func TestListTagsAndGetUserIdentity(t *testing.T) {
	srv, db, store, _, _ := setupTriageTestServer(t)
	ctx := context.Background()

	id1 := indexRawMail(t, db, store, constants.FolderTODO,
		"Message-ID: <tag-1@example.com>\r\nFrom: Alice <alice@example.com>\r\nSubject: one\r\nDate: Tue, 17 Nov 2026 10:00:00 +0000\r\n\r\nbody\r\n")
	id2 := indexRawMail(t, db, store, constants.FolderB4Imports,
		"Message-ID: <tag-2@example.com>\r\nFrom: Bob <bob@example.com>\r\nSubject: two\r\nDate: Tue, 17 Nov 2026 11:00:00 +0000\r\n\r\nbody\r\n")

	if _, err := srv.CallTool(ctx, "update_email_tags", map[string]interface{}{
		"message_ids": []interface{}{strconv.FormatInt(id1, 10)},
		"add_tags":    []interface{}{"$cl_1", "PR-Draft"},
	}); err != nil {
		t.Fatalf("tag id1: %v", err)
	}
	if _, err := srv.CallTool(ctx, "update_email_tags", map[string]interface{}{
		"message_ids": []interface{}{strconv.FormatInt(id2, 10)},
		"add_tags":    []interface{}{"$cl_1"},
	}); err != nil {
		t.Fatalf("tag id2: %v", err)
	}

	// Folder-scoped list_tags("TODO")
	tagsRaw, err := srv.CallTool(ctx, "list_tags", map[string]interface{}{"folder": constants.FolderTODO})
	if err != nil {
		t.Fatalf("list_tags failed: %v", err)
	}
	tagsRes := tagsRaw.(map[string]interface{})
	colorLabels := tagsRes["color_labels"].([]map[string]interface{})
	if len(colorLabels) == 0 || colorLabels[0]["tag"] != "$cl_1" || colorLabels[0]["count"] != 1 {
		t.Errorf("expected $cl_1 first with count=1 in TODO, got %+v", colorLabels[:2])
	}
	customTags := tagsRes["custom_tags"].([]map[string]interface{})
	if len(customTags) != 1 || customTags[0]["tag"] != "PR-Draft" || customTags[0]["count"] != 1 {
		t.Errorf("expected custom tag PR-Draft count=1, got %+v", customTags)
	}

	// get_user_identity returns aliases without secrets
	idRaw, err := srv.CallTool(ctx, "get_user_identity", map[string]interface{}{})
	if err != nil {
		t.Fatalf("get_user_identity failed: %v", err)
	}
	idJSON, _ := json.Marshal(idRaw)
	if strings.Contains(string(idJSON), "super-secret") {
		t.Fatalf("get_user_identity leaked secret: %s", string(idJSON))
	}
	idMap := idRaw.(map[string]interface{})
	if idMap["display_name"] != "Kernel Developer" || idMap["primary_email"] != "dev@kernel.org" {
		t.Errorf("unexpected identity: %+v", idMap)
	}
	aliases := idMap["aliases"].([]string)
	if len(aliases) != 2 || aliases[0] != "dev@kernel.org" || aliases[1] != "dev@example.com" {
		t.Errorf("unexpected aliases: %v", aliases)
	}
}

func TestGetEmailAndThreadContextDedupAndLocalCacheOnly(t *testing.T) {
	srv, db, store, _, _ := setupTriageTestServer(t)
	ctx := context.Background()

	patchRaw := "Message-ID: <dedup-p1@example.com>\r\n" +
		"From: Alice <alice@example.com>\r\n" +
		"Subject: [PATCH] tracing: fix bug\r\n" +
		"Date: Tue, 17 Nov 2026 10:00:00 +0000\r\n\r\n" +
		"Commit description.\n\n" +
		"Fixes: abcdef123456 (\"old commit\")\n" +
		"---\n" +
		" kernel/trace/ftrace.c | 2 +-\n" +
		" 1 file changed, 1 insertion(+), 1 deletion(-)\n\n" +
		"diff --git a/kernel/trace/ftrace.c b/kernel/trace/ftrace.c\n" +
		"--- a/kernel/trace/ftrace.c\n" +
		"+++ b/kernel/trace/ftrace.c\n" +
		"@@ -1,1 +1,1 @@\n" +
		"-old\n+new\n"

	// Hydrated copy in TODO
	todoID := indexRawMail(t, db, store, constants.FolderTODO, patchRaw)
	if _, err := srv.CallTool(ctx, "update_email_tags", map[string]interface{}{
		"message_ids": []interface{}{strconv.FormatInt(todoID, 10)},
		"add_tags":    []interface{}{"$cl_2"},
	}); err != nil {
		t.Fatalf("tag todo patch: %v", err)
	}

	// Unhydrated duplicate in IMAP folder + unhydrated IMAP reply
	imapFolder, _ := db.GetOrCreateFolder("kernel.org/maintainer", true)
	_, err := db.InsertHeaderSummary(imapFolder.ID, 601, []byte(
		"Message-ID: <dedup-p1@example.com>\r\nFrom: Alice <alice@example.com>\r\nSubject: [PATCH] tracing: fix bug\r\nDate: Tue, 17 Nov 2026 10:00:00 +0000\r\n\r\n",
	), true, `\Seen`)
	if err != nil {
		t.Fatalf("insert unhydrated dup: %v", err)
	}
	unhydratedReplyID, err := db.InsertHeaderSummary(imapFolder.ID, 602, []byte(
		"Message-ID: <dedup-reply@example.com>\r\nIn-Reply-To: <dedup-p1@example.com>\r\nReferences: <dedup-p1@example.com>\r\nFrom: Bob <bob@example.com>\r\nSubject: Re: [PATCH] tracing: fix bug\r\nDate: Tue, 17 Nov 2026 11:00:00 +0000\r\n\r\n",
	), false, "")
	if err != nil {
		t.Fatalf("insert unhydrated reply: %v", err)
	}

	// get_email on patch with include_diff=false
	emailRaw, err := srv.CallTool(ctx, "get_email", map[string]interface{}{
		"message_id":   strconv.FormatInt(todoID, 10),
		"include_diff": false,
	})
	if err != nil {
		t.Fatalf("get_email failed: %v", err)
	}
	email := emailRaw.(map[string]interface{})
	if email["body_hydrated"] != true {
		t.Errorf("expected body_hydrated=true, got %v", email["body_hydrated"])
	}
	if strings.Contains(email["body_plain"].(string), "diff --git") {
		t.Errorf("expected diff stripped when include_diff=false")
	}
	if !strings.Contains(email["diffstat"].(string), "1 file changed") {
		t.Errorf("expected diffstat populated, got %q", email["diffstat"])
	}
	trailers := email["trailers"].(map[string][]string)
	if len(trailers["Fixes"]) != 1 {
		t.Errorf("expected Fixes trailer, got %+v", trailers)
	}

	// get_email on unhydrated IMAP reply returns body_hydrated=false with empty body and zero IMAP calls
	replyRaw, err := srv.CallTool(ctx, "get_email", map[string]interface{}{
		"message_id": strconv.FormatInt(unhydratedReplyID, 10),
	})
	if err != nil {
		t.Fatalf("get_email unhydrated reply failed: %v", err)
	}
	reply := replyRaw.(map[string]interface{})
	if reply["body_hydrated"] != false || reply["body_plain"] != "" {
		t.Errorf("expected body_hydrated=false and empty body_plain, got hydrated=%v body=%q", reply["body_hydrated"], reply["body_plain"])
	}

	// get_thread_context with deduplicate=true and include_diff=false
	threadRaw, err := srv.CallTool(ctx, "get_thread_context", map[string]interface{}{
		"message_id":   "dedup-p1@example.com",
		"include_diff": false,
		"deduplicate":  true,
	})
	if err != nil {
		t.Fatalf("get_thread_context failed: %v", err)
	}
	msgs := threadRaw.([]map[string]interface{})
	if len(msgs) != 2 {
		t.Fatalf("expected 2 deduplicated thread messages, got %d: %+v", len(msgs), msgs)
	}
	patchEntry := msgs[0]
	if patchEntry["body_hydrated"] != true {
		t.Errorf("expected hydrated TODO copy preferred, got body_hydrated=%v", patchEntry["body_hydrated"])
	}
	if strings.Contains(patchEntry["body_plain"].(string), "diff --git") {
		t.Errorf("expected diff stripped in get_thread_context")
	}
	folders := patchEntry["folders"].([]string)
	if len(folders) != 2 {
		t.Errorf("expected both folders merged in deduplicated entry, got %v", folders)
	}
	replyEntry := msgs[1]
	if replyEntry["body_hydrated"] != false || replyEntry["body_plain"] != "" {
		t.Errorf("expected unhydrated reply entry with body_hydrated=false, got %+v", replyEntry)
	}

	// get_thread_context with deduplicate=false returns all 3 rows
	rawAll, err := srv.CallTool(ctx, "get_thread_context", map[string]interface{}{
		"message_id":  "dedup-p1@example.com",
		"deduplicate": false,
	})
	if err != nil {
		t.Fatalf("get_thread_context deduplicate=false failed: %v", err)
	}
	if len(rawAll.([]map[string]interface{})) != 3 {
		t.Errorf("expected 3 non-deduplicated rows, got %d", len(rawAll.([]map[string]interface{})))
	}
}

func TestListThreadsIncrementalTriageAndPagination(t *testing.T) {
	srv, db, store, _, _ := setupTriageTestServer(t)
	ctx := context.Background()

	// Thread 1: Self-authored patch in TODO (should be excluded by exclude_from=["self"])
	indexRawMail(t, db, store, constants.FolderTODO,
		"Message-ID: <self-1@kernel.org>\r\nFrom: Kernel Developer <dev@kernel.org>\r\nSubject: [PATCH] self fix\r\nDate: Wed, 18 Nov 2026 09:00:00 +0000\r\n\r\ndiff --git a/x.c b/x.c\n--- a/x.c\n+++ b/x.c\n@@ -1 +1 @@\n-1\n+2\n")

	// Thread 2: Alice 3-patch series in TODO + 1 unhydrated IMAP reply
	aliceCoverID := indexRawMail(t, db, store, constants.FolderTODO,
		"Message-ID: <alice-0@example.com>\r\nFrom: Alice <alice@example.com>\r\nSubject: [PATCH v4 0/3] riscv: probes\r\nDate: Wed, 18 Nov 2026 10:00:00 +0000\r\n\r\nCover\r\n")
	for i := 1; i <= 3; i++ {
		indexRawMail(t, db, store, constants.FolderTODO, fmt.Sprintf(
			"Message-ID: <alice-%d@example.com>\r\nIn-Reply-To: <alice-0@example.com>\r\nReferences: <alice-0@example.com>\r\nFrom: Alice <alice@example.com>\r\nSubject: [PATCH v4 %d/3] riscv: probes part %d\r\nDate: Wed, 18 Nov 2026 10:0%d:00 +0000\r\n\r\ndiff --git a/p%d.c b/p%d.c\n--- a/p%d.c\n+++ b/p%d.c\n@@ -1 +1 @@\n-a\n+b\n",
			i, i, i, i, i, i, i, i,
		))
	}
	imapFolder, _ := db.GetOrCreateFolder("kernel.org/maintainer", true)
	_, _ = db.InsertHeaderSummary(imapFolder.ID, 701, []byte(
		"Message-ID: <alice-rep@example.com>\r\nIn-Reply-To: <alice-0@example.com>\r\nReferences: <alice-0@example.com>\r\nFrom: Reviewer <rev@example.com>\r\nSubject: Re: [PATCH v4 0/3] riscv: probes\r\nDate: Wed, 18 Nov 2026 12:00:00 +0000\r\n\r\n",
	), false, "")

	// Thread 3: Bob patch already marked $cl_5 (Done)
	bobID := indexRawMail(t, db, store, constants.FolderTODO,
		"Message-ID: <bob-1@example.com>\r\nFrom: Bob <bob@example.com>\r\nSubject: [PATCH] bob done\r\nDate: Wed, 18 Nov 2026 11:00:00 +0000\r\n\r\ndiff --git a/b.c b/b.c\n--- a/b.c\n+++ b/b.c\n@@ -1 +1 @@\n-1\n+2\n")
	if _, err := srv.CallTool(ctx, "update_email_tags", map[string]interface{}{
		"message_ids": []interface{}{strconv.FormatInt(bobID, 10)},
		"add_tags":    []interface{}{"$cl_5"},
	}); err != nil {
		t.Fatalf("tag bob: %v", err)
	}

	// Thread 4: Carol patch untriaged, with a reply from self (dev@example.com alias)
	indexRawMail(t, db, store, constants.FolderTODO,
		"Message-ID: <carol-1@example.com>\r\nFrom: Carol <carol@example.com>\r\nSubject: [PATCH] carol fix\r\nDate: Wed, 18 Nov 2026 11:30:00 +0000\r\n\r\ndiff --git a/c.c b/c.c\n--- a/c.c\n+++ b/c.c\n@@ -1 +1 @@\n-1\n+2\n")
	indexRawMail(t, db, store, constants.FolderSent,
		"Message-ID: <my-reply-carol@example.com>\r\nIn-Reply-To: <carol-1@example.com>\r\nReferences: <carol-1@example.com>\r\nFrom: Kernel Developer <dev@example.com>\r\nSubject: Re: [PATCH] carol fix\r\nDate: Wed, 18 Nov 2026 11:45:00 +0000\r\n\r\nLooks good!\r\n")

	// Query untriaged threads excluding self
	resRaw, err := srv.CallTool(ctx, "list_threads", map[string]interface{}{
		"folder":       constants.FolderTODO,
		"exclude_from": []interface{}{"self"},
		"without_tags": []interface{}{"$cl_1", "$cl_2", "$cl_3", "$cl_5"},
	})
	if err != nil {
		t.Fatalf("list_threads failed: %v", err)
	}
	res := resRaw.(map[string]interface{})
	if res["total_threads"] != 2 || res["returned"] != 2 {
		t.Fatalf("expected 2 untriaged non-self threads, got %+v", res)
	}
	threads := res["threads"].([]map[string]interface{})
	// Latest is Alice (12:00 reply), second is Carol (11:45 reply).
	if threads[0]["root_message_id"] != "alice-0@example.com" {
		t.Errorf("expected Alice thread first, got %+v", threads[0])
	}
	if threads[0]["patch_count_in_folder"] != 3 || threads[0]["total_messages_in_thread"] != 5 || threads[0]["reply_count"] != 1 {
		t.Errorf("unexpected Alice thread counts: %+v", threads[0])
	}
	if threads[0]["has_unhydrated_replies"] != true || threads[0]["has_my_reply"] != false {
		t.Errorf("unexpected Alice reply flags: %+v", threads[0])
	}
	if threads[1]["root_message_id"] != "carol-1@example.com" || threads[1]["has_my_reply"] != true {
		t.Errorf("expected Carol thread second with has_my_reply=true, got %+v", threads[1])
	}

	// Cursor pagination: limit=1 across 2 pages
	p1Raw, err := srv.CallTool(ctx, "list_threads", map[string]interface{}{
		"folder":       constants.FolderTODO,
		"exclude_from": []interface{}{"self"},
		"without_tags": []interface{}{"$cl_1", "$cl_2", "$cl_3", "$cl_5"},
		"limit":        1,
	})
	if err != nil {
		t.Fatalf("page 1 failed: %v", err)
	}
	p1 := p1Raw.(map[string]interface{})
	if p1["returned"] != 1 || p1["next_cursor"] == nil {
		t.Fatalf("expected page 1 returned=1 and non-nil next_cursor, got %+v", p1)
	}
	p2Raw, err := srv.CallTool(ctx, "list_threads", map[string]interface{}{
		"folder":       constants.FolderTODO,
		"exclude_from": []interface{}{"self"},
		"without_tags": []interface{}{"$cl_1", "$cl_2", "$cl_3", "$cl_5"},
		"limit":        1,
		"cursor":       p1["next_cursor"],
	})
	if err != nil {
		t.Fatalf("page 2 failed: %v", err)
	}
	p2 := p2Raw.(map[string]interface{})
	if p2["returned"] != 1 || p2["next_cursor"] != nil {
		t.Fatalf("expected page 2 returned=1 and next_cursor=nil, got %+v", p2)
	}

	// Tag Alice's thread with $cl_1 and re-query -> Alice's thread is now excluded (incremental triage).
	if _, err := srv.CallTool(ctx, "update_email_tags", map[string]interface{}{
		"message_ids": []interface{}{strconv.FormatInt(aliceCoverID, 10)},
		"scope":       "thread",
		"add_tags":    []interface{}{"$cl_1"},
	}); err != nil {
		t.Fatalf("tag alice thread: %v", err)
	}
	afterRaw, err := srv.CallTool(ctx, "list_threads", map[string]interface{}{
		"folder":       constants.FolderTODO,
		"exclude_from": []interface{}{"self"},
		"without_tags": []interface{}{"$cl_1", "$cl_2", "$cl_3", "$cl_5"},
	})
	if err != nil {
		t.Fatalf("list_threads after triage failed: %v", err)
	}
	after := afterRaw.(map[string]interface{})
	if after["total_threads"] != 1 {
		t.Fatalf("expected 1 untriaged thread remaining, got %+v", after)
	}
}

func TestListRecentEmailsAndSearchEmailsFilters(t *testing.T) {
	srv, db, store, _, _ := setupTriageTestServer(t)
	ctx := context.Background()

	// Thread 1: Self-authored patch in TODO (should be excluded by exclude_from=["self"])
	indexRawMail(t, db, store, constants.FolderTODO,
		"Message-ID: <filt-self@kernel.org>\r\nFrom: Kernel Developer <dev@kernel.org>\r\nSubject: [PATCH] ftrace self\r\nDate: Thu, 19 Nov 2026 09:00:00 +0000\r\n\r\nftrace body self\ndiff --git a/f.c b/f.c\n--- a/f.c\n+++ b/f.c\n@@ -1 +1 @@\n-1\n+2\n")
	idOther1 := indexRawMail(t, db, store, constants.FolderTODO,
		"Message-ID: <filt-a@example.com>\r\nFrom: Alice <alice@example.com>\r\nSubject: [PATCH] ftrace alice\r\nDate: Thu, 19 Nov 2026 10:00:00 +0000\r\n\r\nftrace body alice\ndiff --git a/a.c b/a.c\n--- a/a.c\n+++ b/a.c\n@@ -1 +1 @@\n-1\n+2\n")
	idOther2 := indexRawMail(t, db, store, constants.FolderTODO,
		"Message-ID: <filt-b@example.com>\r\nFrom: Bob <bob@example.com>\r\nSubject: ftrace discussion\r\nDate: Thu, 19 Nov 2026 11:00:00 +0000\r\n\r\nftrace body bob discussion\r\n")

	if _, err := srv.CallTool(ctx, "update_email_tags", map[string]interface{}{
		"message_ids": []interface{}{strconv.FormatInt(idOther1, 10)},
		"add_tags":    []interface{}{"$cl_1"},
	}); err != nil {
		t.Fatalf("tag idOther1: %v", err)
	}
	if _, err := srv.CallTool(ctx, "update_email_tags", map[string]interface{}{
		"message_ids": []interface{}{strconv.FormatInt(idOther2, 10)},
		"add_tags":    []interface{}{"$cl_5"},
	}); err != nil {
		t.Fatalf("tag idOther2: %v", err)
	}

	// list_recent_emails with exclude_from=["self"], has_tags=["$cl_1"], is_patch=true
	listRaw, err := srv.CallTool(ctx, "list_recent_emails", map[string]interface{}{
		"folder":       constants.FolderTODO,
		"exclude_from": []interface{}{"self"},
		"has_tags":     []interface{}{"$cl_1"},
		"is_patch":     true,
	})
	if err != nil {
		t.Fatalf("list_recent_emails filtered failed: %v", err)
	}
	list := listRaw.([]map[string]interface{})
	if len(list) != 1 || list[0]["message_id"] != "filt-a@example.com" {
		t.Fatalf("expected only filt-a@example.com, got %+v", list)
	}
	if list[0]["thread_id"] == "" {
		t.Errorf("expected thread_id populated, got %+v", list[0])
	}
	tags := list[0]["tags"].([]string)
	if len(tags) != 1 || tags[0] != "$cl_1" {
		t.Errorf("expected tags [$cl_1], got %v", tags)
	}

	// search_emails with folder and without_tags=["$cl_5"]
	searchRaw, err := srv.CallTool(ctx, "search_emails", map[string]interface{}{
		"query":        "ftrace",
		"folder":       constants.FolderTODO,
		"has_tags":     []interface{}{"$cl_1"},
		"without_tags": []interface{}{"$cl_5"},
	})
	if err != nil {
		t.Fatalf("search_emails filtered failed: %v", err)
	}
	search := searchRaw.([]map[string]interface{})
	if len(search) != 1 || search[0]["message_id"] != "filt-a@example.com" {
		t.Fatalf("expected only filt-a@example.com in search, got %+v", search)
	}
	if search[0]["folder_name"] != constants.FolderTODO {
		t.Errorf("expected folder_name=TODO, got %v", search[0]["folder_name"])
	}
}
