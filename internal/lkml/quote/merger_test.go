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

func TestMergePatchReviewQuoteDescriptionOnly(t *testing.T) {
	patchEmail := `ftrace: improve kprobe handling

This patch improves kprobe handling by optimizing the entry path.
It ensures that ftrace hooks do not cause recursion.

Signed-off-by: Dev <dev@example.com>
---
diff --git a/kernel/trace/ftrace.c b/kernel/trace/ftrace.c
--- a/kernel/trace/ftrace.c
+++ b/kernel/trace/ftrace.c
@@ -10,3 +10,3 @@
-	old();
+	new();
`
	parsed := patch.ParsePatch(patchEmail)

	notes := []InlineNote{
		{
			File:   "DESCRIPTION",
			LineNo: 3,
			Side:   "new",
			Text:   "How does this guarantee no recursion?",
		},
	}

	result := MergePatchReviewQuote(
		"Sat, 03 Oct 2026 14:10:05 +0200",
		"Kernel Developer",
		"developer@kernel.org",
		patchEmail,
		parsed,
		notes,
		"",
		"My Signature",
	)

	// Attribution header
	if !strings.Contains(result, "On Sat, 03 Oct 2026 14:10:05 +0200, Kernel Developer <developer@kernel.org> wrote:") {
		t.Errorf("missing attribution: %s", result)
	}

	// Quoted line from description and comment below it
	expectedCommentBlock := "> This patch improves kprobe handling by optimizing the entry path.\n\nHow does this guarantee no recursion?\n\n"
	if !strings.Contains(result, expectedCommentBlock) {
		t.Errorf("expected comment block:\n%s\ngot:\n%s", expectedCommentBlock, result)
	}

	// Should not print diff hunks since no diff notes were present
	if strings.Contains(result, "diff --git") {
		t.Errorf("did not expect diff hunk in output: %s", result)
	}

	if !strings.Contains(result, "-- \nMy Signature") {
		t.Errorf("expected signature: %s", result)
	}
}

func TestMergePatchReviewQuoteBothDescriptionAndDiff(t *testing.T) {
	patchEmail := `ftrace: improve kprobe handling

This patch improves kprobe handling by optimizing the entry path.

Signed-off-by: Dev <dev@example.com>
---
diff --git a/kernel/trace/ftrace.c b/kernel/trace/ftrace.c
--- a/kernel/trace/ftrace.c
+++ b/kernel/trace/ftrace.c
@@ -10,3 +10,3 @@
-	old();
+	new();
`
	parsed := patch.ParsePatch(patchEmail)

	notes := []InlineNote{
		{
			File:   "DESCRIPTION",
			LineNo: 3,
			Side:   "new",
			Text:   "Nice explanation in commit message.",
		},
		{
			File:   "kernel/trace/ftrace.c",
			LineNo: 10,
			Side:   "new",
			Text:   "Check new() return value.",
		},
	}

	result := MergePatchReviewQuote(
		"Sat, 03 Oct 2026 14:10:05 +0200",
		"Kernel Developer",
		"developer@kernel.org",
		patchEmail,
		parsed,
		notes,
		"Looks mostly good.",
		"My Signature",
	)

	// General comment
	if !strings.Contains(result, "Looks mostly good.") {
		t.Errorf("missing general comment: %s", result)
	}

	// Description comment
	if !strings.Contains(result, "> This patch improves kprobe handling by optimizing the entry path.\n\nNice explanation in commit message.\n\n") {
		t.Errorf("missing description comment: %s", result)
	}

	// Diff comment
	if !strings.Contains(result, "> +	new();\n\nCheck new() return value.\n\n") {
		t.Errorf("missing diff comment: %s", result)
	}

	// Diff header
	if !strings.Contains(result, "> diff --git a/kernel/trace/ftrace.c b/kernel/trace/ftrace.c") {
		t.Errorf("missing diff header: %s", result)
	}
}

func TestMergePatchReviewQuoteNonPatchEmailDescription(t *testing.T) {
	origBody := "Question about lockless queues:\nHow do we handle reader starvation?\n"

	notes := []InlineNote{
		{
			File:   "description", // case-insensitive
			LineNo: 2,
			Side:   "new",
			Text:   "Reader starvation is mitigated by epoch counters.",
		},
	}

	result := MergePatchReviewQuote(
		"Mon, 12 Oct 2026 10:00:00 +0000",
		"Dev",
		"dev@example.com",
		origBody,
		nil,
		notes,
		"",
		"My Signature",
	)

	if !strings.Contains(result, "> How do we handle reader starvation?\n\nReader starvation is mitigated by epoch counters.\n\n") {
		t.Errorf("expected inline comment in non-patch email quote: %s", result)
	}
}

