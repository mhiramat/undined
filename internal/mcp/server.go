// Package mcp implements the Model Context Protocol (MCP) server for Project Undine.
// It exposes tools to AI coding agents over JSON-RPC 2.0 (STDIO) and provides
// structured access logging to ensure auditing and traceability.
package mcp

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"undined-go/internal/config"
	"undined-go/internal/constants"
	"undined-go/internal/lkml/b4"
	"undined-go/internal/lkml/patch"
	"undined-go/internal/lkml/quote"
	"undined-go/internal/patchwork"
	"undined-go/internal/smtp"
	"undined-go/internal/storage/database"
	"undined-go/internal/storage/maildir"
	"undined-go/internal/storage/parser"
)

// Server implements the MCP server for UndineD.
type Server struct {
	Config    *config.Config
	Maildir   *maildir.MaildirStore
	DB        *database.Database
	B4        *b4.B4Runner
	Patchwork *patchwork.Client

	logMu     sync.Mutex
	logWriter io.Writer
}

// NewServer creates a new MCP Server instance with provided or defaulted components.
func NewServer(
	cfg *config.Config,
	store *maildir.MaildirStore,
	db *database.Database,
	b4Runner *b4.B4Runner,
	pwClient *patchwork.Client,
) *Server {
	if cfg == nil {
		var err error
		cfg, err = config.Load()
		if err != nil {
			cfg = config.NewDefaultConfig()
		}
	}
	if store == nil && cfg.General.MaildirPath != "" {
		store = maildir.NewMaildirStore(cfg.General.MaildirPath)
	}
	if db == nil && cfg.General.DBPath != "" {
		var err error
		db, err = database.NewDatabase(cfg.General.DBPath)
		if err != nil {
			log.Printf("Warning: failed to initialize database: %v", err)
		}
	}
	if b4Runner == nil && store != nil && db != nil {
		b4Runner = b4.NewB4Runner(cfg, store, db)
	}
	if pwClient == nil {
		pwClient = patchwork.NewClient(cfg, db, store)
	}

	return &Server{
		Config:    cfg,
		Maildir:   store,
		DB:        db,
		B4:        b4Runner,
		Patchwork: pwClient,
	}
}

// SetLogWriter sets a custom writer for MCP access logs (used for testing).
func (s *Server) SetLogWriter(w io.Writer) {
	s.logMu.Lock()
	defer s.logMu.Unlock()
	s.logWriter = w
}

func (s *Server) writeAccessLog(level, message string) {
	s.logMu.Lock()
	defer s.logMu.Unlock()

	formatted := fmt.Sprintf("%s [%s] [undine.mcp] %s\n",
		time.Now().Format("2006-01-02 15:04:05"), level, message)

	if s.logWriter != nil {
		_, _ = s.logWriter.Write([]byte(formatted))
	}

	// Always append to configured log file if available
	logFile := ""
	if s.Config != nil && s.Config.General.LogFile != "" {
		logFile = s.Config.General.LogFile
	} else {
		home, _ := os.UserHomeDir()
		if home != "" {
			logFile = filepath.Join(home, ".cache", "undined", "undined.log")
		}
	}

	if logFile != "" {
		_ = os.MkdirAll(filepath.Dir(logFile), 0700)
		f, err := os.OpenFile(logFile, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0600)
		if err == nil {
			defer f.Close()
			_, _ = f.WriteString(formatted)
		}
	}
}

// JSON-RPC 2.0 Models
type jsonRPCRequest struct {
	JSONRPC string           `json:"jsonrpc"`
	ID      *json.RawMessage `json:"id,omitempty"`
	Method  string           `json:"method"`
	Params  json.RawMessage  `json:"params,omitempty"`
}

type jsonRPCResponse struct {
	JSONRPC string           `json:"jsonrpc"`
	ID      *json.RawMessage `json:"id,omitempty"`
	Result  interface{}      `json:"result,omitempty"`
	Error   *jsonRPCError    `json:"error,omitempty"`
}

type jsonRPCError struct {
	Code    int         `json:"code"`
	Message string      `json:"message"`
	Data    interface{} `json:"data,omitempty"`
}

type toolCallParams struct {
	Name      string                 `json:"name"`
	Arguments map[string]interface{} `json:"arguments"`
}

