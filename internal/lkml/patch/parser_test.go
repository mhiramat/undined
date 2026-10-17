package patch

import (
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
