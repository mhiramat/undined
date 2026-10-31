// Package bridge handles external terminal and $EDITOR launching for draft composition.
package bridge

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"undined-go/internal/config"
	"undined-go/internal/constants"
	"undined-go/internal/lkml/patch"
	"undined-go/internal/lkml/quote"
	"undined-go/internal/storage/database"
	"undined-go/internal/storage/parser"
)

// OpenEditorRequest defines input parameters for launching external draft editor.
type OpenEditorRequest struct {
	MessageID      string             `json:"message_id"`
	CustomTo       *string            `json:"custom_to"`
	CustomCc       *string            `json:"custom_cc"`
	CustomSubject  *string            `json:"custom_subject"`
	CustomBody     *string            `json:"custom_body"`
	GeneralComment string             `json:"general_comment"`
	Notes          []quote.InlineNote `json:"notes,omitempty"`
}

// OpenEditorResponse represents the API response when editor is successfully spawned.
type OpenEditorResponse struct {
	Success  bool   `json:"success"`
	TempFile string `json:"temp_file"`
	Command  string `json:"command"`
	Message  string `json:"message"`
}

// Launcher handles preparing RFC822 drafts and spawning external editors.
type Launcher struct {
	Config *config.Config
	DB     *database.Database
}

// NewLauncher creates a new editor Launcher.
func NewLauncher(cfg *config.Config, db *database.Database) *Launcher {
	return &Launcher{
		Config: cfg,
		DB:     db,
	}
}

// GenerateTempDraftPath generates a unique file path for draft editing.
func (l *Launcher) GenerateTempDraftPath() (string, error) {
	b := make([]byte, 6)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	name := fmt.Sprintf("undine-reply-%s.eml", hex.EncodeToString(b))
	return filepath.Join(os.TempDir(), name), nil
}

// PrepareDraftContent builds RFC822 message text for editor draft.
func (l *Launcher) PrepareDraftContent(req OpenEditorRequest) (string, error) {
	var msg *database.Message
	if req.MessageID != "" && l.DB != nil {
		if num, err := strconv.ParseInt(strings.TrimSpace(req.MessageID), 10, 64); err == nil {
			msg, _ = l.DB.GetMessageByID(num)
		}
		if msg == nil {
			cleanID := parser.CleanMsgID(req.MessageID)
			msg, _ = l.DB.GetMessageByMsgID(cleanID)
			if msg == nil && cleanID != req.MessageID {
				msg, _ = l.DB.GetMessageByMsgID(req.MessageID)
			}
		}
	}

	if msg == nil && req.CustomBody == nil && req.CustomSubject == nil {
		return "", fmt.Errorf("message not found")
	}

	var quoteBody string
	if req.CustomBody != nil {
		quoteBody = *req.CustomBody
	} else if msg != nil {
		parsed := patch.Parse(msg.BodyPlain)
		var notes []quote.InlineNote
		if len(req.Notes) > 0 {
			notes = req.Notes
		} else if l.DB != nil {
			dbComments, err := l.DB.GetMessageComments(msg.MessageID)
			if err == nil {
				for _, c := range dbComments {
					notes = append(notes, quote.InlineNote{
						File:   c.FilePath,
						LineNo: c.LineNo,
						Side:   c.Side,
						Text:   c.Text,
					})
				}
			}
		}

		sig := l.Config.SMTP.EffectiveSignature()
		quoteBody = quote.MergePatchReviewQuote(
			msg.Date,
			msg.FromName,
			msg.FromAddr,
			msg.BodyPlain,
			parsed,
			notes,
			req.GeneralComment,
			sig,
		)
	}

	// Subject
	var subject string
	if req.CustomSubject != nil {
		subject = *req.CustomSubject
	} else if msg != nil {
		cleanSubj := strings.TrimSpace(msg.Subject)
		if strings.HasPrefix(strings.ToLower(cleanSubj), "re:") {
			subject = cleanSubj
		} else {
			subject = fmt.Sprintf("Re: %s", cleanSubj)
		}
	}

	// From
	fromName := l.Config.SMTP.FromName
	fromEmail := l.Config.SMTP.FromEmail
	fromHdr := fmt.Sprintf("%s <%s>", fromName, fromEmail)

	// To and Cc
	var defaultTo, defaultCc string
	if msg != nil {
		defaultTo, defaultCc = parser.BuildReplyRecipients(msg.FromAddr, msg.ToAddr, msg.CcAddr, fromEmail)
	}

	toAddr := defaultTo
	if req.CustomTo != nil {
		toAddr = *req.CustomTo
	}
	ccAddr := defaultCc
	if req.CustomCc != nil {
		ccAddr = *req.CustomCc
	}

	var draftLines []string
	draftLines = append(draftLines,
		fmt.Sprintf("From: %s", fromHdr),
		fmt.Sprintf("To: %s", toAddr),
	)
	if ccAddr != "" {
		draftLines = append(draftLines, fmt.Sprintf("Cc: %s", ccAddr))
	}
	draftLines = append(draftLines,
		fmt.Sprintf("Subject: %s", subject),
		fmt.Sprintf("X-Mailer: %s", constants.DefaultXMailer),
	)

	if req.CustomTo == nil || *req.CustomTo != "" {
		if msg != nil {
			cleanMid := parser.CleanMsgID(msg.MessageID)
			if cleanMid != "" {
				draftLines = append(draftLines, fmt.Sprintf("In-Reply-To: <%s>", cleanMid))
			}
		}
	}

	draftLines = append(draftLines,
		"Content-Type: text/plain; charset=utf-8",
		"Content-Transfer-Encoding: 8bit",
		"",
		quoteBody,
	)

	return strings.Join(draftLines, "\n") + "\n", nil
}

// Launch prepares draft file and executes terminal editor command asynchronously.
func (l *Launcher) Launch(req OpenEditorRequest) (*OpenEditorResponse, error) {
	cmdTemplate := l.Config.General.GetExternalEditorCmd()
	if cmdTemplate == "" {
		return nil, fmt.Errorf("external editor command not configured in [general] editor_cmd or terminal_cmd")
	}

	content, err := l.PrepareDraftContent(req)
	if err != nil {
		return nil, err
	}

	tempPath, err := l.GenerateTempDraftPath()
	if err != nil {
		return nil, fmt.Errorf("failed to generate temp draft path: %w", err)
	}

	if err := os.WriteFile(tempPath, []byte(content), 0600); err != nil {
		return nil, fmt.Errorf("failed to write temp draft file: %w", err)
	}

	quotedPath := "'" + strings.ReplaceAll(tempPath, "'", `'\''`) + "'"
	cmdStr := strings.ReplaceAll(cmdTemplate, "{file}", quotedPath)
	cmdStr = strings.ReplaceAll(cmdStr, "{line}", "10")
	cmdStr = strings.ReplaceAll(cmdStr, "{cursor_line}", "10")

	if !strings.Contains(cmdTemplate, "{file}") {
		cmdStr = fmt.Sprintf("%s %s", cmdTemplate, quotedPath)
	}

	cmd := exec.Command("sh", "-c", cmdStr)
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("failed to spawn editor: %w", err)
	}

	return &OpenEditorResponse{
		Success:  true,
		TempFile: tempPath,
		Command:  cmdStr,
		Message:  "Editor launched",
	}, nil
}