// Tool definitions schema returned by tools/list
func (s *Server) getToolsList() []map[string]interface{} {
	return []map[string]interface{}{
		{
			"name":        "list_recent_emails",
			"description": "List recent emails optionally filtered by folder name (or across all folders if not specified).",
			"inputSchema": map[string]interface{}{
				"type":  "object",
				"title": "list_recent_emailsArguments",
				"properties": map[string]interface{}{
					"folder": map[string]interface{}{
						"title": "Folder",
						"anyOf": []map[string]interface{}{{"type": "string"}, {"type": "null"}},
						"default": nil,
					},
					"limit": map[string]interface{}{
						"title":   "Limit",
						"type":    "integer",
						"default": 20,
					},
				},
			},
			"outputSchema": map[string]interface{}{
				"type":  "object",
				"title": "list_recent_emailsOutput",
				"properties": map[string]interface{}{
					"result": map[string]interface{}{
						"title": "Result",
						"type":  "array",
						"items": map[string]interface{}{"type": "object", "additionalProperties": true},
					},
				},
				"required": []string{"result"},
			},
		},
		{
			"name":        "search_emails",
			"description": "Full-text search indexed kernel emails using SQLite FTS5 with matched snippets.",
			"inputSchema": map[string]interface{}{
				"type":  "object",
				"title": "search_emailsArguments",
				"properties": map[string]interface{}{
					"query": map[string]interface{}{"title": "Query", "type": "string"},
					"limit": map[string]interface{}{"title": "Limit", "type": "integer", "default": 20},
				},
				"required": []string{"query"},
			},
			"outputSchema": map[string]interface{}{
				"type":  "object",
				"title": "search_emailsOutput",
				"properties": map[string]interface{}{
					"result": map[string]interface{}{
						"title": "Result",
						"type":  "array",
						"items": map[string]interface{}{"type": "object", "additionalProperties": true},
					},
				},
				"required": []string{"result"},
			},
		},
		{
			"name":        "get_email",
			"description": "Find an email by Message-ID (or database ID) and retrieve its metadata and complete plaintext body.",
			"inputSchema": map[string]interface{}{
				"type":  "object",
				"title": "get_emailArguments",
				"properties": map[string]interface{}{
					"message_id": map[string]interface{}{
						"title":       "Message Id",
						"type":        "string",
						"description": "Message-ID (with or without angle brackets <...>) or numeric database ID.",
					},
					"id": map[string]interface{}{
						"title":       "Id",
						"type":        "string",
						"description": "Alias for message_id.",
					},
				},
				"required": []string{"message_id"},
			},
			"outputSchema": map[string]interface{}{
				"type":  "object",
				"title": "get_emailOutput",
				"properties": map[string]interface{}{
					"result": map[string]interface{}{
						"title":                "Result",
						"type":                 "object",
						"additionalProperties": true,
					},
				},
				"required": []string{"result"},
			},
		},
		{
			"name":        "get_message",
			"description": "Find an email by Message-ID (or database ID) and retrieve its metadata and complete plaintext body (alias for get_email).",
			"inputSchema": map[string]interface{}{
				"type":  "object",
				"title": "get_messageArguments",
				"properties": map[string]interface{}{
					"message_id": map[string]interface{}{
						"title":       "Message Id",
						"type":        "string",
						"description": "Message-ID (with or without angle brackets <...>) or numeric database ID.",
					},
					"id": map[string]interface{}{
						"title":       "Id",
						"type":        "string",
						"description": "Alias for message_id.",
					},
				},
				"required": []string{"message_id"},
			},
			"outputSchema": map[string]interface{}{
				"type":  "object",
				"title": "get_messageOutput",
				"properties": map[string]interface{}{
					"result": map[string]interface{}{
						"title":                "Result",
						"type":                 "object",
						"additionalProperties": true,
					},
				},
				"required": []string{"result"},
			},
		},
		{
			"name":        "get_thread_context",
			"description": "Retrieve the entire thread tree for a given message ID including plaintext bodies.",
			"inputSchema": map[string]interface{}{
				"type":  "object",
				"title": "get_thread_contextArguments",
				"properties": map[string]interface{}{
					"message_id": map[string]interface{}{"title": "Message Id", "type": "string"},
					"id":         map[string]interface{}{"title": "Id", "type": "string"},
				},
				"required": []string{"message_id"},
			},
			"outputSchema": map[string]interface{}{
				"type":  "object",
				"title": "get_thread_contextOutput",
				"properties": map[string]interface{}{
					"result": map[string]interface{}{
						"title": "Result",
						"type":  "array",
						"items": map[string]interface{}{"type": "object", "additionalProperties": true},
					},
				},
				"required": []string{"result"},
			},
		},
		{
			"name":        "import_patch_series_b4",
			"description": "Download patch series or thread from lore.kernel.org via b4 and import into local store.",
			"inputSchema": map[string]interface{}{
				"type":  "object",
				"title": "import_patch_series_b4Arguments",
				"properties": map[string]interface{}{
					"message_id_or_url": map[string]interface{}{"title": "Message Id Or Url", "type": "string"},
				},
				"required": []string{"message_id_or_url"},
			},
			"outputSchema": map[string]interface{}{
				"type":  "object",
				"title": "import_patch_series_b4Output",
				"properties": map[string]interface{}{
					"result": map[string]interface{}{
						"title":                "Result",
						"type":                 "object",
						"additionalProperties": true,
					},
				},
				"required": []string{"result"},
			},
		},
		{
			"name": "create_review_draft",
			"description": "Convert structured review comments into an LKML quoted reply in the local Drafts folder.\n\n        " +
				"inline_notes format: [{\"file\": \"path/file.c\" (or \"DESCRIPTION\"), \"line_no\": 123, \"comment\": \"Review text...\"}]\n        ",
			"inputSchema": map[string]interface{}{
				"type":  "object",
				"title": "create_review_draftArguments",
				"properties": map[string]interface{}{
					"target_message_id": map[string]interface{}{"title": "Target Message Id", "type": "string"},
					"general_feedback":  map[string]interface{}{"title": "General Feedback", "type": "string"},
					"inline_notes": map[string]interface{}{
						"title": "Inline Notes",
						"type":  "array",
						"items": map[string]interface{}{"type": "object", "additionalProperties": true},
					},
				},
				"required": []string{"target_message_id"},
			},
			"outputSchema": map[string]interface{}{
				"type":  "object",
				"title": "create_review_draftOutput",
				"properties": map[string]interface{}{
					"result": map[string]interface{}{
						"title":                "Result",
						"type":                 "object",
						"additionalProperties": true,
					},
				},
				"required": []string{"result"},
			},
		},
		{
			"name": "write_draft_message",
			"description": "Write a draft email message into the local Maildir Drafts folder for user review, editing, and dispatch.\n\n" +
				"Supports drafting replies to existing messages (auto-deriving To, Cc, Subject, In-Reply-To, References) or composing new standalone messages.\n" +
				"If in_reply_to (or target_message_id) is provided, recipient addresses and subject are automatically derived from the target message if omitted.\n" +
				"Optionally accepts inline_notes to automatically format and merge line-specific review comments into LKML quote format.\n\n" +
				"Args:\n" +
				"  body: Plain-text body of the draft email (or use body_text / general_feedback).\n" +
				"  in_reply_to: Message-ID (or DB ID) of the message being replied to (optional, alias: target_message_id).\n" +
				"  to: Recipient email address(es) (optional if in_reply_to is given, alias: to_addr).\n" +
				"  subject: Subject line (optional if in_reply_to is given).\n" +
				"  cc: CC recipient email address(es) (optional, alias: cc_addr).\n" +
				"  references: References header value (optional, auto-derived if replying).\n" +
				"  inline_notes: Optional array of [{\"file\": \"...\", \"line_no\": 123, \"comment\": \"...\"}] to merge as LKML quotes into the draft.\n" +
				"  append_signature: Boolean, whether to append configured ~/.signature (default: false).\n",
			"inputSchema": map[string]interface{}{
				"type":  "object",
				"title": "write_draft_messageArguments",
				"properties": map[string]interface{}{
					"body":              map[string]interface{}{"title": "Body", "type": "string"},
					"body_text":         map[string]interface{}{"title": "Body Text", "type": "string"},
					"general_feedback":  map[string]interface{}{"title": "General Feedback", "type": "string"},
					"in_reply_to":       map[string]interface{}{"title": "In Reply To", "type": "string"},
					"target_message_id": map[string]interface{}{"title": "Target Message Id", "type": "string"},
					"reply_to":          map[string]interface{}{"title": "Reply To", "type": "string"},
					"to":                map[string]interface{}{"title": "To", "type": "string"},
					"to_addr":           map[string]interface{}{"title": "To Addr", "type": "string"},
					"subject":           map[string]interface{}{"title": "Subject", "type": "string"},
					"cc":                map[string]interface{}{"title": "Cc", "type": "string"},
					"cc_addr":           map[string]interface{}{"title": "Cc Addr", "type": "string"},
					"references":        map[string]interface{}{"title": "References", "type": "string"},
					"inline_notes": map[string]interface{}{
						"title": "Inline Notes",
						"type":  "array",
						"items": map[string]interface{}{"type": "object", "additionalProperties": true},
					},
					"append_signature": map[string]interface{}{"title": "Append Signature", "type": "boolean", "default": false},
				},
			},
			"outputSchema": map[string]interface{}{
				"type":  "object",
				"title": "write_draft_messageOutput",
				"properties": map[string]interface{}{
					"result": map[string]interface{}{
						"title":                "Result",
						"type":                 "object",
						"additionalProperties": true,
					},
				},
				"required": []string{"result"},
			},
		},
		{
			"name":        "write_draft",
			"description": "Write a draft email message into the local Maildir Drafts folder (alias for write_draft_message).",
			"inputSchema": map[string]interface{}{
				"type":  "object",
				"title": "write_draftArguments",
				"properties": map[string]interface{}{
					"body":              map[string]interface{}{"title": "Body", "type": "string"},
					"in_reply_to":       map[string]interface{}{"title": "In Reply To", "type": "string"},
					"target_message_id": map[string]interface{}{"title": "Target Message Id", "type": "string"},
					"to":                map[string]interface{}{"title": "To", "type": "string"},
					"subject":           map[string]interface{}{"title": "Subject", "type": "string"},
					"cc":                map[string]interface{}{"title": "Cc", "type": "string"},
				},
			},
			"outputSchema": map[string]interface{}{
				"type":  "object",
				"title": "write_draftOutput",
				"properties": map[string]interface{}{
					"result": map[string]interface{}{
						"title":                "Result",
						"type":                 "object",
						"additionalProperties": true,
					},
				},
				"required": []string{"result"},
			},
		},
		{
			"name":        "save_draft",
			"description": "Save an email draft to the local Drafts folder (alias for write_draft_message).",
			"inputSchema": map[string]interface{}{
				"type":  "object",
				"title": "save_draftArguments",
				"properties": map[string]interface{}{
					"body":              map[string]interface{}{"title": "Body", "type": "string"},
					"in_reply_to":       map[string]interface{}{"title": "In Reply To", "type": "string"},
					"target_message_id": map[string]interface{}{"title": "Target Message Id", "type": "string"},
					"to":                map[string]interface{}{"title": "To", "type": "string"},
					"subject":           map[string]interface{}{"title": "Subject", "type": "string"},
					"cc":                map[string]interface{}{"title": "Cc", "type": "string"},
				},
			},
			"outputSchema": map[string]interface{}{
				"type":  "object",
				"title": "save_draftOutput",
				"properties": map[string]interface{}{
					"result": map[string]interface{}{
						"title":                "Result",
						"type":                 "object",
						"additionalProperties": true,
					},
				},
				"required": []string{"result"},
			},
		},
		{
			"name":        "get_patchwork_patch_info",
			"description": "Query Patchwork (patchwork.kernel.org) for a message to check if it's delegated to the user, its state, CI checks, and series info.",
			"inputSchema": map[string]interface{}{
				"type":  "object",
				"title": "get_patchwork_patch_infoArguments",
				"properties": map[string]interface{}{
					"message_id": map[string]interface{}{"title": "Message Id", "type": "string"},
				},
				"required": []string{"message_id"},
			},
			"outputSchema": map[string]interface{}{
				"type":  "object",
				"title": "get_patchwork_patch_infoOutput",
				"properties": map[string]interface{}{
					"result": map[string]interface{}{
						"title":                "Result",
						"type":                 "object",
						"additionalProperties": true,
					},
				},
				"required": []string{"result"},
			},
		},
		{
			"name":        "list_my_delegated_patches",
			"description": "List patches currently delegated to the user on Patchwork (patchwork.kernel.org).",
			"inputSchema": map[string]interface{}{
				"type":  "object",
				"title": "list_my_delegated_patchesArguments",
				"properties": map[string]interface{}{
					"state":   map[string]interface{}{"title": "State", "anyOf": []map[string]interface{}{{"type": "string"}, {"type": "null"}}, "default": nil},
					"project": map[string]interface{}{"title": "Project", "anyOf": []map[string]interface{}{{"type": "string"}, {"type": "null"}}, "default": nil},
					"limit":   map[string]interface{}{"title": "Limit", "type": "integer", "default": 20},
				},
			},
			"outputSchema": map[string]interface{}{
				"type":  "object",
				"title": "list_my_delegated_patchesOutput",
				"properties": map[string]interface{}{
					"result": map[string]interface{}{
						"title": "Result",
						"type":  "array",
						"items": map[string]interface{}{"type": "object", "additionalProperties": true},
					},
				},
				"required": []string{"result"},
			},
		},
		{
			"name":        "update_patchwork_patch",
			"description": "Update patch state (e.g. 'under-review', 'accepted', 'changes-requested') or delegate the patch to the current user.",
			"inputSchema": map[string]interface{}{
				"type":  "object",
				"title": "update_patchwork_patchArguments",
				"properties": map[string]interface{}{
					"patch_id":       map[string]interface{}{"title": "Patch Id", "type": "integer"},
					"state":          map[string]interface{}{"title": "State", "anyOf": []map[string]interface{}{{"type": "string"}, {"type": "null"}}, "default": nil},
					"delegate_to_me": map[string]interface{}{"title": "Delegate To Me", "type": "boolean", "default": false},
				},
				"required": []string{"patch_id"},
			},
			"outputSchema": map[string]interface{}{
				"type":  "object",
				"title": "update_patchwork_patchOutput",
				"properties": map[string]interface{}{
					"result": map[string]interface{}{
						"title":                "Result",
						"type":                 "object",
						"additionalProperties": true,
					},
				},
				"required": []string{"result"},
			},
		},
		{
			"name": "post_review_comment",
			"description": "Post an inline review comment onto a patch message by specifying the target FILE:LINE.\n\n        " +
				"Args:\n            message_id: Message-ID (or database ID) of the target patch email.\n            " +
				"target: Target coordinate in \"FILE:LINE\" format (e.g. \"kernel/trace/ftrace.c:51\"). To comment on lines in the message body / patch description, specify \"DESCRIPTION:LINE\" (e.g. \"DESCRIPTION:12\").\n            " +
				"comment: Review comment text.\n            " +
				"side: Diff side: \"new\" (post-image/additions) or \"old\" (pre-image/deletions). Defaults to \"new\".\n            " +
				"target_message_id: Alias for message_id for backward compatibility.\n        ",
			"inputSchema": map[string]interface{}{
				"type":  "object",
				"title": "post_review_commentArguments",
				"properties": map[string]interface{}{
					"message_id":        map[string]interface{}{"title": "Message Id", "anyOf": []map[string]interface{}{{"type": "string"}, {"type": "null"}}, "default": nil},
					"target":            map[string]interface{}{"title": "Target", "type": "string", "default": ""},
					"comment":           map[string]interface{}{"title": "Comment", "type": "string", "default": ""},
					"side":              map[string]interface{}{"title": "Side", "anyOf": []map[string]interface{}{{"type": "string"}, {"type": "null"}}, "default": "new"},
					"state":             map[string]interface{}{"title": "State", "anyOf": []map[string]interface{}{{"type": "string"}, {"type": "null"}}, "default": nil},
					"target_message_id": map[string]interface{}{"title": "Target Message Id", "anyOf": []map[string]interface{}{{"type": "string"}, {"type": "null"}}, "default": nil},
				},
			},
			"outputSchema": map[string]interface{}{
				"type":  "object",
				"title": "post_review_commentOutput",
				"properties": map[string]interface{}{
					"result": map[string]interface{}{
						"title":                "Result",
						"type":                 "object",
						"additionalProperties": true,
					},
				},
				"required": []string{"result"},
			},
		},
		{
			"name":        "post_diff_comment",
			"description": "Post an inline review comment onto a patch message or description (alias for post_review_comment, supports FILE:LINE and DESCRIPTION:LINE).",
			"inputSchema": map[string]interface{}{
				"type":  "object",
				"title": "post_diff_commentArguments",
				"properties": map[string]interface{}{
					"message_id":        map[string]interface{}{"title": "Message Id", "anyOf": []map[string]interface{}{{"type": "string"}, {"type": "null"}}, "default": nil},
					"target":            map[string]interface{}{"title": "Target", "type": "string", "default": ""},
					"comment":           map[string]interface{}{"title": "Comment", "type": "string", "default": ""},
					"side":              map[string]interface{}{"title": "Side", "anyOf": []map[string]interface{}{{"type": "string"}, {"type": "null"}}, "default": "new"},
					"target_message_id": map[string]interface{}{"title": "Target Message Id", "anyOf": []map[string]interface{}{{"type": "string"}, {"type": "null"}}, "default": nil},
				},
			},
			"outputSchema": map[string]interface{}{
				"type":  "object",
				"title": "post_diff_commentOutput",
				"properties": map[string]interface{}{
					"result": map[string]interface{}{
						"title":                "Result",
						"type":                 "object",
						"additionalProperties": true,
					},
				},
				"required": []string{"result"},
			},
		},
		{
			"name":        "add_diff_comment",
			"description": "Add an inline review comment onto a patch message or description (alias for post_review_comment, supports FILE:LINE and DESCRIPTION:LINE).",
			"inputSchema": map[string]interface{}{
				"type":  "object",
				"title": "add_diff_commentArguments",
				"properties": map[string]interface{}{
					"message_id":        map[string]interface{}{"title": "Message Id", "anyOf": []map[string]interface{}{{"type": "string"}, {"type": "null"}}, "default": nil},
					"target":            map[string]interface{}{"title": "Target", "type": "string", "default": ""},
					"comment":           map[string]interface{}{"title": "Comment", "type": "string", "default": ""},
					"side":              map[string]interface{}{"title": "Side", "anyOf": []map[string]interface{}{{"type": "string"}, {"type": "null"}}, "default": "new"},
					"target_message_id": map[string]interface{}{"title": "Target Message Id", "anyOf": []map[string]interface{}{{"type": "string"}, {"type": "null"}}, "default": nil},
				},
			},
			"outputSchema": map[string]interface{}{
				"type":  "object",
				"title": "add_diff_commentOutput",
				"properties": map[string]interface{}{
					"result": map[string]interface{}{
						"title":                "Result",
						"type":                 "object",
						"additionalProperties": true,
					},
				},
				"required": []string{"result"},
			},
		},
	}
}

