package patch

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestParsePatch(t *testing.T) {
	diffText := `This is the commit preamble description.

Signed-off-by: Developer <dev@kernel.org>
---
diff --git a/kernel/trace/trace_kprobe.c b/kernel/trace/trace_kprobe.c
index 1234567..89abcdef 100644
--- a/kernel/trace/trace_kprobe.c
+++ b/kernel/trace/trace_kprobe.c
@@ -50,6 +50,7 @@ static int kprobe_init(void)
 	int ret;
 
+	pr_info("Initializing kprobe\n");
 	ret = register_tracepoint();
 	return ret;
 }
-- 
2.43.0
`

	parsed := ParsePatch(diffText)
	if !parsed.IsPatch {
		t.Fatalf("expected is_patch to be true")
	}
	if parsed.Preamble != "This is the commit preamble description.\n\nSigned-off-by: Developer <dev@kernel.org>\n---" {
		t.Errorf("unexpected preamble: %s", parsed.Preamble)
	}
	if len(parsed.Files) != 1 {
		t.Fatalf("expected 1 diff file, got %d", len(parsed.Files))
	}

	f := parsed.Files[0]
	if f.OldPath != "kernel/trace/trace_kprobe.c" || f.NewPath != "kernel/trace/trace_kprobe.c" {
		t.Errorf("unexpected file paths: %s -> %s", f.OldPath, f.NewPath)
	}
	if len(f.Hunks) != 1 {
		t.Fatalf("expected 1 hunk, got %d", len(f.Hunks))
	}

	h := f.Hunks[0]
	if h.OldStart != 50 || h.NewStart != 50 {
		t.Errorf("unexpected hunk line starts: %d -> %d", h.OldStart, h.NewStart)
	}
	if len(h.PairedRows) == 0 {
		t.Fatalf("expected paired rows to be populated")
	}

	// Verify that addition row has left cell empty and right cell with line 52
	foundAdd := false
	for _, row := range h.PairedRows {
		if row.Right.Type == "add" {
			foundAdd = true
			if row.Left.Type != "empty" {
				t.Errorf("expected left cell to be empty for add line")
			}
			if row.Right.Content != "\tpr_info(\"Initializing kprobe\\n\");" {
				t.Errorf("unexpected right cell content: %q", row.Right.Content)
			}
		}
	}
	if !foundAdd {
		t.Errorf("expected to find add row in paired rows")
	}
}

func TestFileMatches(t *testing.T) {
	if !FileMatches("a/kernel/trace/ftrace.c", "kernel/trace/ftrace.c") {
		t.Errorf("expected match for a/ prefix")
	}
	if !FileMatches("ftrace.c", "kernel/trace/ftrace.c") {
		t.Errorf("expected suffix match")
	}
	if FileMatches("other.c", "kernel/trace/ftrace.c") {
		t.Errorf("unexpected match for different file")
	}
}

func TestParsePatch_CorruptedUnicodeInDiff(t *testing.T) {
	// Diff lines with non-breaking space (\u00a0) and en-space (\u2002) as found in webmail-corrupted patches
	diffText := `Commit preamble text.

Signed-off-by: Kernel Developer <dev@example.com>
---
diff --git a/drivers/perf/arm_cspmu/arm_cspmu.c b/drivers/perf/arm_cspmu/arm_cspmu.c
index 28df8ad4d13c..61a586d34eec 100644
--- a/drivers/perf/arm_cspmu/arm_cspmu.c
+++ b/drivers/perf/arm_cspmu/arm_cspmu.c
@@ -1115,7 +1115,7 @@ static int arm_cspmu_get_filter_idx(struct arm_cspmu *cspmu,
` + "\u00a0\u2002\u2002\u2002const struct arm_cspmu_filter_ops *ops)\n" +
		`{
-- 
2.43.0
`

	parsed := ParsePatch(diffText)
	if !parsed.IsPatch {
		t.Fatalf("expected is_patch to be true for patch with diff headers")
	}
	if !parsed.IsCorrupted {
		t.Fatalf("expected is_corrupted to be true")
	}
	if !strings.Contains(parsed.CorruptedReason, "U+00A0") {
		t.Errorf("expected corrupted_reason to mention U+00A0, got: %s", parsed.CorruptedReason)
	}

	// Verify JSON marshaling produces empty arrays rather than null for paired_rows and unified_lines
	data, err := json.Marshal(parsed)
	if err != nil {
		t.Fatalf("failed to marshal parsed diff: %v", err)
	}
	jsonStr := string(data)
	if strings.Contains(jsonStr, `"paired_rows":null`) {
		t.Errorf("paired_rows serialized as null in JSON: %s", jsonStr)
	}
	if strings.Contains(jsonStr, `"unified_lines":null`) {
		t.Errorf("unified_lines serialized as null in JSON: %s", jsonStr)
	}
	if strings.Contains(jsonStr, `"hunks":null`) {
		t.Errorf("hunks serialized as null in JSON: %s", jsonStr)
	}
}

