package b4

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"undined-go/internal/config"
	"undined-go/internal/storage/database"
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

func TestIsValidGitRef(t *testing.T) {
	validRefs := []string{
		"main",
		"review/patch-1",
		"v6.10-rc1",
		"HEAD",
		"4ac73dd24e3123cdcdb3e938dc7bd74be66a3f77",
		"feature+testing",
		"HEAD~1",
	}
	for _, ref := range validRefs {
		if !IsValidGitRef(ref) {
			t.Errorf("expected %q to be valid git ref, got false", ref)
		}
	}

	invalidRefs := []string{
		"-b",
		"--orphan",
		"--config=core.editor=calc",
		"-D",
		"",
		"   ",
		"branch with space",
		"branch;rm -rf",
		"branch$(whoami)",
	}
	for _, ref := range invalidRefs {
		if IsValidGitRef(ref) {
			t.Errorf("expected %q to be invalid git ref, got true", ref)
		}
	}
}

func TestRecordSeriesCommits(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test.db")
	db, err := database.NewDatabase(dbPath)
	if err != nil {
		t.Fatalf("failed to create database: %v", err)
	}

	folder, err := db.GetOrCreateFolder("INBOX", true)
	if err != nil {
		t.Fatalf("failed to create folder: %v", err)
	}

	msg1Header := []byte("Message-ID: <patch-1@example.com>\nSubject: [PATCH 1/2] ftrace: update kprobes\nFrom: dev@example.com\nDate: Mon, 1 Jan 2026 00:00:00 +0000\n\n")
	msg2Header := []byte("Message-ID: <patch-2@example.com>\nSubject: [PATCH 2/2] ftrace: add self test\nFrom: dev@example.com\nDate: Mon, 1 Jan 2026 00:00:01 +0000\nIn-Reply-To: <patch-1@example.com>\nReferences: <patch-1@example.com>\n\n")

	if _, err := db.InsertHeaderSummary(folder.ID, 1, msg1Header, false, ""); err != nil {
		t.Fatalf("failed to insert msg1: %v", err)
	}
	if _, err := db.InsertHeaderSummary(folder.ID, 2, msg2Header, false, ""); err != nil {
		t.Fatalf("failed to insert msg2: %v", err)
	}

	gitDir := filepath.Join(tmpDir, "repo")
	if err := os.MkdirAll(gitDir, 0755); err != nil {
		t.Fatalf("failed to create git dir: %v", err)
	}

	runGit := func(args ...string) string {
		cmd := exec.Command("git", append([]string{"-C", gitDir}, args...)...)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v failed: %v, out: %s", args, err, string(out))
		}
		return strings.TrimSpace(string(out))
	}

	runGit("init")
	runGit("config", "user.name", "Kernel Developer")
	runGit("config", "user.email", "dev@example.com")

	runGit("commit", "--allow-empty", "-m", "ftrace: update kprobes\n\nMessage-ID: <patch-1@example.com>")
	hash1 := runGit("rev-parse", "HEAD")

	runGit("commit", "--allow-empty", "-m", "ftrace: add self test\n\nMessage-ID: <patch-2@example.com>")
	hash2 := runGit("rev-parse", "HEAD")

	runner := NewB4Runner(&config.Config{}, nil, db)
	appliedCommits := []string{hash1, hash2}

	matched := runner.RecordSeriesCommits("patch-1@example.com", gitDir, appliedCommits)
	if matched != hash1 {
		t.Errorf("expected matched commit for patch-1 to be %s, got %s", hash1, matched)
	}

	commit1, err := db.GetReviewCommit("patch-1@example.com")
	if err != nil || commit1 != hash1 {
		t.Errorf("expected review commit for patch-1 to be %s, got %s (err: %v)", hash1, commit1, err)
	}

	commit2, err := db.GetReviewCommit("patch-2@example.com")
	if err != nil || commit2 != hash2 {
		t.Errorf("expected review commit for patch-2 to be %s, got %s (err: %v)", hash2, commit2, err)
	}

	m1, err := db.GetMessageByMsgID("patch-1@example.com")
	if err != nil || m1.ReviewCommitID != hash1 {
		t.Errorf("expected m1.ReviewCommitID to be %s, got %s (err: %v)", hash1, m1.ReviewCommitID, err)
	}
	m2, err := db.GetMessageByMsgID("patch-2@example.com")
	if err != nil || m2.ReviewCommitID != hash2 {
		t.Errorf("expected m2.ReviewCommitID to be %s, got %s (err: %v)", hash2, m2.ReviewCommitID, err)
	}
}