// CallTool executes a named tool with provided arguments and returns structured result.
func (s *Server) CallTool(ctx context.Context, name string, args map[string]interface{}) (interface{}, error) {
	if args == nil {
		args = make(map[string]interface{})
	}

	switch name {
	case "list_recent_emails":
		return s.toolListRecentEmails(args)
	case "search_emails":
		return s.toolSearchEmails(args)
	case "get_email", "get_message", "fetch_email", "read_email":
		return s.toolGetEmail(args)
	case "get_thread_context":
		return s.toolGetThreadContext(args)
	case "import_patch_series_b4":
		return s.toolImportPatchSeriesB4(ctx, args)
	case "write_draft_message", "write_draft", "save_draft", "create_draft", "save_draft_message":
		return s.toolWriteDraftMessage(name, args)
	case "create_review_draft":
		return s.toolCreateReviewDraft(args)
	case "get_patchwork_patch_info":
		return s.toolGetPatchworkPatchInfo(ctx, args)
	case "list_my_delegated_patches":
		return s.toolListMyDelegatedPatches(ctx, args)
	case "update_patchwork_patch":
		return s.toolUpdatePatchworkPatch(ctx, args)
	case "post_review_comment", "post_diff_comment", "add_diff_comment":
		return s.toolPostReviewComment(name, args)
	default:
		return nil, fmt.Errorf("unknown tool: %s", name)
	}
}