func TestParsePatch_InlineSnippetFollowedByDiscussion(t *testing.T) {
	// Sample review reply containing an inline diff snippet followed by discussion
	text := `On Mon, 26 Oct 2026, Reviewer wrote:
> I noticed an issue in parse_pred().
> Here is what happened:

Maybe something like this would be cleaner:

diff --git a/kernel/trace/trace_events_filter.c b/kernel/trace/trace_events_filter.c
--- a/kernel/trace/trace_events_filter.c
+++ b/kernel/trace/trace_events_filter.c
@@ -628,6 +628,7 @@ static int parse_pred(const char *str, void *data,
 	int ret;

+	/* Reset predicate state */
 	clear_pred(pred);
 	return 0;
 }

> What do you think about handling the error return differently?

I agree that the error return could be improved.
Let's make sure we also update the selftests.

-- 
Kernel Developer <dev@example.com>
`

	parsed := ParsePatch(text)
	if parsed.IsPatch {
		t.Fatalf("expected IsPatch=false for discussion email with inline diff snippet")
	}
	if !parsed.BodyContinuesAfter {
		t.Fatalf("expected BodyContinuesAfter=true")
	}
	if len(parsed.Files) != 1 {
		t.Fatalf("expected 1 parsed file, got %d", len(parsed.Files))
	}
	if !strings.Contains(parsed.Postscript, "What do you think about handling the error return differently?") {
		t.Errorf("expected Postscript to contain discussion following the diff, got: %q", parsed.Postscript)
	}
	if !strings.Contains(parsed.Postscript, "Kernel Developer <dev@example.com>") {
		t.Errorf("expected Postscript to contain signature, got: %q", parsed.Postscript)
	}
}

func TestHasBodyAfterDiff(t *testing.T) {
	tests := []struct {
		name     string
		lines    []string
		expected bool
	}{
		{
			name:     "empty",
			lines:    []string{},
			expected: false,
		},
		{
			name:     "standard git signature",
			lines:    []string{"-- ", "2.43.0"},
			expected: false,
		},
		{
			name:     "signature only",
			lines:    []string{"--", "Kernel Developer <dev@example.com>"},
			expected: false,
		},
		{
			name:     "quoted line",
			lines:    []string{"> What do you think?"},
			expected: true,
		},
		{
			name:     "substantive text",
			lines:    []string{"Let's verify this in the test suite."},
			expected: true,
		},
		{
			name:     "whitespace and dashes only",
			lines:    []string{"", "  ", "-- ", "2.43.0"},
			expected: false,
		},
		{
			name:     "git version without separator",
			lines:    []string{"2.43.0"},
			expected: false,
		},
		{
			name:     "git version with prefix without separator",
			lines:    []string{"git version 2.43.0"},
			expected: false,
		},
		{
			name:     "three dashes signature",
			lines:    []string{"---", "Kernel Developer <dev@example.com>"},
			expected: false,
		},
		{
			name:     "three dashes with space signature",
			lines:    []string{"--- ", "Kernel Developer"},
			expected: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := HasBodyAfterDiff(tc.lines)
			if got != tc.expected {
				t.Errorf("expected %v, got %v for lines: %v", tc.expected, got, tc.lines)
			}
		})
	}
}

func TestParsePatch_UnicodeInPreambleNotCorrupted(t *testing.T) {
	// Unicode in preamble / commit message (e.g. author name with umlauts or accents)
	diffText := `Subject: [PATCH] perf/arm: Fix filter ops

This fixes an issue reported by Björn <dev@kernel.org>.

Signed-off-by: Kernel Developer <dev@example.com>
---
diff --git a/drivers/perf/arm_cspmu/arm_cspmu.c b/drivers/perf/arm_cspmu/arm_cspmu.c
index 28df8ad4d13c..61a586d34eec 100644
--- a/drivers/perf/arm_cspmu/arm_cspmu.c
+++ b/drivers/perf/arm_cspmu/arm_cspmu.c
@@ -1115,7 +1115,7 @@ static int arm_cspmu_get_filter_idx(struct arm_cspmu *cspmu,
 	const struct arm_cspmu_filter_ops *ops)
 {
-- 
2.43.0
`

	parsed := ParsePatch(diffText)
	if !parsed.IsPatch {
		t.Fatalf("expected is_patch to be true")
	}
	if parsed.IsCorrupted {
		t.Fatalf("preamble unicode should not mark patch as corrupted: %s", parsed.CorruptedReason)
	}
	if parsed.CorruptedReason != "" {
		t.Errorf("expected empty corrupted_reason, got: %s", parsed.CorruptedReason)
	}
}

