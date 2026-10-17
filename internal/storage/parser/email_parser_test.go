package parser

import (
	"strings"
	"testing"
)

func TestParseEmail(t *testing.T) {
	raw := `From: "Masami Hiramatsu" <mhiramat@kernel.org>
To: linux-trace-kernel@vger.kernel.org
Cc: Steven Rostedt <rostedt@goodmis.org>
Subject: [PATCH] ftrace: fix kprobe event handling
Date: Sat, 03 Oct 2026 14:10:05 +0200
Message-ID: <20261003141005.12345-1-mhiramat@kernel.org>
In-Reply-To: <cover.20261003.ftrace@kernel.org>
References: <root.20261003@kernel.org> <cover.20261003.ftrace@kernel.org>
Content-Type: text/plain; charset=utf-8

This patch fixes a corner case in kprobe event handling.

Signed-off-by: Masami Hiramatsu <mhiramat@kernel.org>
---
diff --git a/kernel/trace/trace_kprobe.c b/kernel/trace/trace_kprobe.c
--- a/kernel/trace/trace_kprobe.c
+++ b/kernel/trace/trace_kprobe.c
@@ -100,6 +100,7 @@ static int trace_kprobe_create(...)
+	/* check return value */
`

	parsed, err := ParseEmail([]byte(raw))
	if err != nil {
		t.Fatalf("ParseEmail failed: %v", err)
	}

	if parsed.MessageID != "20261003141005.12345-1-mhiramat@kernel.org" {
		t.Errorf("unexpected message_id: %s", parsed.MessageID)
	}
	if parsed.FromName != "Masami Hiramatsu" {
		t.Errorf("unexpected from_name: %s", parsed.FromName)
	}
	if parsed.FromAddr != "mhiramat@kernel.org" {
		t.Errorf("unexpected from_addr: %s", parsed.FromAddr)
	}
	if parsed.Subject != "[PATCH] ftrace: fix kprobe event handling" {
		t.Errorf("unexpected subject: %s", parsed.Subject)
	}
	if parsed.EpochTimestamp == 0 {
		t.Errorf("epoch timestamp should not be 0")
	}
	if !parsed.IsPatch {
		t.Errorf("expected is_patch to be true")
	}
	if !strings.Contains(parsed.BodyPlain, "diff --git") {
		t.Errorf("expected body_plain to contain patch")
	}
}

func TestExtractMsgIDs(t *testing.T) {
	header := "<id1@kernel.org> <id2@kernel.org>"
	ids := ExtractMsgIDs(header)
	if len(ids) != 2 || ids[0] != "id1@kernel.org" || ids[1] != "id2@kernel.org" {
		t.Errorf("unexpected ids: %v", ids)
	}
}

func TestBuildReplyRecipients(t *testing.T) {
	to, cc := BuildReplyRecipients(
		"author@kernel.org",
		"mhiramat@kernel.org, list@vger.kernel.org",
		"reviewer@kernel.org",
		"mhiramat@kernel.org",
	)

	if to != "author@kernel.org" {
		t.Errorf("expected to=author@kernel.org, got %s", to)
	}
	// mhiramat should be excluded from Cc, list and reviewer included
	if strings.Contains(cc, "mhiramat@kernel.org") {
		t.Errorf("Cc should exclude self email: %s", cc)
	}
	if !strings.Contains(cc, "list@vger.kernel.org") {
		t.Errorf("Cc should include list: %s", cc)
	}
	if !strings.Contains(cc, "reviewer@kernel.org") {
		t.Errorf("Cc should include reviewer: %s", cc)
	}
}