func (s *Server) toolListRecentEmails(args map[string]interface{}) (interface{}, error) {
	t0 := time.Now()
	folderStr := getStringPtr(args, "folder")
	limit := getInt(args, "limit", 20)

	var folderID *int64
	if folderStr != nil && *folderStr != "" && s.DB != nil {
		fInfo, err := s.DB.GetFolderByName(*folderStr)
		if err == nil && fInfo != nil {
			folderID = &fInfo.ID
		}
	}

	var results []map[string]interface{}
	if s.DB != nil {
		msgs, err := s.DB.ListMessages(folderID, limit, 0)
		if err == nil {
			for _, m := range msgs {
				results = append(results, map[string]interface{}{
					"id":         m.ID,
					"message_id": m.MessageID,
					"subject":    m.Subject,
					"from_addr":  m.FromAddr,
					"from_name":  m.FromName,
					"date":       m.Date,
					"is_patch":   m.IsPatch,
					"is_read":    m.IsRead,
				})
			}
		}
	}

	folderDisplay := "None"
	if folderStr != nil {
		folderDisplay = fmt.Sprintf("%q", *folderStr)
	}
	durMs := time.Since(t0).Seconds() * 1000.0
	s.writeAccessLog("INFO", fmt.Sprintf("MCP access: list_recent_emails(folder=%s, limit=%d) -> %d messages (%.1fms)",
		folderDisplay, limit, len(results), durMs))

	return results, nil
}

func (s *Server) toolSearchEmails(args map[string]interface{}) (interface{}, error) {
	t0 := time.Now()
	query := getString(args, "query")
	limit := getInt(args, "limit", 20)

	var matches []*database.SearchResult
	if s.DB != nil && query != "" {
		res, err := s.DB.Search(query, limit)
		if err == nil {
			matches = res
		}
	}

	durMs := time.Since(t0).Seconds() * 1000.0
	s.writeAccessLog("INFO", fmt.Sprintf("MCP access: search_emails(query=%q, limit=%d) -> %d matches (%.1fms)",
		query, limit, len(matches), durMs))

	return matches, nil
}

func (s *Server) resolveMessage(idOrMsgID string) (*database.Message, error) {
	if s.DB == nil {
		return nil, fmt.Errorf("database not initialized")
	}
	idOrMsgID = strings.TrimSpace(idOrMsgID)
	if idOrMsgID == "" {
		return nil, fmt.Errorf("message ID cannot be empty")
	}

	// 1. Try numeric DB ID
	if idVal, err := strconv.ParseInt(idOrMsgID, 10, 64); err == nil {
		if msg, err := s.DB.GetMessageByID(idVal); err == nil && msg != nil {
			s.hydrateMessageBody(msg)
			return msg, nil
		}
	}

	// 2. Try cleaned Message-ID (strips angle brackets <...>)
	cleaned := parser.CleanMsgID(idOrMsgID)
	if cleaned != "" {
		if msg, err := s.DB.GetMessageByMsgID(cleaned); err == nil && msg != nil {
			s.hydrateMessageBody(msg)
			return msg, nil
		}
	}

	// 3. Try raw idOrMsgID if different from cleaned
	if cleaned != idOrMsgID {
		if msg, err := s.DB.GetMessageByMsgID(idOrMsgID); err == nil && msg != nil {
			s.hydrateMessageBody(msg)
			return msg, nil
		}
	}

	return nil, fmt.Errorf("message not found: %s", idOrMsgID)
}

func (s *Server) hydrateMessageBody(m *database.Message) {
	if m == nil || m.BodyPlain != "" || m.FilePath == "" {
		return
	}
	if data, err := os.ReadFile(m.FilePath); err == nil {
		if parsed, err := parser.ParseEmail(data); err == nil && parsed.BodyPlain != "" {
			m.BodyPlain = parsed.BodyPlain
			_ = s.DB.UpdateMessageSubjectAndBody(m.ID, m.Subject, m.BodyPlain)
		}
	}
}

