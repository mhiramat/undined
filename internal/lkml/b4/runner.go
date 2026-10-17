// Package b4 provides subprocess orchestration for b4 mbox and git am operations.
package b4

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"

	"undined-go/internal/config"
	"undined-go/internal/constants"
	"undined-go/internal/storage/database"
	"undined-go/internal/storage/maildir"
	"undined-go/internal/storage/parser"
)

var (
	seriesVersionRegex = regexp.MustCompile(`(?i)\[[^\]]*\bv(\d+)\b[^\]]*\]`)
	slugCleanRegex     = regexp.MustCompile(`[^a-z0-9]+`)
	stripPatchRegex    = regexp.MustCompile(`(?i)^(?:\s*\[[^\]]*\]\s*|\s*(?:re|fwd):\s*)+`)
)

// ExtractSeriesVersion extracts version string (e.g. "v2") from subject.
func ExtractSeriesVersion(subject string) string {
	if subject == "" {
		return ""
	}
	if m := seriesVersionRegex.FindStringSubmatch(subject); len(m) > 1 {
		return "v" + m[1]
	}
	return ""
}

// CleanSeriesTitle strips leading [PATCH...] and Re: tags from subject.
func CleanSeriesTitle(subject string) string {
	if subject == "" {
		return ""
	}
	cleaned := strings.TrimSpace(stripPatchRegex.ReplaceAllString(subject, ""))
	if cleaned == "" {
		cleaned = strings.Trim(subject, "[] ")
	}
	return cleaned
}

// GenerateBranchName creates a standard review branch name from an email subject.
func GenerateBranchName(subject string) string {
	if subject == "" {
		return "review/patch"
	}

	version := ExtractSeriesVersion(subject)
	title := CleanSeriesTitle(subject)

	length := 32
	if len(title) < length {
		length = len(title)
	}
	sliced := strings.ToLower(title[:length])
	slug := strings.Trim(slugCleanRegex.ReplaceAllString(sliced, "-"), "-")
	if slug == "" {
		slug = "patch"
	}

	if version != "" {
		verLower := strings.ToLower(version)
		if !strings.HasSuffix(slug, "-"+verLower) && slug != verLower {
			slug = fmt.Sprintf("%s-%s", slug, verLower)
		}
	}

	return "review/" + slug
}

// B4Runner wraps the b4 and git command-line utilities.
type B4Runner struct {
	Config  *config.Config
	Maildir *maildir.MaildirStore
	DB      *database.Database
}

// NewB4Runner creates a new B4Runner instance.
func NewB4Runner(cfg *config.Config, store *maildir.MaildirStore, db *database.Database) *B4Runner {
	return &B4Runner{
		Config:  cfg,
		Maildir: store,
		DB:      db,
	}
}

// SplitMbox splits raw mbox data into individual RFC 822 email byte slices.
func SplitMbox(data []byte) [][]byte {
	var messages [][]byte
	lines := bytes.Split(data, []byte("\n"))

	var current [][]byte
	for _, line := range lines {
		if bytes.HasPrefix(line, []byte("From ")) && len(current) > 0 {
			messages = append(messages, bytes.Join(current, []byte("\n")))
			current = nil
		}
		current = append(current, line)
	}
	if len(current) > 0 {
		messages = append(messages, bytes.Join(current, []byte("\n")))
	}
	return messages
}

// ImportThread downloads the mbox stream for msgID from lore via b4 and saves emails into b4-imports.
func (b *B4Runner) ImportThread(msgIDOrURL string) ([]int64, error) {
	cleanedID := parser.CleanMsgID(msgIDOrURL)
	if cleanedID == "" {
		return nil, fmt.Errorf("message-id or url cannot be empty")
	}

	b4Cmd := b.Config.LKML.B4Cmd
	if b4Cmd == "" {
		b4Cmd = constants.DefaultB4Cmd
	}

	cmd := exec.Command(b4Cmd, "mbox", "-o", "-", cleanedID)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("b4 mbox failed: %s: %w", stderr.String(), err)
	}

	if stdout.Len() == 0 {
		return nil, nil
	}

	folder, err := b.DB.GetOrCreateFolder(constants.FolderB4Imports, false)
	if err != nil {
		return nil, fmt.Errorf("failed to get/create b4-imports folder: %w", err)
	}

	mboxMessages := SplitMbox(stdout.Bytes())
	var indexedIDs []int64

	for _, msgBytes := range mboxMessages {
		if len(bytes.TrimSpace(msgBytes)) == 0 {
			continue
		}
		path, err := b.Maildir.AddMessage(constants.FolderB4Imports, msgBytes, "")
		if err != nil {
			return nil, fmt.Errorf("failed to store mbox message: %w", err)
		}
		dbID, err := b.DB.IndexEmailFile(folder.ID, path)
		if err != nil {
			return nil, fmt.Errorf("failed to index imported message: %w", err)
		}
		indexedIDs = append(indexedIDs, dbID)
	}

	return indexedIDs, nil
}

