// Package b4 provides subprocess orchestration for b4 mbox and git am operations.
package b4

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
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
	msgIDTrailerRegex  = regexp.MustCompile(`(?im)^Message-ID:\s*<([^>]+)>`)
	loreLinkRegex      = regexp.MustCompile(`(?im)^Link:\s*https?://lore\.kernel\.org/(?:r/|all/|[a-zA-Z0-9_-]+/)([^/\s>]+)`)
	seriesIndexRegex   = regexp.MustCompile(`(?i)\[PATCH[^\]]*\b(\d+)\s*/\s*(\d+)[^\]]*\]`)
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

// IsValidGitRef verifies that a git refname or commit hash is syntactically safe
// and does not start with a hyphen to prevent option flag injection.
func IsValidGitRef(ref string) bool {
	ref = strings.TrimSpace(ref)
	if ref == "" || strings.HasPrefix(ref, "-") {
		return false
	}
	for _, r := range ref {
		if !((r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') ||
			r == '.' || r == '_' || r == '-' || r == '/' || r == '+' || r == '~' || r == '^' || r == ':') {
			return false
		}
	}
	return true
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

	cmd := exec.Command(b4Cmd, "mbox", "-o", "-", "--", cleanedID)
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
	cleanBranch := strings.TrimSpace(branchName)
	cleanBase := strings.TrimSpace(baseCommit)

	if cleanBranch != "" && !IsValidGitRef(cleanBranch) {
		return &ApplyResult{
			Success:       false,
			ExitCode:      1,
			Stderr:        fmt.Sprintf("invalid branch name %q: must be a valid git ref not starting with '-'", cleanBranch),
			TargetGitRepo: repoPath,
		}, nil
	}
	if cleanBase != "" && !IsValidGitRef(cleanBase) {
		return &ApplyResult{
			Success:       false,
			ExitCode:      1,
			Stderr:        fmt.Sprintf("invalid base commit %q: must be a valid git ref not starting with '-'", cleanBase),
			TargetGitRepo: repoPath,
		}, nil
	}

	if cleanBranch != "" || cleanBase != "" {
		args := []string{"-C", repoPath, "checkout"}
		if cleanBranch != "" {
			args = append(args, "-b", cleanBranch)
		}
		if cleanBase != "" {
			args = append(args, cleanBase)
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
	b4Args = append(b4Args, "--", cleanedID)

	b4Exec := exec.Command(b4Cmd, b4Args...)
	var b4Out, b4Err bytes.Buffer
	b4Exec.Stdout = &b4Out
	b4Exec.Stderr = &b4Err

	var amStdin io.Reader = &b4Out

	if err := b4Exec.Run(); err != nil {
		// If b4 failed (e.g. message not on public lore.kernel.org), check if the message exists locally
		appliedLocally := false
		if b.DB != nil {
			if origMsg, dbErr := b.DB.GetMessageByMsgID(cleanedID); dbErr == nil && origMsg != nil {
				var localData []byte
				if origMsg.FilePath != "" {
					localData, _ = os.ReadFile(origMsg.FilePath)
				}
				if len(localData) == 0 && origMsg.BodyPlain != "" {
					var buf bytes.Buffer
					fromName := origMsg.FromName
					if fromName == "" {
						fromName = "Kernel Developer"
					}
					fromAddr := origMsg.FromAddr
					if fromAddr == "" {
						fromAddr = "dev@example.com"
					}
					buf.WriteString(fmt.Sprintf("From %s Mon Jan 01 00:00:00 2026\n", fromAddr))
					buf.WriteString(fmt.Sprintf("From: %s <%s>\n", fromName, fromAddr))
					buf.WriteString(fmt.Sprintf("Subject: %s\n", origMsg.Subject))
					buf.WriteString(fmt.Sprintf("Message-ID: <%s>\n", origMsg.MessageID))
					if origMsg.Date != "" {
						buf.WriteString(fmt.Sprintf("Date: %s\n", origMsg.Date))
					}
					buf.WriteString("\n")
					buf.WriteString(origMsg.BodyPlain)
					buf.WriteString("\n")
					localData = buf.Bytes()
				}
				if len(localData) > 0 {
					amStdin = bytes.NewReader(localData)
					appliedLocally = true
				}
			}
		}
		if !appliedLocally {
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
	}

	// Capture HEAD before git am if not captured yet
	if headBefore == "" {
		if out, err := exec.Command("git", "-C", repoPath, "rev-parse", "HEAD").Output(); err == nil {
			headBefore = strings.TrimSpace(string(out))
		}
	}

	// 3. Pipe into git am
	gitAm := exec.Command("git", "-C", repoPath, "am")
	gitAm.Stdin = amStdin
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

	matchedCommit := b.RecordSeriesCommits(cleanedID, repoPath, appliedCommits)
	primaryCommit := headAfter
	if matchedCommit != "" {
		primaryCommit = matchedCommit
	}
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

// RecordSeriesCommits inspects applied git commits, matches them to messages in the series,
// and records review commit hashes for all identified patch emails in the database.
// Returns the commit ID matching cleanedID if specifically identified.
func (b *B4Runner) RecordSeriesCommits(cleanedID, repoPath string, appliedCommits []string) string {
	if b.DB == nil || len(appliedCommits) == 0 || repoPath == "" {
		return ""
	}

	// 1. Gather all candidate messages (from thread hierarchy, same folder, and b4-imports)
	candidateMap := make(map[string]*database.Message)
	addCandidate := func(m *database.Message) {
		if m == nil {
			return
		}
		clean := parser.CleanMsgID(m.MessageID)
		if clean != "" {
			if _, exists := candidateMap[clean]; !exists {
				candidateMap[clean] = m
			}
		}
	}

	var origMsg *database.Message
	if cleanedID != "" {
		origMsg, _ = b.DB.GetMessageByMsgID(cleanedID)
		addCandidate(origMsg)

		if threadMsgs, err := b.DB.GetThreadHierarchy(cleanedID); err == nil {
			for _, m := range threadMsgs {
				addCandidate(m)
			}
		}
	}

	if origMsg != nil && origMsg.FolderID > 0 {
		if folderMsgs, err := b.DB.ListMessages(&origMsg.FolderID, 200, 0); err == nil {
			for _, m := range folderMsgs {
				addCandidate(m)
			}
		}
	}

	// Also check b4-imports folder if present
	if b4Folder, err := b.DB.GetFolderByName(constants.FolderB4Imports); err == nil && b4Folder != nil {
		if b4Msgs, err := b.DB.ListMessages(&b4Folder.ID, 200, 0); err == nil {
			for _, m := range b4Msgs {
				addCandidate(m)
			}
		}
	}

	// 2. Extract commit info (subject and body) from git
	type commitInfo struct {
		hash    string
		subject string
		body    string
	}
	commits := make([]commitInfo, 0, len(appliedCommits))
	for _, hash := range appliedCommits {
		info := commitInfo{hash: hash}
		if out, err := exec.Command("git", "-C", repoPath, "log", "-1", "--format=%s", hash).Output(); err == nil {
			info.subject = strings.TrimSpace(string(out))
		}
		if out, err := exec.Command("git", "-C", repoPath, "log", "-1", "--format=%B", hash).Output(); err == nil {
			info.body = string(out)
		}
		commits = append(commits, info)
	}

	assignedCommits := make(map[string]bool)
	assignedMsgs := make(map[string]string) // cleanMsgID -> commitHash

	// Priority 1: Match via Message-ID / Link trailers in commit body
	for _, c := range commits {
		var extractedID string
		if m := msgIDTrailerRegex.FindStringSubmatch(c.body); len(m) > 1 {
			extractedID = parser.CleanMsgID(m[1])
		} else if m := loreLinkRegex.FindStringSubmatch(c.body); len(m) > 1 {
			extractedID = parser.CleanMsgID(m[1])
		}

		if extractedID != "" {
			_ = b.DB.SetMessageReviewCommit(extractedID, c.hash)
			assignedCommits[c.hash] = true
			assignedMsgs[extractedID] = c.hash
		}
	}

	// Priority 2: Match via CleanSeriesTitle(Subject)
	for _, c := range commits {
		if assignedCommits[c.hash] || c.subject == "" {
			continue
		}
		for msgID, msg := range candidateMap {
			if assignedMsgs[msgID] != "" {
				continue
			}
			msgTitle := CleanSeriesTitle(msg.Subject)
			if msgTitle != "" && (strings.EqualFold(msgTitle, c.subject) || strings.EqualFold(strings.TrimSpace(msg.Subject), c.subject)) {
				_ = b.DB.SetMessageReviewCommit(msgID, c.hash)
				assignedCommits[c.hash] = true
				assignedMsgs[msgID] = c.hash
				break
			}
		}
	}

	// Priority 3: Match via series index [i/N]
	for msgID, msg := range candidateMap {
		if assignedMsgs[msgID] != "" {
			continue
		}
		if m := seriesIndexRegex.FindStringSubmatch(msg.Subject); len(m) > 2 {
			idx, err1 := strconv.Atoi(m[1])
			if err1 == nil && idx >= 1 && idx <= len(commits) {
				c := commits[idx-1]
				if !assignedCommits[c.hash] {
					_ = b.DB.SetMessageReviewCommit(msgID, c.hash)
					assignedCommits[c.hash] = true
					assignedMsgs[msgID] = c.hash
				}
			}
		}
	}

	// Priority 4: Single commit match
	if len(commits) == 1 && len(assignedCommits) == 0 {
		c := commits[0]
		targetID := cleanedID
		if targetID == "" && len(candidateMap) > 0 {
			for id := range candidateMap {
				targetID = id
				break
			}
		}
		if targetID != "" {
			_ = b.DB.SetMessageReviewCommit(targetID, c.hash)
			assignedCommits[c.hash] = true
			assignedMsgs[targetID] = c.hash
		}
	}

	return assignedMsgs[cleanedID]
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

	if cleanBranch != "" && IsValidGitRef(cleanBranch) {
		if cleanBase != "" && strings.ToUpper(cleanBase) != "HEAD" && cleanBase != cleanBranch && IsValidGitRef(cleanBase) {
			checkoutRef = cleanBase
		} else if originalBranch != "" && originalBranch != cleanBranch && IsValidGitRef(originalBranch) {
			checkoutRef = originalBranch
		} else if headBefore != "" && IsValidGitRef(headBefore) {
			checkoutRef = headBefore
		} else {
			checkoutRef = "HEAD~0"
		}

		if checkoutRef != "" && IsValidGitRef(checkoutRef) {
			coOut, _ := exec.Command("git", "-C", repoPath, "checkout", checkoutRef).CombinedOutput()
			if len(coOut) > 0 {
				logs = append(logs, strings.TrimSpace(string(coOut)))
			}
		}

		// 3. Delete the review branch
		delOut, _ := exec.Command("git", "-C", repoPath, "branch", "-D", "--", cleanBranch).CombinedOutput()
		if len(delOut) > 0 {
			logs = append(logs, strings.TrimSpace(string(delOut)))
		}
	}

	return &AbortResult{
		Success: err == nil,
		Logs:    strings.Join(logs, "\n"),
	}, nil
}