func (s *Server) toolGetEmail(args map[string]interface{}) (interface{}, error) {
	t0 := time.Now()
	msgID := getString(args, "message_id")
	if msgID == "" {
		msgID = getString(args, "id")
	}

	if s.DB == nil {
		durMs := time.Since(t0).Seconds() * 1000.0
		s.writeAccessLog("WARNING", fmt.Sprintf("MCP access: get_email(%q) -> ERROR: Database not initialized (%.1fms)", msgID, durMs))
		return map[string]interface{}{
			"found": false,
			"error": "Database not initialized",
		}, nil
	}

	msg, err := s.resolveMessage(msgID)
	durMs := time.Since(t0).Seconds() * 1000.0
	if err != nil || msg == nil {
		s.writeAccessLog("WARNING", fmt.Sprintf("MCP access: get_email(%q) -> NOT FOUND (%.1fms)", msgID, durMs))
		return map[string]interface{}{
			"found":      false,
			"message_id": msgID,
			"error":      fmt.Sprintf("Message not found: %s", msgID),
		}, nil
	}

	s.writeAccessLog("INFO", fmt.Sprintf("MCP access: get_email(%q) -> SUCCESS (id=%d, subject=%q) (%.1fms)",
		msgID, msg.ID, msg.Subject, durMs))

	return map[string]interface{}{
		"found":            true,
		"id":               msg.ID,
		"message_id":       msg.MessageID,
		"folder_id":        msg.FolderID,
		"folder_name":      msg.FolderName,
		"subject":          msg.Subject,
		"from_name":        msg.FromName,
		"from_addr":        msg.FromAddr,
		"to_addr":          msg.ToAddr,
		"cc_addr":          msg.CcAddr,
		"date":             msg.Date,
		"epoch_timestamp":  msg.EpochTimestamp,
		"in_reply_to":      msg.InReplyTo,
		"references":       msg.ReferencesHeader,
		"body_plain":       msg.BodyPlain,
		"is_patch":         msg.IsPatch,
		"is_read":          msg.IsRead,
		"is_replied":       msg.IsReplied,
		"has_notes":        msg.HasNotes,
		"file_path":        msg.FilePath,
		"review_commit_id": msg.ReviewCommitID,
	}, nil
}

func (s *Server) toolGetThreadContext(args map[string]interface{}) (interface{}, error) {
	t0 := time.Now()
	msgID := getString(args, "message_id")
	if msgID == "" {
		msgID = getString(args, "id")
	}

	var results []map[string]interface{}
	if s.DB != nil && msgID != "" {
		orig, _ := s.resolveMessage(msgID)

		var msgs []*database.Message
		if orig != nil {
			msgs, _ = s.DB.GetThreadHierarchy(orig.MessageID)
		} else {
			msgs, _ = s.DB.GetThreadHierarchy(msgID)
		}

		foundOrig := false
		for _, m := range msgs {
			if orig != nil && m.ID == orig.ID {
				foundOrig = true
				break
			}
		}
		if !foundOrig && orig != nil {
			msgs = append([]*database.Message{orig}, msgs...)
		}

		for _, m := range msgs {
			s.hydrateMessageBody(m)
			results = append(results, map[string]interface{}{
				"id":          m.ID,
				"message_id":  m.MessageID,
				"folder_name": m.FolderName,
				"subject":     m.Subject,
				"from_name":   m.FromName,
				"from_addr":   m.FromAddr,
				"to_addr":     m.ToAddr,
				"cc_addr":     m.CcAddr,
				"date":        m.Date,
				"in_reply_to": m.InReplyTo,
				"references":  m.ReferencesHeader,
				"body_plain":  m.BodyPlain,
				"is_patch":    m.IsPatch,
			})
		}
	}

	durMs := time.Since(t0).Seconds() * 1000.0
	s.writeAccessLog("INFO", fmt.Sprintf("MCP access: get_thread_context(message_id=%q) -> %d messages in thread (%.1fms)",
		msgID, len(results), durMs))

	return results, nil
}

func (s *Server) toolImportPatchSeriesB4(ctx context.Context, args map[string]interface{}) (interface{}, error) {
	t0 := time.Now()
	msgIDOrURL := getString(args, "message_id_or_url")

	if s.B4 == nil {
		durMs := time.Since(t0).Seconds() * 1000.0
		s.writeAccessLog("WARNING", fmt.Sprintf("MCP access: import_patch_series_b4(%q) -> ERROR: b4 runner not initialized (%.1fms)", msgIDOrURL, durMs))
		return map[string]interface{}{
			"success":    false,
			"message_id": msgIDOrURL,
			"error":      "b4 runner not initialized",
		}, nil
	}

	ids, err := s.B4.ImportThread(msgIDOrURL)
	durMs := time.Since(t0).Seconds() * 1000.0
	if err != nil {
		s.writeAccessLog("WARNING", fmt.Sprintf("MCP access: import_patch_series_b4(%q) -> ERROR: %v (%.1fms)", msgIDOrURL, err, durMs))
		return map[string]interface{}{
			"success":    false,
			"message_id": msgIDOrURL,
			"error":      err.Error(),
		}, nil
	}

	s.writeAccessLog("INFO", fmt.Sprintf("MCP access: import_patch_series_b4(%q) -> SUCCESS (imported %d messages) (%.1fms)", msgIDOrURL, len(ids), durMs))
	return map[string]interface{}{
		"success":        true,
		"message_id":     msgIDOrURL,
		"imported_count": len(ids),
		"indexed_ids":    ids,
	}, nil
}

