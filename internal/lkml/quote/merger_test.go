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

func TestWrapCommentText(t *testing.T) {
	t.Run("long_prose", func(t *testing.T) {
		longComment := "This is a dummy long review comment on the patch diff that exceeds seventy-five columns and should be automatically wrapped around column 75 when composing a reply mail."
		wrapped := WrapCommentText(longComment, 75)
		lines := strings.Split(wrapped, "\n")
		if len(lines) < 3 {
			t.Fatalf("WrapCommentText line count = %d, want >= 3:\n%s", len(lines), wrapped)
		}
		for i, line := range lines {
			if len(line) > 75 {
				t.Errorf("line %d len = %d, want <= 75: %q", i+1, len(line), line)
			}
		}
		if got := strings.Join(strings.Fields(wrapped), " "); got != longComment {
			t.Errorf("wrapped words = %q, want %q", got, longComment)
		}
	})

	t.Run("spaceless_token", func(t *testing.T) {
		dummy := strings.Repeat("a", 160)
		wrapped := WrapCommentText(dummy, 75)
		lines := strings.Split(wrapped, "\n")
		if len(lines) != 3 {
			t.Fatalf("WrapCommentText line count = %d, want 3:\n%s", len(lines), wrapped)
		}
		if len(lines[0]) != 75 || len(lines[1]) != 75 || len(lines[2]) != 10 {
			t.Errorf("line lengths = (%d, %d, %d), want (75, 75, 10)", len(lines[0]), len(lines[1]), len(lines[2]))
		}
	})

	t.Run("long_url", func(t *testing.T) {
		url := "https://lore.kernel.org/all/20261104135327.23dbae14572e88b3@example.kernel.org/T/#u"
		comment := "Please refer to the previous thread at " + url + " for full details on this regression."
		wrapped := WrapCommentText(comment, 75)
		if !strings.Contains(wrapped, "\n"+url+"\n") {
			t.Errorf("WrapCommentText() = %q, want URL on its own line %q", wrapped, url)
		}
	})

	t.Run("fenced_code_and_quotes", func(t *testing.T) {
		codeLine := "	if (unlikely(kprobe_running() && !arch_kprobe_allow_reentry(p, regs))) return 0;"
		fenced := "Here is an example:\n```\n" + codeLine + "\n```"
		wrapped := WrapCommentText(fenced, 75)
		if !strings.Contains(wrapped, codeLine) {
			t.Errorf("WrapCommentText() = %q, want unwrapped fenced code line %q", wrapped, codeLine)
		}
	})
}

func TestMergePatchReviewQuoteWrapsLongComments(t *testing.T) {
	diffText := `diff --git a/kernel/kprobes.c b/kernel/kprobes.c
--- a/kernel/kprobes.c
+++ b/kernel/kprobes.c
@@ -10,5 +10,6 @@ static int register_kprobe_test(void)
 	int ret = 0;
 
+	ret = check_kprobe_address_safe();
 	return ret;
 }
`
	parsed := patch.ParsePatch(diffText)
	longComment := "This is a dummy long comment on the diff line to verify that when composing the reply mail, MergePatchReviewQuote automatically wraps inline diff comments around 75 columns instead of emitting a single long unwrapped line."
	longGeneral := "Thank you for working on these KUnit test cases for kprobes regressions, please find my detailed comments inline below."

	notes := []InlineNote{
		{
			File:   "kernel/kprobes.c",
			LineNo: 12,
			Side:   "new",
			Text:   longComment,
		},
	}

	result := MergePatchReviewQuote(
		"Thu, 05 Nov 2026 21:00:00 +0900",
		"Kernel Developer",
		"dev@kernel.org",
		diffText,
		parsed,
		notes,
		longGeneral,
		"Kernel Developer",
	)

	for i, line := range strings.Split(result, "\n") {
		if strings.HasPrefix(line, ">") || strings.HasPrefix(line, "On ") {
			continue
		}
		if len(line) > 75 {
			t.Errorf("non-quoted line %d len = %d, want <= 75: %q", i+1, len(line), line)
		}
	}
}
