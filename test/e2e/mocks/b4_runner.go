package mocks

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// MockB4Runner generates an executable mock b4 script and records its invocations.
type MockB4Runner struct {
	ScriptPath string
	LogPath    string
	tempDir    string
	mu         sync.Mutex
}

// NewMockB4Runner generates an executable b4 mock script in a temporary directory.
func NewMockB4Runner() (*MockB4Runner, error) {
	tmp, err := os.MkdirTemp("", "undined-mock-b4-*")
	if err != nil {
		return nil, fmt.Errorf("failed to create temp dir for mock b4: %w", err)
	}

	scriptPath := filepath.Join(tmp, "b4")
	logPath := filepath.Join(tmp, "b4_invocations.log")

	scriptContent := fmt.Sprintf(`#!/bin/sh
echo "$@" >> %q

out_target=""
prev_arg=""
for arg in "$@"; do
  if [ "$prev_arg" = "-o" ]; then
    out_target="$arg"
  fi
  prev_arg="$arg"
done

case "$1" in
  am|mbox)
    if [ "$out_target" = "-" ] || [ -z "$out_target" ]; then
      cat <<'EOF'
From dev@example.com Sat Oct 24 12:00:00 2026
From: Kernel Developer <dev@example.com>
Date: Sat, 24 Oct 2026 12:00:00 +0000
Subject: [PATCH] trace/kprobes: Add support for custom event probes
Message-ID: <patch-e2e-1@kernel.org>

---
 kernel/trace/trace_kprobe.c | 1 +
 1 file changed, 1 insertion(+)

diff --git a/kernel/trace/trace_kprobe.c b/kernel/trace/trace_kprobe.c
--- a/kernel/trace/trace_kprobe.c
+++ b/kernel/trace/trace_kprobe.c
@@ -1,3 +1,4 @@
 int trace_kprobe_init(void) {
+    /* E2E test probe initialization */
     return 0;
 }
-- 
2.34.1
EOF
      exit 0
    else
      mkdir -p "$out_target"
      cat <<'EOF' > "$out_target/mock-series.mbx"
From dev@example.com Sat Oct 24 12:00:00 2026
From: Kernel Developer <dev@example.com>
Date: Sat, 24 Oct 2026 12:00:00 +0000
Subject: [PATCH] trace/kprobes: Add support for custom event probes
Message-ID: <patch-e2e-1@kernel.org>

---
 kernel/trace/trace_kprobe.c | 1 +
 1 file changed, 1 insertion(+)

diff --git a/kernel/trace/trace_kprobe.c b/kernel/trace/trace_kprobe.c
--- a/kernel/trace/trace_kprobe.c
+++ b/kernel/trace/trace_kprobe.c
@@ -1,3 +1,4 @@
 int trace_kprobe_init(void) {
+    /* E2E test probe initialization */
     return 0;
 }
-- 
2.34.1
EOF
      echo "Saved to $out_target/mock-series.mbx"
      exit 0
    fi
    ;;
  diff)
    cat <<'EOF'
--- a/kernel/trace/trace_kprobe.c
+++ b/kernel/trace/trace_kprobe.c
@@ -1,3 +1,4 @@
 int trace_kprobe_init(void) {
+    /* E2E test probe initialization */
     return 0;
 }
EOF
    exit 0
    ;;
  *)
    echo "mock b4 received: $@"
    exit 0
    ;;
esac
`, logPath)

	if err := os.WriteFile(scriptPath, []byte(scriptContent), 0755); err != nil {
		_ = os.RemoveAll(tmp)
		return nil, fmt.Errorf("failed to write mock b4 script: %w", err)
	}

	return &MockB4Runner{
		ScriptPath: scriptPath,
		LogPath:    logPath,
		tempDir:    tmp,
	}, nil
}

// GetInvocations returns all command-line invocations made to the mock b4 script.
func (b *MockB4Runner) GetInvocations() []string {
	b.mu.Lock()
	defer b.mu.Unlock()

	data, err := os.ReadFile(b.LogPath)
	if err != nil {
		return nil
	}
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	var res []string
	for _, l := range lines {
		if trimmed := strings.TrimSpace(l); trimmed != "" {
			res = append(res, trimmed)
		}
	}
	return res
}

// Cleanup removes the mock b4 script and log files.
func (b *MockB4Runner) Cleanup() {
	b.mu.Lock()
	defer b.mu.Unlock()
	_ = os.RemoveAll(b.tempDir)
}
