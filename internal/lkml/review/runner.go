// Package review manages asynchronous background AI patch review executions (agy --print) with queuing and cancellation.
package review

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"undined-go/internal/config"
	"undined-go/internal/constants"
	"undined-go/internal/events"
	"undined-go/internal/storage/database"
	"undined-go/internal/storage/maildir"
	"undined-go/internal/storage/parser"
)

var (
	safeMsgIDRegex  = regexp.MustCompile(`^[a-zA-Z0-9._\-+@~]+$`)
	safeCommitRegex = regexp.MustCompile(`^[a-zA-Z0-9._\-+/~^:]+$`)
)

// SplitCommandLine splits a command string into program and arguments, respecting quotes.
func SplitCommandLine(cmdStr string) ([]string, error) {
	cmdStr = strings.TrimSpace(cmdStr)
	if cmdStr == "" {
		return nil, fmt.Errorf("empty command")
	}

	var parts []string
	var current strings.Builder
	inSingle := false
	inDouble := false
	escaped := false

	for _, r := range cmdStr {
		if escaped {
			current.WriteRune(r)
			escaped = false
			continue
		}
		if r == '\\' && !inSingle {
			escaped = true
			continue
		}
		if r == '\'' && !inDouble {
			inSingle = !inSingle
			continue
		}
		if r == '"' && !inSingle {
			inDouble = !inDouble
			continue
		}
		if (r == ' ' || r == '\t') && !inSingle && !inDouble {
			if current.Len() > 0 {
				parts = append(parts, current.String())
				current.Reset()
			}
			continue
		}
		current.WriteRune(r)
	}

	if current.Len() > 0 {
		parts = append(parts, current.String())
	}
	if inSingle || inDouble {
		return nil, fmt.Errorf("unclosed quote in command %q", cmdStr)
	}
	if len(parts) == 0 {
		return nil, fmt.Errorf("empty command")
	}
	return parts, nil
}

// Job represents a review job in queue or execution.
type Job struct {
	MessageID    string `json:"message_id"`
	CommitID     string `json:"commit_id"`
	Status       string `json:"status"` // "queued", "in-progress", "completed", "error", "cancelled"
	ReviewDBID   *int64 `json:"review_db_id,omitempty"`
	FilePath     string `json:"file_path,omitempty"`
	Error        string `json:"error,omitempty"`
	Output       string `json:"output,omitempty"`
	OrigSubject  string `json:"-"`
	OrigDBID     int64  `json:"-"`
	OrigFolderID int64  `json:"-"`
}

// JobInfo provides serialized queue job details.
type JobInfo struct {
	MessageID  string `json:"message_id"`
	CommitID   string `json:"commit_id"`
	Status     string `json:"status"`
	ReviewDBID *int64 `json:"review_db_id,omitempty"`
}

// StatusResponse represents current review runner status for frontend and API polling.
type StatusResponse struct {
	IsRunning        bool      `json:"is_running"`
	ActiveMessageID  string    `json:"active_message_id,omitempty"`
	ActiveCommitID   string    `json:"active_commit_id,omitempty"`
	ActiveReviewDBID *int64    `json:"active_review_db_id,omitempty"`
	Status           string    `json:"status"` // "idle", "running", "completed", "error", "cancelled"
	Error            string    `json:"error,omitempty"`
	Output           string    `json:"output,omitempty"`
	Queue            []JobInfo `json:"queue"`
	QueueLength      int       `json:"queue_length"`
}

// TriggerResult represents the immediate response from triggering a review.
type TriggerResult struct {
	Success   bool   `json:"success"`
	Status    string `json:"status"` // "running" or "queued"
	MessageID string `json:"message_id"`
	CommitID  string `json:"commit_id"`
}

// Runner coordinates background AI patch reviews.
type Runner struct {
	Config  *config.Config
	Maildir *maildir.MaildirStore
	DB      *database.Database
	Events  *events.Hub

	mu         sync.Mutex
	queue      []*Job
	activeJob  *Job
	isRunning  bool
	status     string // "idle", "running", "completed", "error", "cancelled"
	lastError  string
	lastOutput string

	cancelFunc context.CancelFunc
}

// SetEvents attaches an Event Hub to the ReviewRunner.
func (r *Runner) SetEvents(hub *events.Hub) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.Events = hub
}

// NewRunner creates a new ReviewRunner.
func NewRunner(cfg *config.Config, store *maildir.MaildirStore, db *database.Database) *Runner {
	return &Runner{
		Config:  cfg,
		Maildir: store,
		DB:      db,
		status:  "idle",
		queue:   make([]*Job, 0),
	}
}