func TestRecordSeriesCommits_SubjectAndIndexMatching(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test.db")
	db, err := database.NewDatabase(dbPath)
	if err != nil {
		t.Fatalf("failed to create database: %v", err)
	}

	folder, err := db.GetOrCreateFolder("INBOX", true)
	if err != nil {
		t.Fatalf("failed to create folder: %v", err)
	}

	msg1Header := []byte("Message-ID: <patch-a@example.com>\nSubject: [PATCH 1/2] tracing: fix bug A\nFrom: dev@example.com\nDate: Mon, 1 Jan 2026 00:00:00 +0000\n\n")
	msg2Header := []byte("Message-ID: <patch-b@example.com>\nSubject: [PATCH 2/2] tracing: fix bug B\nFrom: dev@example.com\nDate: Mon, 1 Jan 2026 00:00:01 +0000\nIn-Reply-To: <patch-a@example.com>\nReferences: <patch-a@example.com>\n\n")

	if _, err := db.InsertHeaderSummary(folder.ID, 1, msg1Header, false, ""); err != nil {
		t.Fatalf("failed to insert msg1: %v", err)
	}
	if _, err := db.InsertHeaderSummary(folder.ID, 2, msg2Header, false, ""); err != nil {
		t.Fatalf("failed to insert msg2: %v", err)
	}

	gitDir := filepath.Join(tmpDir, "repo")
	if err := os.MkdirAll(gitDir, 0755); err != nil {
		t.Fatalf("failed to create git dir: %v", err)
	}

	runGit := func(args ...string) string {
		cmd := exec.Command("git", append([]string{"-C", gitDir}, args...)...)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v failed: %v, out: %s", args, err, string(out))
		}
		return strings.TrimSpace(string(out))
	}

	runGit("init")
	runGit("config", "user.name", "Kernel Developer")
	runGit("config", "user.email", "dev@example.com")

	runGit("commit", "--allow-empty", "-m", "tracing: fix bug A")
	hash1 := runGit("rev-parse", "HEAD")

	runGit("commit", "--allow-empty", "-m", "tracing: fix bug B")
	hash2 := runGit("rev-parse", "HEAD")

	runner := NewB4Runner(&config.Config{}, nil, db)
	appliedCommits := []string{hash1, hash2}

	matched := runner.RecordSeriesCommits("patch-a@example.com", gitDir, appliedCommits)
	if matched != hash1 {
		t.Errorf("expected matched commit for patch-a to be %s, got %s", hash1, matched)
	}

	c1, err := db.GetReviewCommit("patch-a@example.com")
	if err != nil || c1 != hash1 {
		t.Errorf("expected review commit for patch-a to be %s, got %s", hash1, c1)
	}

	c2, err := db.GetReviewCommit("patch-b@example.com")
	if err != nil || c2 != hash2 {
		t.Errorf("expected review commit for patch-b to be %s, got %s", hash2, c2)
	}
}

func TestApplyPatch_LocalFallbackWithGitSignature(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test.db")
	db, err := database.NewDatabase(dbPath)
	if err != nil {
		t.Fatalf("failed to create database: %v", err)
	}

	folder, err := db.GetOrCreateFolder("INBOX", true)
	if err != nil {
		t.Fatalf("failed to create folder: %v", err)
	}

	patchBody := "From: Kernel Developer <dev@example.com>\n" +
		"Subject: [PATCH] kernel: add custom trace feature\n" +
		"Date: Sat, 24 Oct 2026 12:00:00 +0000\n" +
		"Message-ID: <patch-sig-test@example.com>\n\n" +
		"This adds custom trace feature.\n\n" +
		"Signed-off-by: Kernel Developer <dev@example.com>\n" +
		"---\n" +
		" test_file.c | 1 +\n" +
		" 1 file changed, 1 insertion(+)\n\n" +
		"diff --git a/test_file.c b/test_file.c\n" +
		"new file mode 100644\n" +
		"--- /dev/null\n" +
		"+++ b/test_file.c\n" +
		"@@ -0,0 +1,1 @@\n" +
		"+/* custom trace feature */\n" +
		"-- \n" +
		"2.43.0\n"

	msgHeader := []byte("Message-ID: <patch-sig-test@example.com>\n" +
		"Subject: [PATCH] kernel: add custom trace feature\n" +
		"From: Kernel Developer <dev@example.com>\n" +
		"Date: Sat, 24 Oct 2026 12:00:00 +0000\n\n")

	msgID, err := db.InsertHeaderSummary(folder.ID, 1, msgHeader, false, "")
	if err != nil {
		t.Fatalf("failed to insert header: %v", err)
	}
	if err := db.HydrateMessageBody(msgID, "", patchBody, true); err != nil {
		t.Fatalf("failed to hydrate body: %v", err)
	}

	gitDir := filepath.Join(tmpDir, "repo")
	if err := os.MkdirAll(gitDir, 0755); err != nil {
		t.Fatalf("failed to create git dir: %v", err)
	}

	runGit := func(args ...string) string {
		cmd := exec.Command("git", append([]string{"-C", gitDir}, args...)...)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v failed: %v, out: %s", args, err, string(out))
		}
		return strings.TrimSpace(string(out))
	}

	runGit("init")
	runGit("config", "user.name", "Kernel Developer")
	runGit("config", "user.email", "dev@example.com")
	runGit("commit", "--allow-empty", "-m", "Initial commit")

	// Set B4Cmd to 'false' so b4 lore fetch fails, triggering local database fallback
	cfg := &config.Config{
		LKML: config.LKMLConfig{
			B4Cmd: "false",
		},
	}
	runner := NewB4Runner(cfg, nil, db)

	res, err := runner.ApplyPatch("patch-sig-test@example.com", gitDir, "review/trace-feature", "")
	if err != nil {
		t.Fatalf("ApplyPatch returned unexpected error: %v", err)
	}
	if !res.Success {
		t.Fatalf("expected ApplyPatch success=true with local fallback, got false; stderr=%s, stdout=%s", res.Stderr, res.Stdout)
	}
	if res.CommitID == "" {
		t.Fatalf("expected CommitID to be populated")
	}

	// Verify commit was created on the repository
	headSubject := runGit("log", "-1", "--format=%s")
	if headSubject != "kernel: add custom trace feature" {
		t.Errorf("unexpected commit subject: %q", headSubject)
	}

	// Verify file was created
	content, err := os.ReadFile(filepath.Join(gitDir, "test_file.c"))
	if err != nil || !strings.Contains(string(content), "/* custom trace feature */") {
		t.Errorf("expected test_file.c to be created by git am: %v, content: %q", err, string(content))
	}

	// Verify database recorded review commit
	recordedCommit, err := db.GetReviewCommit("patch-sig-test@example.com")
	if err != nil || recordedCommit != res.CommitID {
		t.Errorf("expected recorded commit %s, got %s (err: %v)", res.CommitID, recordedCommit, err)
	}
}