func (s *Server) toolWriteDraftMessage(toolName string, args map[string]interface{}) (interface{}, error) {
	t0 := time.Now()
	targetMsgID := getString(args, "in_reply_to", "target_message_id", "reply_to", "reply_to_message_id", "message_id")
	bodyText := getString(args, "body", "body_text", "general_feedback", "text", "content")
	toAddr := getString(args, "to", "to_addr")
	ccAddr := getString(args, "cc", "cc_addr")
	subject := getString(args, "subject")
	references := getString(args, "references")
	customFrom := getString(args, "custom_from", "from")
	rawNotes := getSlice(args, "inline_notes")
	appendSig := getBool(args, "append_signature", false)

	if s.DB == nil || s.Maildir == nil {
		durMs := time.Since(t0).Seconds() * 1000.0
		s.writeAccessLog("WARNING", fmt.Sprintf("MCP access: %s() -> REJECTED (Storage not initialized) (%.1fms)", toolName, durMs))
		return map[string]interface{}{"success": false, "error": "Storage not initialized"}, nil
	}

	var orig *database.Message
	if targetMsgID != "" {
		orig, _ = s.resolveMessage(targetMsgID)
	}

	if targetMsgID != "" && orig == nil && (toAddr == "" || subject == "") {
		durMs := time.Since(t0).Seconds() * 1000.0
		s.writeAccessLog("WARNING", fmt.Sprintf("MCP access: %s(target_message_id=%q) -> REJECTED (Message not found) (%.1fms)", toolName, targetMsgID, durMs))
		return map[string]interface{}{"success": false, "error": fmt.Sprintf("Message not found: %s", targetMsgID)}, nil
	}

	fromEmail := "dev@example.com"
	if s.Config != nil && s.Config.SMTP.FromEmail != "" {
		fromEmail = s.Config.SMTP.FromEmail
	}

	inReplyToHdr := ""
	if orig != nil {
		if toAddr == "" {
			autoTo, autoCc := parser.BuildReplyRecipients(orig.FromAddr, orig.ToAddr, orig.CcAddr, fromEmail)
			toAddr = autoTo
			if ccAddr == "" {
				ccAddr = autoCc
			}
		}
		if subject == "" {
			cleanSubj := strings.TrimSpace(orig.Subject)
			if !strings.HasPrefix(strings.ToLower(cleanSubj), "re:") {
				subject = "Re: " + cleanSubj
			} else {
				subject = cleanSubj
			}
		}
		parentID := parser.CleanMsgID(orig.MessageID)
		if parentID != "" {
			inReplyToHdr = fmt.Sprintf("<%s>", parentID)
		}
		if references == "" {
			parentRefs := strings.TrimSpace(orig.ReferencesHeader)
			if parentRefs != "" {
				if parentID != "" && !strings.Contains(parentRefs, parentID) {
					references = fmt.Sprintf("%s <%s>", parentRefs, parentID)
				} else {
					references = parentRefs
				}
			} else if parentID != "" {
				references = fmt.Sprintf("<%s>", parentID)
			}
		}
	} else if targetMsgID != "" {
		cleaned := parser.CleanMsgID(targetMsgID)
		if cleaned != "" {
			inReplyToHdr = fmt.Sprintf("<%s>", cleaned)
			if references == "" {
				references = fmt.Sprintf("<%s>", cleaned)
			}
		}
	}

	// Merge structured inline notes if provided and original message exists
	if len(rawNotes) > 0 && orig != nil {
		var notes []quote.InlineNote
		for _, n := range rawNotes {
			if m, ok := n.(map[string]interface{}); ok {
				file := getString(m, "file", "path")
				if strings.EqualFold(file, "DESCRIPTION") {
					file = "DESCRIPTION"
				}
				lineNo := getInt(m, "line_no", 0)
				if lineNo == 0 {
					lineNo = getInt(m, "lineNo", 0)
				}
				side := getString(m, "side")
				text := getString(m, "comment", "text")
				notes = append(notes, quote.InlineNote{
					File:   file,
					LineNo: lineNo,
					Side:   side,
					Text:   text,
				})
			}
		}
		if len(notes) > 0 {
			parsed := patch.ParsePatch(orig.BodyPlain)
			sig := ""
			if s.Config != nil {
				sig = s.Config.SMTP.GetSignature()
			}
			bodyText = quote.MergePatchReviewQuote(
				orig.Date, orig.FromName, orig.FromAddr, orig.BodyPlain,
				parsed, notes, bodyText, sig,
			)
		}
	}

	if appendSig && s.Config != nil {
		sig := s.Config.SMTP.GetSignature()
		if sig != "" && !strings.Contains(bodyText, strings.TrimSpace(sig)) {
			bodyText = strings.TrimRight(bodyText, "\n") + "\n\n" + sig + "\n"
		}
	}

	if strings.TrimSpace(bodyText) == "" {
		durMs := time.Since(t0).Seconds() * 1000.0
		s.writeAccessLog("WARNING", fmt.Sprintf("MCP access: %s() -> REJECTED (Empty body) (%.1fms)", toolName, durMs))
		return map[string]interface{}{"success": false, "error": "Draft body text is required"}, nil
	}

	if strings.TrimSpace(subject) == "" {
		subject = "(no subject)"
	}

	// Construct RFC 822 email via SMTP Client's BuildMessage
	smtpClient := smtp.NewClient(s.Config, s.Maildir, s.DB)
	rawMsg := smtpClient.BuildMessage(toAddr, subject, bodyText, ccAddr, inReplyToHdr, references, customFrom)

	_ = s.Maildir.EnsureFolder(constants.FolderDrafts)
	draftFolder, err := s.DB.GetOrCreateFolder(constants.FolderDrafts, false)
	if err != nil {
		return map[string]interface{}{"success": false, "error": err.Error()}, nil
	}

	savedPath, err := s.Maildir.AddMessage(constants.FolderDrafts, []byte(rawMsg), constants.FlagDraft)
	if err != nil {
		return map[string]interface{}{"success": false, "error": err.Error()}, nil
	}

	dbID, err := s.DB.IndexEmailFile(draftFolder.ID, savedPath)
	if err != nil {
		return map[string]interface{}{"success": false, "error": err.Error()}, nil
	}

	msgID := ""
	if savedMsg, _ := s.DB.GetMessageByID(dbID); savedMsg != nil {
		msgID = savedMsg.MessageID
	}

	durMs := time.Since(t0).Seconds() * 1000.0
	s.writeAccessLog("INFO", fmt.Sprintf("MCP access: %s(subject=%q, to=%q) -> SUCCESS (draft_id=%d, msg_id=%q) (%.1fms)",
		toolName, subject, toAddr, dbID, msgID, durMs))

	return map[string]interface{}{
		"success":    true,
		"draft_id":   dbID,
		"message_id": msgID,
		"subject":    subject,
		"to":         toAddr,
		"cc":         ccAddr,
		"file_path":  savedPath,
	}, nil
}

func (s *Server) toolCreateReviewDraft(args map[string]interface{}) (interface{}, error) {
	return s.toolWriteDraftMessage("create_review_draft", args)
}

func (s *Server) toolGetPatchworkPatchInfo(ctx context.Context, args map[string]interface{}) (interface{}, error) {
	t0 := time.Now()
	msgID := getString(args, "message_id")

	if s.Patchwork == nil {
		return map[string]interface{}{"error": "Patchwork client not configured"}, nil
	}

	res, err := s.Patchwork.GetPatchInfo(msgID, "")
	durMs := time.Since(t0).Seconds() * 1000.0
	if err != nil {
		s.writeAccessLog("WARNING", fmt.Sprintf("MCP access: get_patchwork_patch_info(message_id=%q) -> ERROR: %v (%.1fms)", msgID, err, durMs))
		return map[string]interface{}{"error": err.Error()}, nil
	}

	if res == nil {
		res = map[string]interface{}{
			"found": false,
			"msgid": msgID,
		}
	}

	state, _ := res["state"].(string)
	delegated, _ := res["is_delegated_to_me"].(bool)
	s.writeAccessLog("INFO", fmt.Sprintf("MCP access: get_patchwork_patch_info(message_id=%q) -> state=%q, delegated=%v (%.1fms)",
		msgID, state, delegated, durMs))

	return res, nil
}

func (s *Server) toolListMyDelegatedPatches(ctx context.Context, args map[string]interface{}) (interface{}, error) {
	t0 := time.Now()
	state := getStringPtr(args, "state")
	project := getStringPtr(args, "project")
	limit := getInt(args, "limit", 20)
	fallbackEmail := ""
	if s.Config != nil {
		fallbackEmail = s.Config.SMTP.FromEmail
	}

	if s.Patchwork == nil {
		return []map[string]interface{}{}, nil
	}

	var states []string
	if state != nil && *state != "" {
		for _, st := range strings.Split(*state, ",") {
			if clean := strings.TrimSpace(st); clean != "" {
				states = append(states, clean)
			}
		}
	}
	proj := ""
	if project != nil {
		proj = *project
	}

	res, err := s.Patchwork.ListMyPatches(states, proj, limit, fallbackEmail)
	durMs := time.Since(t0).Seconds() * 1000.0
	if err != nil {
		s.writeAccessLog("WARNING", fmt.Sprintf("MCP access: list_my_delegated_patches() -> ERROR: %v (%.1fms)", err, durMs))
		return []map[string]interface{}{}, nil
	}

	stateDisp := "None"
	if state != nil {
		stateDisp = fmt.Sprintf("%q", *state)
	}
	projDisp := "None"
	if project != nil {
		projDisp = fmt.Sprintf("%q", *project)
	}
	s.writeAccessLog("INFO", fmt.Sprintf("MCP access: list_my_delegated_patches(state=%s, project=%s, limit=%d) -> %d patches (%.1fms)",
		stateDisp, projDisp, limit, len(res), durMs))

	return res, nil
}