// GetStatus returns the current review runner status and queue.
func (r *Runner) GetStatus() StatusResponse {
	r.mu.Lock()
	defer r.mu.Unlock()

	res := StatusResponse{
		IsRunning:   r.isRunning,
		Status:      r.status,
		Error:       r.lastError,
		Output:      r.lastOutput,
		QueueLength: len(r.queue),
		Queue:       make([]JobInfo, 0, len(r.queue)),
	}

	if r.activeJob != nil {
		res.ActiveMessageID = r.activeJob.MessageID
		res.ActiveCommitID = r.activeJob.CommitID
		res.ActiveReviewDBID = r.activeJob.ReviewDBID
	}

	for _, j := range r.queue {
		res.Queue = append(res.Queue, JobInfo{
			MessageID:  j.MessageID,
			CommitID:   j.CommitID,
			Status:     j.Status,
			ReviewDBID: j.ReviewDBID,
		})
	}

	return res
}

func (r *Runner) matchesJob(job *Job, target string) bool {
	target = strings.TrimSpace(target)
	cleanTarget := parser.CleanMsgID(target)
	if cleanTarget == "" {
		cleanTarget = target
	}

	if parser.CleanMsgID(job.MessageID) == cleanTarget || job.MessageID == target {
		return true
	}
	if job.ReviewDBID != nil && fmt.Sprintf("%d", *job.ReviewDBID) == target {
		return true
	}
	if job.OrigDBID != 0 && fmt.Sprintf("%d", job.OrigDBID) == target {
		return true
	}
	return false
}

// TriggerReview validates parameters, queues the job, and starts the worker if needed.
func (r *Runner) TriggerReview(msgID, commitID string) (*TriggerResult, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	cleanedID := parser.CleanMsgID(msgID)
	if cleanedID == "" {
		cleanedID = msgID
	}

	if !safeMsgIDRegex.MatchString(cleanedID) {
		return nil, fmt.Errorf("invalid message ID format: contains illegal characters")
	}

	// 1. Check duplicate running or queued jobs
	if r.isRunning && r.activeJob != nil && r.matchesJob(r.activeJob, cleanedID) {
		return nil, fmt.Errorf("a review is already in progress for message %s", msgID)
	}

	for _, qj := range r.queue {
		if r.matchesJob(qj, cleanedID) {
			return nil, fmt.Errorf("a review is already queued for message %s", msgID)
		}
	}

	// 2. Resolve message metadata and commit ID
	var origMsg *database.Message
	if r.DB != nil {
		if num, err := strconv.ParseInt(msgID, 10, 64); err == nil && num > 0 {
			origMsg, _ = r.DB.GetMessageByID(num)
		}
		if origMsg == nil {
			origMsg, _ = r.DB.GetMessageByMsgID(cleanedID)
			if origMsg == nil && msgID != cleanedID {
				origMsg, _ = r.DB.GetMessageByMsgID(msgID)
			}
		}
		if origMsg != nil && origMsg.MessageID != "" && cleanedID == msgID {
			cleanedID = parser.CleanMsgID(origMsg.MessageID)
		}
	}

	if r.DB != nil && origMsg == nil {
		return nil, fmt.Errorf("message %s not found in local mailbox", cleanedID)
	}

	if origMsg != nil && !origMsg.IsPatch {
		return nil, fmt.Errorf("message %s is not a patch", cleanedID)
	}

	resolvedCommit := commitID
	if resolvedCommit == "" && origMsg != nil {
		resolvedCommit = origMsg.ReviewCommitID
		if resolvedCommit == "" && r.DB != nil {
			resolvedCommit, _ = r.DB.GetReviewCommit(cleanedID)
		}
	}

	if resolvedCommit == "" {
		return nil, fmt.Errorf("message has no associated review-commit-id. Apply the patch first")
	}

	if strings.HasPrefix(resolvedCommit, "-") || !safeCommitRegex.MatchString(resolvedCommit) {
		return nil, fmt.Errorf("invalid commit ID format: must be a valid commit hash or ref")
	}

	job := &Job{
		MessageID: cleanedID,
		CommitID:  resolvedCommit,
		Status:    "queued",
	}

	if origMsg != nil {
		job.OrigSubject = origMsg.Subject
		job.OrigDBID = origMsg.ID
		job.OrigFolderID = origMsg.FolderID
	}

	// 3. Save placeholder email in AI-reviews mailbox
	r.saveOrUpdateReviewMessage(job, "Queued", "")

	// 4. Enqueue and trigger worker loop
	r.queue = append(r.queue, job)

	statusStr := "queued"
	if !r.isRunning {
		r.isRunning = true
		r.status = "running"
		statusStr = "running"
		go r.workerLoop()
	}

	return &TriggerResult{
		Success:   true,
		Status:    statusStr,
		MessageID: cleanedID,
		CommitID:  resolvedCommit,
	}, nil
}