func TestParsePatch_EmptyHunkPairedRowsNotNull(t *testing.T) {
	diffText := `Commit preamble text.

Signed-off-by: Kernel Developer <dev@example.com>
---
diff --git a/test.c b/test.c
--- a/test.c
+++ b/test.c
@@ -1,1 +1,1 @@
-- 
2.43.0
`

	parsed := ParsePatch(diffText)
	data, err := json.Marshal(parsed)
	if err != nil {
		t.Fatalf("failed to marshal: %v", err)
	}
	jsonStr := string(data)
	if strings.Contains(jsonStr, `"paired_rows":null`) {
		t.Errorf("expected paired_rows:[], got null in: %s", jsonStr)
	}
	if strings.Contains(jsonStr, `"unified_lines":null`) {
		t.Errorf("expected unified_lines:[], got null in: %s", jsonStr)
	}
}

func TestParsePatch_GitAppendedSignature(t *testing.T) {
	patchWithCRLF := "Subject: [PATCH] tracing: Fix probe handling\r\n\r\n" +
		"Commit message preamble.\r\n\r\n" +
		"Signed-off-by: Kernel Developer <dev@example.com>\r\n---\r\n" +
		"diff --git a/kernel/trace/trace_kprobe.c b/kernel/trace/trace_kprobe.c\r\n" +
		"--- a/kernel/trace/trace_kprobe.c\r\n" +
		"+++ b/kernel/trace/trace_kprobe.c\r\n" +
		"@@ -50,6 +50,7 @@ static int kprobe_init(void)\r\n" +
		"  \tint ret;\r\n" +
		" \r\n" +
		"+\tpr_info(\"probe init\\n\");\r\n" +
		" \treturn 0;\r\n" +
		" }\r\n" +
		"-- \r\n" +
		"2.43.0\r\n"

	parsed := ParsePatch(patchWithCRLF)
	if !parsed.IsPatch {
		t.Fatalf("expected IsPatch=true for patch email sent with git and CRLF line endings")
	}
	if parsed.BodyContinuesAfter {
		t.Fatalf("expected BodyContinuesAfter=false for git version signature")
	}
	if len(parsed.Files) != 1 {
		t.Fatalf("expected 1 file, got %d", len(parsed.Files))
	}
	if len(parsed.Files[0].Hunks) != 1 {
		t.Fatalf("expected 1 hunk, got %d", len(parsed.Files[0].Hunks))
	}
	// Verify that the signature was not sucked into the hunk as a deletion line
	for _, row := range parsed.Files[0].Hunks[0].PairedRows {
		if strings.Contains(row.Left.Content, "--") || strings.Contains(row.Right.Content, "--") {
			t.Errorf("diff hunk should not contain signature separator: left=%q right=%q", row.Left.Content, row.Right.Content)
		}
	}
	if !strings.Contains(parsed.Postscript, "2.43.0") {
		t.Errorf("expected Postscript to contain git version, got %q", parsed.Postscript)
	}

	// Test with author signature below patch
	patchWithAuthorSig := `Subject: [PATCH] mm: optimize slab
Signed-off-by: Kernel Developer <dev@example.com>
---
diff --git a/mm/slab.c b/mm/slab.c
--- a/mm/slab.c
+++ b/mm/slab.c
@@ -10,3 +10,4 @@
 int init_slab(void) {
+    return 1;
 }
-- 
Kernel Developer <dev@example.com>
`
	parsedAuthor := ParsePatch(patchWithAuthorSig)
	if !parsedAuthor.IsPatch {
		t.Fatalf("expected IsPatch=true with author signature below patch")
	}
	if parsedAuthor.BodyContinuesAfter {
		t.Fatalf("expected BodyContinuesAfter=false with author signature below patch")
	}

	// Test with bare git version line below patch (no separator)
	patchWithBareVersion := `Subject: [PATCH] net: filter fix
---
diff --git a/net/core/filter.c b/net/core/filter.c
--- a/net/core/filter.c
+++ b/net/core/filter.c
@@ -10,3 +10,4 @@
 int filter(void) {
+    return 0;
 }
2.43.0
`
	parsedBare := ParsePatch(patchWithBareVersion)
	if !parsedBare.IsPatch {
		t.Fatalf("expected IsPatch=true with bare git version below patch")
	}
	if parsedBare.BodyContinuesAfter {
		t.Fatalf("expected BodyContinuesAfter=false with bare git version below patch")
	}
}
