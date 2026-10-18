// Package constants defines shared constants and default settings for UndineD.
package constants

const (
	// Project Identity
	ProjectName    = "UndineD"
	DefaultXMailer = "UndineD 0.1"

	// Environment Variables
	EnvUndinedConfig       = "UNDINED_CONFIG"
	EnvLegacyUndineConfig = "UNDINE_CONFIG"
	EnvUndinedPort         = "UNDINED_PORT"
	EnvUndinedHost         = "UNDINED_HOST"
	EnvXDGConfigHome       = "XDG_CONFIG_HOME"
	EnvXDGDataHome         = "XDG_DATA_HOME"
	EnvXDGCacheHome        = "XDG_CACHE_HOME"

	// File and Path Names
	ConfigFileName     = "config.toml"
	ConfigExampleName  = "config.toml.example"
	LogFileName        = "undined.log"
	DBFileName         = "undined.db"
	AuthTokenFileName  = "auth_token"
	SessionCookieName  = "undine_session"
	PIDFileName        = "undined.pid"

	// Default Paths (unexpanded)
	DefaultConfigDir     = "~/.config/undined"
	DefaultConfigPath    = "~/.config/undined/config.toml"
	DefaultDataDir       = "~/.local/share/undined"
	DefaultMaildirPath   = "~/.local/share/undined/maildir"
	DefaultDBPath        = "~/.local/share/undined/undined.db"
	DefaultLogPath       = "~/.cache/undined/undined.log"
	DefaultSignaturePath = "~/.signature"
	DefaultKernelGitDir  = "~/linux"
	DefaultAuthTokenPath = "~/.local/share/undined/auth_token"
	DefaultPIDPath       = "~/.local/share/undined/undined.pid"

	// Standard Mailbox Folders
	FolderB4Imports = "b4-imports"
	FolderDrafts    = "Drafts"
	FolderTODO      = "TODO"
	FolderOutbox    = "Outbox" // Deprecated in favor of FolderTODO
	FolderSent      = "Sent"
	FolderAIReviews = "AI-reviews"
	FolderTrash     = "Trash"

	// Maildir Flags (RFC 2076 / Maildir Standard)
	FlagSeen    = "S" // Seen / Read
	FlagReplied = "R" // Replied
	FlagFlagged = "F" // Flagged / Important
	FlagDraft   = "D" // Draft
	FlagTrashed = "T" // Trashed
	FlagPassed  = "P" // Passed / Forwarded

	// Server & Network Defaults
	DefaultHost         = "127.0.0.1"
	DefaultPort         = 8080
	DefaultMCPPort      = 8081
	DefaultMCPTransport = "stdio"

	DefaultLogLevel        = "DEBUG"
	DefaultLogMaxBytes     = 10 * 1024 * 1024 // 10 MB
	DefaultLogBackupCount  = 5
	DefaultTerminalCmd     = "foot -e nvim +{line} {file}"

	// IMAP Defaults
	DefaultIMAPHost                = "imap.example.com"
	DefaultIMAPPort                = 993
	DefaultIMAPPollIntervalSeconds = 60
	MinIMAPPollIntervalSeconds     = 5

	// SMTP Defaults
	DefaultSMTPHost          = "smtp.example.com"
	DefaultSMTPPort          = 587
	DefaultSMTPSSLPort       = 465
	DefaultFromName          = "Kernel Developer"
	DefaultFromEmail         = "dev@example.com"
	DefaultContentType       = "text/plain; charset=utf-8"
	DefaultTransferEncoding  = "8bit"

	// LKML & b4 Defaults
	DefaultB4Cmd   = "b4"
	DefaultLoreURL = "https://lore.kernel.org/all"

	// Patchwork Defaults
	DefaultPatchworkURL            = "https://patchwork.kernel.org"
	DefaultPatchworkAPIVersion     = "1.4"
	DefaultPatchworkTimeoutSeconds = 10.0

	// AI Review Defaults
	DefaultReviewCmd    = "agy --print"
	DefaultReviewPrompt = "Review patch for message {message_id} (commit {commit_id}). Post diff review comments using the Undine MCP review comment tool."

	// Database Defaults
	DefaultSQLiteTimeoutSeconds = 30.0
)

// StandardFolders lists all system mailboxes maintained automatically.
var StandardFolders = []string{
	FolderB4Imports,
	FolderDrafts,
	FolderTODO,
	FolderSent,
	FolderAIReviews,
	FolderTrash,
}