// CancelReview cancels an in-progress or queued review.
func (r *Runner) CancelReview(target string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()

	cleanTarget := strings.TrimSpace(target)

	// If target is empty, cancel active or first queued
	if cleanTarget == "" {
		if r.isRunning && r.activeJob != nil {
			if r.cancelFunc != nil {
				r.cancelFunc()
			}
			return true
		}
		if len(r.queue) > 0 {
			q := r.queue[0]
			r.queue = r.queue[1:]
			q.Status = "cancelled"
			q.Error = "Review was cancelled"
			r.status = "cancelled"
			r.lastError = "Review was cancelled"
			r.saveOrUpdateReviewMessage(q, "Review was cancelled", " (cancelled)")
			return true
		}
		return false
	}

	// Match active job
	if r.isRunning && r.activeJob != nil && r.matchesJob(r.activeJob, cleanTarget) {
		if r.cancelFunc != nil {
			r.cancelFunc()
		}
		return true
	}

	// Match queued jobs
	for i, qj := range r.queue {
		if r.matchesJob(qj, cleanTarget) {
			r.queue = append(r.queue[:i], r.queue[i+1:]...)
			qj.Status = "cancelled"
			qj.Error = "Review was cancelled"
			r.status = "cancelled"
			r.lastError = "Review was cancelled"
			r.saveOrUpdateReviewMessage(qj, "Review was cancelled", " (cancelled)")
			return true
		}
	}

	return false
}

func (r *Runner) workerLoop() {
	for {
		r.mu.Lock()
		if len(r.queue) == 0 {
			r.isRunning = false
			r.activeJob = nil
			r.cancelFunc = nil
			if r.status == "running" {
				r.status = "idle"
			}
			r.mu.Unlock()
			return
		}

		job := r.queue[0]
		r.queue = r.queue[1:]

		if job.Status == "cancelled" {
			r.mu.Unlock()
			continue
		}

		r.activeJob = job
		r.isRunning = true
		r.status = "running"
		r.lastError = ""
		r.lastOutput = ""

		ctx, cancel := context.WithCancel(context.Background())
		r.cancelFunc = cancel

		job.Status = "in-progress"
		r.saveOrUpdateReviewMessage(job, "In-progress", "")
		r.mu.Unlock()

		r.executeJob(ctx, job)
	}
}

func (r *Runner) executeJob(ctx context.Context, job *Job) {
	promptTpl := r.Config.Review.EffectivePrompt()
	prompt := strings.ReplaceAll(promptTpl, "{message_id}", job.MessageID)
	prompt = strings.ReplaceAll(prompt, "{msgid}", job.MessageID)
	prompt = strings.ReplaceAll(prompt, "{commit_id}", job.CommitID)
	prompt = strings.ReplaceAll(prompt, "{commit}", job.CommitID)

	var cmd *exec.Cmd

	wrapperScript := r.Config.Review.WrapperScript
	if wrapperScript != "" {
		wrapperPath := config.ExpandPath(wrapperScript)
		cmd = exec.CommandContext(ctx, wrapperPath, job.MessageID, job.CommitID)
	} else {
		cmdStr := r.Config.Review.Command
		if cmdStr == "" {
			cmdStr = constants.DefaultReviewCmd
		}
		parts, err := SplitCommandLine(cmdStr)
		if err != nil || len(parts) == 0 {
			r.mu.Lock()
			r.status = "error"
			job.Status = "error"
			job.Error = fmt.Sprintf("invalid review command %q: %v", cmdStr, err)
			r.lastError = job.Error
			r.saveOrUpdateReviewMessage(job, job.Error, " (failed)")
			r.mu.Unlock()
			return
		}

		hasTemplateVar := false
		for i, p := range parts {
			if strings.Contains(p, "{message_id}") || strings.Contains(p, "{msgid}") ||
				strings.Contains(p, "{commit_id}") || strings.Contains(p, "{commit}") ||
				strings.Contains(p, "{prompt}") {
				hasTemplateVar = true
			}
			p = strings.ReplaceAll(p, "{message_id}", job.MessageID)
			p = strings.ReplaceAll(p, "{msgid}", job.MessageID)
			p = strings.ReplaceAll(p, "{commit_id}", job.CommitID)
			p = strings.ReplaceAll(p, "{commit}", job.CommitID)
			p = strings.ReplaceAll(p, "{prompt}", prompt)
			parts[i] = p
		}

		if !hasTemplateVar {
			parts = append(parts, prompt)
		}

		cmd = exec.CommandContext(ctx, parts[0], parts[1:]...)
	}

	// Always pipe prompt to stdin and provide environment variables
	cmd.Stdin = strings.NewReader(prompt)
	cmd.Env = append(os.Environ(),
		"UNDINE_MESSAGE_ID="+job.MessageID,
		"UNDINE_COMMIT_ID="+job.CommitID,
		"UNDINE_PROMPT="+prompt,
	)

	outBytes, err := cmd.CombinedOutput()
	output := string(outBytes)

	r.mu.Lock()
	defer r.mu.Unlock()

	r.lastOutput = output
	job.Output = output

	if ctx.Err() == context.Canceled {
		r.status = "cancelled"
		job.Status = "cancelled"
		r.lastError = "Review was cancelled"
		job.Error = r.lastError
		r.saveOrUpdateReviewMessage(job, "Review was cancelled", " (cancelled)")
		return
	}

	if err != nil {
		r.status = "error"
		job.Status = "error"
		r.lastError = fmt.Sprintf("%s: %s", err.Error(), strings.TrimSpace(output))
		job.Error = r.lastError
		r.saveOrUpdateReviewMessage(job, r.lastError, " (failed)")
		return
	}

	r.status = "completed"
	job.Status = "completed"
	r.lastError = ""
	job.Error = ""
	r.saveOrUpdateReviewMessage(job, output, "")
}

