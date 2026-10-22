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