// ApplyResult contains the outcome of applying a patch series to a git repository.
type ApplyResult struct {
	Success        bool     `json:"success"`
	ExitCode       int      `json:"exit_code"`
	Stdout         string   `json:"stdout"`
	Stderr         string   `json:"stderr"`
	Branch         string   `json:"branch,omitempty"`
	BaseCommit     string   `json:"base_commit,omitempty"`
	OriginalBranch string   `json:"original_branch,omitempty"`
	HeadBefore     string   `json:"head_before,omitempty"`
	TargetGitRepo  string   `json:"target_git_repo"`
	CommitID       string   `json:"commit_id,omitempty"`
	ReviewCommitID string   `json:"review_commit_id,omitempty"`
	AppliedCommits []string `json:"applied_commits,omitempty"`
	AMInProgress   bool     `json:"am_in_progress,omitempty"`
}

// ApplyPatch runs `b4 am -o - <msgid> | git -C <repo> am`.
func (b *B4Runner) ApplyPatch(
	msgID string,
	targetGitRepo string,
	branchName string,
	baseCommit string,
	extraArgs ...string,
) (*ApplyResult, error) {
	cleanedID := parser.CleanMsgID(msgID)
	repoPath := targetGitRepo
	if repoPath == "" {
		repoPath = b.Config.General.KernelGitDir
	}
	repoPath = config.ExpandPath(repoPath)

	gitDir := filepath.Join(repoPath, ".git")
	if fi, err := os.Stat(gitDir); err != nil || !fi.IsDir() {
		return &ApplyResult{
			Success:       false,
			ExitCode:      1,
			Stderr:        fmt.Sprintf("target directory is not a git repository: %s", repoPath),
			TargetGitRepo: repoPath,
		}, nil
	}

	// Capture original branch and HEAD before checkout/am
	var originalBranch string
	if out, err := exec.Command("git", "-C", repoPath, "rev-parse", "--abbrev-ref", "HEAD").Output(); err == nil {
		originalBranch = strings.TrimSpace(string(out))
	}
	var headBefore string
	if out, err := exec.Command("git", "-C", repoPath, "rev-parse", "HEAD").Output(); err == nil {
		headBefore = strings.TrimSpace(string(out))
	}

	// 1. Create and checkout branch if specified
	var branchLogs []string
	if strings.TrimSpace(branchName) != "" || strings.TrimSpace(baseCommit) != "" {
		args := []string{"-C", repoPath, "checkout"}
		if strings.TrimSpace(branchName) != "" {
			args = append(args, "-b", strings.TrimSpace(branchName))
		}
		if strings.TrimSpace(baseCommit) != "" {
			args = append(args, strings.TrimSpace(baseCommit))
		}

		coCmd := exec.Command("git", args...)
		out, err := coCmd.CombinedOutput()
		branchLogs = append(branchLogs, string(out))
		if err != nil {
			return &ApplyResult{
				Success:        false,
				ExitCode:       1,
				Stdout:         strings.Join(branchLogs, "\n"),
				Stderr:         fmt.Sprintf("failed to checkout branch: %s", string(out)),
				TargetGitRepo:  repoPath,
				Branch:         branchName,
				BaseCommit:     baseCommit,
				OriginalBranch: originalBranch,
				HeadBefore:     headBefore,
			}, nil
		}
	}

	// 2. Run b4 am -o -
	b4Cmd := b.Config.LKML.B4Cmd
	if b4Cmd == "" {
		b4Cmd = constants.DefaultB4Cmd
	}
	b4Args := []string{"am", "-o", "-"}
	b4Args = append(b4Args, extraArgs...)
	b4Args = append(b4Args, cleanedID)

	b4Exec := exec.Command(b4Cmd, b4Args...)
	var b4Out, b4Err bytes.Buffer
	b4Exec.Stdout = &b4Out
	b4Exec.Stderr = &b4Err

	if err := b4Exec.Run(); err != nil {
		return &ApplyResult{
			Success:        false,
			ExitCode:       1,
			Stdout:         b4Out.String(),
			Stderr:         b4Err.String(),
			TargetGitRepo:  repoPath,
			Branch:         branchName,
			BaseCommit:     baseCommit,
			OriginalBranch: originalBranch,
			HeadBefore:     headBefore,
		}, nil
	}

	// Capture HEAD before git am if not captured yet
	if headBefore == "" {
		if out, err := exec.Command("git", "-C", repoPath, "rev-parse", "HEAD").Output(); err == nil {
			headBefore = strings.TrimSpace(string(out))
		}
	}

	// 3. Pipe into git am
	gitAm := exec.Command("git", "-C", repoPath, "am")
	gitAm.Stdin = &b4Out
	var gitOut, gitErr bytes.Buffer
	gitAm.Stdout = &gitOut
	gitAm.Stderr = &gitErr

	amErr := gitAm.Run()
	combinedStdout := strings.TrimSpace(strings.Join(append(branchLogs, gitOut.String()), "\n"))
	combinedStderr := strings.TrimSpace(gitErr.String())

	if amErr != nil {
		rebaseApply := filepath.Join(gitDir, "rebase-apply")
		_, inProgress := os.Stat(rebaseApply)
		return &ApplyResult{
			Success:        false,
			ExitCode:       1,
			Stdout:         combinedStdout,
			Stderr:         combinedStderr,
			TargetGitRepo:  repoPath,
			Branch:         branchName,
			BaseCommit:     baseCommit,
			OriginalBranch: originalBranch,
			HeadBefore:     headBefore,
			AMInProgress:   inProgress == nil,
		}, nil
	}

	// 4. Capture HEAD after git am
	headAfter := ""
	if out, err := exec.Command("git", "-C", repoPath, "rev-parse", "HEAD").Output(); err == nil {
		headAfter = strings.TrimSpace(string(out))
	}

	var appliedCommits []string
	if headBefore != "" && headAfter != "" && headBefore != headAfter {
		if out, err := exec.Command("git", "-C", repoPath, "rev-list", "--reverse", fmt.Sprintf("%s..%s", headBefore, headAfter)).Output(); err == nil {
			for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
				if c := strings.TrimSpace(line); c != "" {
					appliedCommits = append(appliedCommits, c)
				}
			}
		}
	}
	if len(appliedCommits) == 0 && headAfter != "" {
		appliedCommits = append(appliedCommits, headAfter)
	}

	primaryCommit := headAfter
	_ = b.DB.SetMessageReviewCommit(cleanedID, primaryCommit)

	return &ApplyResult{
		Success:        true,
		ExitCode:       0,
		Stdout:         combinedStdout,
		Stderr:         combinedStderr,
		Branch:         branchName,
		BaseCommit:     baseCommit,
		OriginalBranch: originalBranch,
		HeadBefore:     headBefore,
		TargetGitRepo:  repoPath,
		CommitID:       primaryCommit,
		ReviewCommitID: primaryCommit,
		AppliedCommits: appliedCommits,
	}, nil
}