func (r *Runner) saveOrUpdateReviewMessage(job *Job, bodyText, statusTag string) {
	if r.DB == nil || r.Maildir == nil {
		return
	}

	origSubj := job.OrigSubject
	if origSubj == "" {
		if len(job.CommitID) >= 12 {
			origSubj = fmt.Sprintf("patch %s", job.CommitID[:12])
		} else {
			origSubj = fmt.Sprintf("patch %s", job.MessageID)
		}
	}
	subject := fmt.Sprintf("AI review%s: %s", statusTag, origSubj)

	cleanBody := strings.TrimSpace(bodyText)
	if cleanBody == "" {
		cleanBody = "(No output from review process)"
	}

	var rawLines []string
	rawLines = append(rawLines,
		"From: AI review",
		fmt.Sprintf("Subject: %s", subject),
		fmt.Sprintf("Date: %s", time.Now().Format(time.RFC1123Z)),
		"MIME-Version: 1.0",
		"Content-Type: text/plain; charset=utf-8",
		"Content-Transfer-Encoding: 8bit",
	)

	cleanedMid := parser.CleanMsgID(job.MessageID)
	if cleanedMid != "" {
		rawLines = append(rawLines,
			fmt.Sprintf("In-Reply-To: <%s>", cleanedMid),
			fmt.Sprintf("References: <%s>", cleanedMid),
			fmt.Sprintf("X-Reviewed-Message-ID: <%s>", cleanedMid),
		)
	} else if job.OrigDBID != 0 {
		rawLines = append(rawLines,
			fmt.Sprintf("In-Reply-To: <id-%d>", job.OrigDBID),
			fmt.Sprintf("References: <id-%d>", job.OrigDBID),
		)
	}
	if job.OrigDBID != 0 {
		rawLines = append(rawLines, fmt.Sprintf("X-Reviewed-Message-DB-ID: %d", job.OrigDBID))
	}
	if job.OrigFolderID != 0 {
		rawLines = append(rawLines, fmt.Sprintf("X-Reviewed-Folder-ID: %d", job.OrigFolderID))
	}
	rawLines = append(rawLines, "", cleanBody, "")
	rawBytes := []byte(strings.Join(rawLines, "\r\n"))

	// Check if file already exists
	if job.FilePath != "" {
		if _, err := os.Stat(job.FilePath); err == nil {
			_ = os.WriteFile(job.FilePath, rawBytes, 0600)
			if job.ReviewDBID != nil {
				_ = r.DB.UpdateMessageSubjectAndBody(*job.ReviewDBID, subject, cleanBody)
			}
			if r.Events != nil {
				r.Events.Publish("mail.sync_completed", map[string]any{
					"folder":    constants.FolderAIReviews,
					"new_count": 0,
					"timestamp": time.Now().Unix(),
				})
				r.Events.Publish("message.updated", map[string]any{
					"folder":     constants.FolderAIReviews,
					"message_id": job.MessageID,
				})
			}
			return
		}
	}

	folder, err := r.DB.GetOrCreateFolder(constants.FolderAIReviews, false)
	if err != nil {
		return
	}

	_ = r.Maildir.EnsureFolder(constants.FolderAIReviews)
	savedPath, err := r.Maildir.AddMessage(constants.FolderAIReviews, rawBytes, constants.FlagSeen)
	if err != nil {
		return
	}

	job.FilePath = filepath.Clean(savedPath)
	dbID, err := r.DB.IndexEmailFile(folder.ID, savedPath)
	if err == nil {
		job.ReviewDBID = &dbID
		if r.Events != nil {
			r.Events.Publish("mail.sync_completed", map[string]any{
				"folder":    constants.FolderAIReviews,
				"new_count": 1,
				"timestamp": time.Now().Unix(),
			})
			r.Events.Publish("message.updated", map[string]any{
				"folder":     constants.FolderAIReviews,
				"message_id": job.MessageID,
			})
		}
	}
}