func (s *Server) toolUpdatePatchworkPatch(ctx context.Context, args map[string]interface{}) (interface{}, error) {
	t0 := time.Now()
	patchID := getInt(args, "patch_id", 0)
	state := getStringPtr(args, "state")
	delegateToMe := getBool(args, "delegate_to_me", false)
	fallbackEmail := ""
	if s.Config != nil {
		fallbackEmail = s.Config.SMTP.FromEmail
	}

	if s.Patchwork == nil {
		return map[string]interface{}{"error": "Patchwork client not configured"}, nil
	}

	var res map[string]any
	var err error

	if delegateToMe {
		res, err = s.Patchwork.DelegateToMe(patchID, fallbackEmail)
	} else if state != nil {
		res, err = s.Patchwork.UpdatePatch(patchID, map[string]any{"state": *state})
	} else {
		res = map[string]any{"error": "Must specify either state or delegate_to_me=True"}
	}

	durMs := time.Since(t0).Seconds() * 1000.0
	if err != nil {
		s.writeAccessLog("WARNING", fmt.Sprintf("MCP access: update_patchwork_patch(patch_id=%d) -> ERROR: %v (%.1fms)", patchID, err, durMs))
		return map[string]interface{}{"error": err.Error()}, nil
	}

	stateDisp := "None"
	if state != nil {
		stateDisp = fmt.Sprintf("%q", *state)
	}
	s.writeAccessLog("INFO", fmt.Sprintf("MCP access: update_patchwork_patch(patch_id=%d, state=%s, delegate_to_me=%v) -> result=%v (%.1fms)",
		patchID, stateDisp, delegateToMe, res != nil, durMs))

	return res, nil
}

func (s *Server) toolPostReviewComment(toolName string, args map[string]interface{}) (interface{}, error) {
	t0 := time.Now()
	msgID := getString(args, "message_id")
	targetMsgID := getString(args, "target_message_id")
	target := getString(args, "target")
	comment := getString(args, "comment")
	side := getString(args, "side")
	statePtr := getStringPtr(args, "state")

	effectiveMsgID := strings.TrimSpace(msgID)
	if effectiveMsgID == "" {
		effectiveMsgID = strings.TrimSpace(targetMsgID)
	}

	if effectiveMsgID == "" {
		s.writeAccessLog("WARNING", fmt.Sprintf("MCP access: %s() -> REJECTED (Message ID cannot be empty)", toolName))
		return map[string]interface{}{"success": false, "error": "Message ID cannot be empty"}, nil
	}

	if !strings.Contains(target, ":") {
		durMs := time.Since(t0).Seconds() * 1000.0
		s.writeAccessLog("WARNING", fmt.Sprintf("MCP access: %s(%q, target=%q) -> REJECTED (Invalid target format) (%.1fms)",
			toolName, effectiveMsgID, target, durMs))
		return map[string]interface{}{"success": false, "error": fmt.Sprintf("Invalid target format '%s'. Expected 'FILE:LINE'.", target)}, nil
	}

	parts := strings.Split(target, ":")
	filePart := strings.TrimSpace(strings.Join(parts[:len(parts)-1], ":"))
	linePart := strings.TrimSpace(parts[len(parts)-1])

	if filePart == "" {
		durMs := time.Since(t0).Seconds() * 1000.0
		s.writeAccessLog("WARNING", fmt.Sprintf("MCP access: %s(%q, target=%q) -> REJECTED (Missing file part) (%.1fms)",
			toolName, effectiveMsgID, target, durMs))
		return map[string]interface{}{"success": false, "error": fmt.Sprintf("Missing file part in target '%s'.", target)}, nil
	}

	lineNo, err := strconv.Atoi(linePart)
	if err != nil {
		durMs := time.Since(t0).Seconds() * 1000.0
		s.writeAccessLog("WARNING", fmt.Sprintf("MCP access: %s(%q, target=%q) -> REJECTED (Invalid line number) (%.1fms)",
			toolName, effectiveMsgID, target, durMs))
		return map[string]interface{}{"success": false, "error": fmt.Sprintf("Invalid line number in target '%s'.", target)}, nil
	}

	if lineNo <= 0 {
		durMs := time.Since(t0).Seconds() * 1000.0
		s.writeAccessLog("WARNING", fmt.Sprintf("MCP access: %s(%q, target=%q) -> REJECTED (Non-positive line number) (%.1fms)",
			toolName, effectiveMsgID, target, durMs))
		return map[string]interface{}{"success": false, "error": fmt.Sprintf("Line number must be positive in target '%s'.", target)}, nil
	}

	isDesc := strings.EqualFold(filePart, "DESCRIPTION")
	if isDesc {
		filePart = "DESCRIPTION"
	} else {
		filePart = patch.NormalizePath(filePart)
	}

	if s.DB == nil {
		return map[string]interface{}{"success": false, "error": "Database not initialized"}, nil
	}

	orig, _ := s.resolveMessage(effectiveMsgID)
	if orig == nil {
		durMs := time.Since(t0).Seconds() * 1000.0
		s.writeAccessLog("WARNING", fmt.Sprintf("MCP access: %s(%q, target=%q) -> REJECTED (Message not found) (%.1fms)",
			toolName, effectiveMsgID, target, durMs))
		return map[string]interface{}{"success": false, "error": fmt.Sprintf("Message not found: %s", effectiveMsgID)}, nil
	}

	parsed := patch.ParsePatch(orig.BodyPlain)
	canonicalFile := filePart
	lineMapped := false
	chosenSide := strings.ToLower(strings.TrimSpace(side))
	if chosenSide == "" {
		chosenSide = "new"
	}

	if isDesc {
		canonicalFile = "DESCRIPTION"
		descText := ""
		if parsed.IsPatch && parsed.Preamble != "" {
			descText = parsed.Preamble
		} else {
			descText = orig.BodyPlain
		}
		descLines := strings.Split(descText, "\n")
		if lineNo >= 1 && lineNo <= len(descLines) {
			lineMapped = true
		}
	} else {
		var matchedFile *patch.DiffFile
		for _, f := range parsed.Files {
			if patch.FileMatches(filePart, f.NewPath) || patch.FileMatches(filePart, f.OldPath) {
				matchedFile = f
				break
			}
		}

		if matchedFile != nil {
			if matchedFile.NewPath != "" {
				canonicalFile = patch.NormalizePath(matchedFile.NewPath)
			} else if matchedFile.OldPath != "" {
				canonicalFile = patch.NormalizePath(matchedFile.OldPath)
			}

			for _, hunk := range matchedFile.Hunks {
				if chosenSide == "old" || chosenSide == "left" {
					for _, row := range hunk.PairedRows {
						if row.Left.LineNo != nil && *row.Left.LineNo == lineNo {
							lineMapped = true
							chosenSide = "old"
							break
						}
						if row.Right.LineNo != nil && *row.Right.LineNo == lineNo {
							lineMapped = true
							chosenSide = "new"
							break
						}
					}
				} else {
					for _, row := range hunk.PairedRows {
						if row.Right.LineNo != nil && *row.Right.LineNo == lineNo {
							lineMapped = true
							chosenSide = "new"
							break
						}
						if row.Left.LineNo != nil && *row.Left.LineNo == lineNo {
							lineMapped = true
							chosenSide = "old"
							break
						}
					}
				}
				if lineMapped {
					break
				}
			}
		}
	}

	cleanID := parser.CleanMsgID(orig.MessageID)
	commentState := "draft"
	if statePtr != nil && *statePtr != "" {
		commentState = *statePtr
	}

	commentRow, err := s.DB.SavePatchComment(&database.PatchComment{
		MessageID: cleanID,
		FilePath:  canonicalFile,
		LineNo:    lineNo,
		Side:      chosenSide,
		State:     commentState,
		Author:    "agent",
		Text:      strings.TrimSpace(comment),
	})
	if err != nil {
		return nil, err
	}
	_ = s.DB.SetMessageNotes(orig.ID, true)

	durMs := time.Since(t0).Seconds() * 1000.0
	s.writeAccessLog("INFO", fmt.Sprintf("MCP access: %s(%q, target=%s:%d, side=%q) -> SUCCESS (comment_id=%d, mapped=%v, state=%q) (%.1fms)",
		toolName, effectiveMsgID, canonicalFile, lineNo, chosenSide, commentRow.ID, lineMapped, commentState, durMs))

	return map[string]interface{}{
		"success":    true,
		"comment_id": commentRow.ID,
		"message_id": orig.MessageID,
		"file":       canonicalFile,
		"line_no":    lineNo,
		"side":       chosenSide,
		"comment":    strings.TrimSpace(comment),
		"mapped":     lineMapped,
	}, nil
}