// AbortResult contains the output of git am --abort.
type AbortResult struct {
	Success bool   `json:"success"`
	Logs    string `json:"logs"`
	Error   string `json:"error,omitempty"`
}

// AbortApply runs git am --abort, checks out base commit or original branch, and cleans up review branches.
func (b *B4Runner) AbortApply(targetGitRepo, branchName, baseCommit, originalBranch, headBefore string) (*AbortResult, error) {
	repoPath := targetGitRepo
	if repoPath == "" {
		repoPath = b.Config.General.KernelGitDir
	}
	repoPath = config.ExpandPath(repoPath)

	gitDir := filepath.Join(repoPath, ".git")
	if fi, err := os.Stat(gitDir); err != nil || !fi.IsDir() {
		return &AbortResult{
			Success: false,
			Error:   fmt.Sprintf("target directory is not a git repository: %s", repoPath),
		}, nil
	}

	var logs []string

	// 1. Abort git am session if in progress
	out, err := exec.Command("git", "-C", repoPath, "am", "--abort").CombinedOutput()
	if len(out) > 0 {
		logs = append(logs, strings.TrimSpace(string(out)))
	}

	// 2. Checkout base commit or original ref away from branchName, and delete review branch
	cleanBranch := strings.TrimSpace(branchName)
	cleanBase := strings.TrimSpace(baseCommit)
	checkoutRef := ""

	if cleanBranch != "" {
		if cleanBase != "" && strings.ToUpper(cleanBase) != "HEAD" && cleanBase != cleanBranch {
			checkoutRef = cleanBase
		} else if originalBranch != "" && originalBranch != cleanBranch {
			checkoutRef = originalBranch
		} else if headBefore != "" {
			checkoutRef = headBefore
		} else {
			checkoutRef = "HEAD~0"
		}

		if checkoutRef != "" {
			coOut, _ := exec.Command("git", "-C", repoPath, "checkout", checkoutRef).CombinedOutput()
			if len(coOut) > 0 {
				logs = append(logs, strings.TrimSpace(string(coOut)))
			}
		}

		// 3. Delete the review branch
		delOut, _ := exec.Command("git", "-C", repoPath, "branch", "-D", cleanBranch).CombinedOutput()
		if len(delOut) > 0 {
			logs = append(logs, strings.TrimSpace(string(delOut)))
		}
	}

	return &AbortResult{
		Success: err == nil,
		Logs:    strings.Join(logs, "\n"),
	}, nil
}
