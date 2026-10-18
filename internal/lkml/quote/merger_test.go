package quote

import (
	"strings"
	"testing"

	"undined-go/internal/lkml/patch"
)

func TestMergePatchReviewQuote(t *testing.T) {
	diffText := `diff --git a/kernel/trace/ftrace.c b/kernel/trace/ftrace.c
--- a/kernel/trace/ftrace.c
+++ b/kernel/trace/ftrace.c
@@ -10,6 +10,7 @@ static int ftrace_init(void)
 	int ret = 0;
 
+	pr_info("Starting ftrace\n");
 	return ret;
 }
`
	parsed := patch.ParsePatch(diffText)

	notes := []InlineNote{
		{
			File:   "kernel/trace/ftrace.c",
			LineNo: 12,
			Side:   "new",
			Text:   "Does this print every time or only once?",
		},
	}

	result := MergePatchReviewQuote(
		"Sat, 03 Oct 2026 14:10:05 +0200",
		"Kernel Developer",
		"developer@kernel.org",
		diffText,
		parsed,
		notes,
		"Thanks for the patch, please see my comment below.",
		"Kernel Developer\nExample Org",
		2,
	)

	// Verify Attribution
	if !strings.Contains(result, "On Sat, 03 Oct 2026 14:10:05 +0200, Kernel Developer <developer@kernel.org> wrote:") {
		t.Errorf("missing or incorrect attribution header: %s", result)
	}

	// Verify General Comment
	if !strings.Contains(result, "Thanks for the patch, please see my comment below.") {
		t.Errorf("missing general comment: %s", result)
	}

	// Verify Comment is inserted below line 12
	if !strings.Contains(result, "> +	pr_info(\"Starting ftrace\\n\");\n\nDoes this print every time or only once?\n\n") {
		t.Errorf("inline comment not properly formatted:\n%s", result)
	}

	// Verify Signature
	if !strings.Contains(result, "-- \nKernel Developer\nExample Org") {
		t.Errorf("missing signature: %s", result)
	}
}

func TestMergeQuoteNoNotes(t *testing.T) {
	origBody := "Hello world\nThis is a simple message.\n"
	result := MergePatchReviewQuote(
		"Mon, 12 Oct 2026 10:00:00 +0000",
		"Dev",
		"dev@example.com",
		origBody,
		nil,
		nil,
		"",
		"My Signature",
	)

	if !strings.Contains(result, "> Hello world") {
		t.Errorf("expected full quote of original body: %s", result)
	}
	if !strings.Contains(result, "-- \nMy Signature") {
		t.Errorf("expected signature: %s", result)
	}
}