// HandleSTDIO reads newline-delimited JSON-RPC requests from r and writes responses to w.
func (s *Server) HandleSTDIO(r io.Reader, w io.Writer) error {
	scanner := bufio.NewScanner(r)
	// Support up to 16MB per JSON message line
	const maxScanTokenSize = 16 * 1024 * 1024
	buf := make([]byte, 64*1024)
	scanner.Buffer(buf, maxScanTokenSize)

	for scanner.Scan() {
		line := bytes.TrimSpace(scanner.Bytes())
		if len(line) == 0 {
			continue
		}

		var req jsonRPCRequest
		if err := json.Unmarshal(line, &req); err != nil {
			errResp := jsonRPCResponse{
				JSONRPC: "2.0",
				Error: &jsonRPCError{
					Code:    -32700,
					Message: "Parse error",
				},
			}
			s.writeJSON(w, errResp)
			continue
		}

		// Notifications (no ID) do not receive a response
		if req.ID == nil {
			// E.g. notifications/initialized
			continue
		}

		switch req.Method {
		case "initialize":
			resp := jsonRPCResponse{
				JSONRPC: "2.0",
				ID:      req.ID,
				Result: map[string]interface{}{
					"protocolVersion": "2024-11-05",
					"capabilities": map[string]interface{}{
						"prompts":   map[string]interface{}{"listChanged": false},
						"resources": map[string]interface{}{"listChanged": false, "subscribe": false},
						"tools":     map[string]interface{}{"listChanged": false},
					},
					"serverInfo": map[string]interface{}{
						"name":    "undined",
						"version": "0.1",
					},
				},
			}
			s.writeJSON(w, resp)

		case "ping":
			resp := jsonRPCResponse{
				JSONRPC: "2.0",
				ID:      req.ID,
				Result:  map[string]interface{}{},
			}
			s.writeJSON(w, resp)

		case "tools/list":
			resp := jsonRPCResponse{
				JSONRPC: "2.0",
				ID:      req.ID,
				Result: map[string]interface{}{
					"tools": s.getToolsList(),
				},
			}
			s.writeJSON(w, resp)

		case "tools/call":
			var p toolCallParams
			if err := json.Unmarshal(req.Params, &p); err != nil {
				resp := jsonRPCResponse{
					JSONRPC: "2.0",
					ID:      req.ID,
					Error: &jsonRPCError{
						Code:    -32602,
						Message: "Invalid request parameters",
					},
				}
				s.writeJSON(w, resp)
				continue
			}

			result, err := s.CallTool(context.Background(), p.Name, p.Arguments)
			if err != nil {
				// Tool not found or execution failure
				resp := jsonRPCResponse{
					JSONRPC: "2.0",
					ID:      req.ID,
					Result: map[string]interface{}{
						"content": []map[string]interface{}{
							{
								"type": "text",
								"text": err.Error(),
							},
						},
						"isError": true,
					},
				}
				s.writeJSON(w, resp)
				continue
			}

			// Format standard MCP tool response
			var textContent string
			if b, err := json.MarshalIndent(result, "", "  "); err == nil {
				textContent = string(b)
			} else {
				textContent = fmt.Sprint(result)
			}

			resp := jsonRPCResponse{
				JSONRPC: "2.0",
				ID:      req.ID,
				Result: map[string]interface{}{
					"content": []map[string]interface{}{
						{
							"type": "text",
							"text": textContent,
						},
					},
					"isError": false,
					"structuredContent": map[string]interface{}{
						"result": result,
					},
				},
			}
			s.writeJSON(w, resp)

		default:
			resp := jsonRPCResponse{
				JSONRPC: "2.0",
				ID:      req.ID,
				Error: &jsonRPCError{
					Code:    -32601,
					Message: fmt.Sprintf("Method not found: %s", req.Method),
				},
			}
			s.writeJSON(w, resp)
		}
	}

	return scanner.Err()
}

func (s *Server) writeJSON(w io.Writer, v interface{}) {
	b, err := json.Marshal(v)
	if err == nil {
		b = append(b, '\n')
		_, _ = w.Write(b)
	}
}

// Helpers for extracting args
func getString(args map[string]interface{}, keys ...string) string {
	for _, k := range keys {
		if val, ok := args[k]; ok && val != nil {
			if s, ok := val.(string); ok {
				return s
			}
		}
	}
	return ""
}

func getStringPtr(args map[string]interface{}, keys ...string) *string {
	for _, k := range keys {
		if val, ok := args[k]; ok {
			if val == nil {
				return nil
			}
			if s, ok := val.(string); ok {
				return &s
			}
		}
	}
	return nil
}

func getInt(args map[string]interface{}, key string, defVal int) int {
	if val, ok := args[key]; ok && val != nil {
		switch v := val.(type) {
		case int:
			return v
		case int64:
			return int(v)
		case float64:
			return int(v)
		case json.Number:
			if i, err := v.Int64(); err == nil {
				return int(i)
			}
		case string:
			if i, err := strconv.Atoi(v); err == nil {
				return i
			}
		}
	}
	return defVal
}

func getBool(args map[string]interface{}, key string, defVal bool) bool {
	if val, ok := args[key]; ok && val != nil {
		if b, ok := val.(bool); ok {
			return b
		}
	}
	return defVal
}

func getSlice(args map[string]interface{}, key string) []interface{} {
	if val, ok := args[key]; ok && val != nil {
		if s, ok := val.([]interface{}); ok {
			return s
		}
	}
	return nil
}
