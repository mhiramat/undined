package b4

import (
	"testing"
)

func TestExtractSeriesVersion(t *testing.T) {
	tests := []struct {
		subject  string
		expected string
	}{
		{"[PATCH v3 1/4] ftrace: update kprobes", "v3"},
		{"[PATCH 1/2] fix memory leak", ""},
		{"[PATCH V2] perf probe improvements", "v2"},
		{"Re: [PATCH v5 0/3] new feature", "v5"},
	}

	for _, tc := range tests {
		got := ExtractSeriesVersion(tc.subject)
		if got != tc.expected {
			t.Errorf("for subject %q, expected %q, got %q", tc.subject, tc.expected, got)
		}
	}
}

func TestCleanSeriesTitle(t *testing.T) {
	tests := []struct {
		subject  string
		expected string
	}{
		{"[PATCH v2 1/3] ftrace: update docs", "ftrace: update docs"},
		{"Re: [PATCH] mm: improve page alloc", "mm: improve page alloc"},
		{"Fwd: [PATCH v4] net: add filter", "net: add filter"},
	}

	for _, tc := range tests {
		got := CleanSeriesTitle(tc.subject)
		if got != tc.expected {
			t.Errorf("for subject %q, expected %q, got %q", tc.subject, tc.expected, got)
		}
	}
}

func TestGenerateBranchName(t *testing.T) {
	tests := []struct {
		subject  string
		expected string
	}{
		{"[PATCH v2 1/3] selftests/ftrace: fix path", "review/selftests-ftrace-fix-path-v2"},
		{"[PATCH] mm/slab: optimize alloc", "review/mm-slab-optimize-alloc"},
	}

	for _, tc := range tests {
		got := GenerateBranchName(tc.subject)
		if got != tc.expected {
			t.Errorf("for subject %q, expected %q, got %q", tc.subject, tc.expected, got)
		}
	}
}

func TestSplitMbox(t *testing.T) {
	mboxData := []byte(`From dev@kernel.org Mon Oct 12 10:00:00 2026
Subject: Message 1
Message-ID: <msg-1@kernel.org>

Body 1

From dev@kernel.org Mon Oct 12 10:05:00 2026
Subject: Message 2
Message-ID: <msg-2@kernel.org>

Body 2
`)

	msgs := SplitMbox(mboxData)
	if len(msgs) != 2 {
		t.Fatalf("expected 2 messages, got %d", len(msgs))
	}
}
