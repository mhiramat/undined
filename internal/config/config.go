// Package config handles loading, parsing, and validating TOML configuration for UndineD.
package config

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/pelletier/go-toml/v2"

	"undined-go/internal/constants"
)

// TagDefinition specifies display label and color for an email tag.
type TagDefinition struct {
	Tag   string `toml:"tag" json:"tag"`
	Color string `toml:"color" json:"color"`
}

// ResolvedTag contains key, tag name, and color for a matched email flag.
type ResolvedTag struct {
	Key   string `json:"key"`
	Tag   string `json:"tag"`
	Color string `json:"color"`
}

// GeneralConfig holds server, storage, and editor settings.
type GeneralConfig struct {
	Host                   string `toml:"host" json:"host"`
	Port                   int    `toml:"port" json:"port"`
	Debug                  bool   `toml:"debug" json:"debug"`
	DataDir                string `toml:"data_dir" json:"data_dir"`
	MaildirPath            string `toml:"maildir_path" json:"maildir_path"`
	DBPath                 string `toml:"db_path" json:"db_path"`
	EditorCmd              string `toml:"editor_cmd" json:"editor_cmd"`
	TerminalCmd            string `toml:"terminal_cmd" json:"terminal_cmd"`
	ReplyUseExternalEditor bool   `toml:"reply_use_external_editor" json:"reply_use_external_editor"`
	KernelGitDir           string `toml:"kernel_git_dir" json:"kernel_git_dir"`
	LogFile                string `toml:"log_file" json:"log_file"`
	LogLevel               string `toml:"log_level" json:"log_level"`
	PIDFile                string `toml:"pid_file" json:"pid_file"`
}

// EffectiveEditorCmd returns EditorCmd, falling back to TerminalCmd or DefaultTerminalCmd.
func (g *GeneralConfig) EffectiveEditorCmd() string {
	if strings.TrimSpace(g.EditorCmd) != "" {
		return g.EditorCmd
	}
	if strings.TrimSpace(g.TerminalCmd) != "" {
		return g.TerminalCmd
	}
	return constants.DefaultTerminalCmd
}

// GetExternalEditorCmd returns the external editor command, preferring editor_cmd if set, otherwise terminal_cmd.
func (g *GeneralConfig) GetExternalEditorCmd() string {
	if strings.TrimSpace(g.EditorCmd) != "" {
		return strings.TrimSpace(g.EditorCmd)
	}
	return strings.TrimSpace(g.TerminalCmd)
}

// IMAPConfig holds settings for selective IMAP sync.
type IMAPConfig struct {
	Host                string   `toml:"host" json:"host"`
	Port                int      `toml:"port" json:"port"`
	UseSSL              bool     `toml:"use_ssl" json:"use_ssl"`
	VerifyCert          bool     `toml:"verify_cert" json:"verify_cert"`
	CACertPath          string   `toml:"ca_cert_path" json:"ca_cert_path"`
	Username            string   `toml:"username" json:"username"`
	PasswordCmd         string   `toml:"password_cmd" json:"password_cmd"`
	Password            string   `toml:"password" json:"password"`
	SyncFolders         []string `toml:"sync_folders" json:"sync_folders"`
	PollIntervalSeconds int      `toml:"poll_interval_seconds" json:"poll_interval_seconds"`
}

// GetPassword returns Password, or executes PasswordCmd if Password is empty.
func (i *IMAPConfig) GetPassword() string {
	if i.Password != "" {
		return i.Password
	}
	if i.PasswordCmd != "" {
		out, err := ExecuteSecretCommand(i.PasswordCmd)
		if err == nil {
			return out
		}
	}
	return ""
}

// SMTPConfig holds settings for strict plain-text SMTP transport.
type SMTPConfig struct {
	Host             string `toml:"host" json:"host"`
	Port             int    `toml:"port" json:"port"`
	UseSSL           bool   `toml:"use_ssl" json:"use_ssl"`
	UseSTARTTLS      bool   `toml:"use_starttls" json:"use_starttls"`
	VerifyCert       bool   `toml:"verify_cert" json:"verify_cert"`
	CACertPath       string `toml:"ca_cert_path" json:"ca_cert_path"`
	Username         string `toml:"username" json:"username"`
	PasswordCmd      string `toml:"password_cmd" json:"password_cmd"`
	Password         string `toml:"password" json:"password"`
	FromName         string `toml:"from_name" json:"from_name"`
	FromEmail        string `toml:"from_email" json:"from_email"`
	SignatureFile    string `toml:"signature_file" json:"signature_file"`
	ContentType      string `toml:"content_type" json:"content_type"`
	TransferEncoding string `toml:"transfer_encoding" json:"transfer_encoding"`
}

// GetPassword returns Password, or executes PasswordCmd if Password is empty.
func (s *SMTPConfig) GetPassword() string {
	if s.Password != "" {
		return s.Password
	}
	if s.PasswordCmd != "" {
		out, err := ExecuteSecretCommand(s.PasswordCmd)
		if err == nil {
			return out
		}
	}
	return ""
}

// GetSignature reads the signature from SignatureFile if available.
func (s *SMTPConfig) GetSignature() string {
	path := ExpandPath(s.SignatureFile)
	if path == "" {
		return ""
	}
	content, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return strings.TrimRight(string(content), "\r\n")
}

// EffectiveSignature returns the signature from SignatureFile if available.
func (s *SMTPConfig) EffectiveSignature() string {
	return s.GetSignature()
}

// LKMLConfig holds b4 and lore settings.
type LKMLConfig struct {
	B4Cmd                     string `toml:"b4_cmd" json:"b4_cmd"`
	LoreURL                   string `toml:"lore_url" json:"lore_url"`
	AutoHydrateMissingParents bool   `toml:"auto_hydrate_missing_parents" json:"auto_hydrate_missing_parents"`
}

// MCPConfig holds Model Context Protocol server settings.
type MCPConfig struct {
	Enabled   bool   `toml:"enabled" json:"enabled"`
	Transport string `toml:"transport" json:"transport"`
	SSEPort   int    `toml:"sse_port" json:"sse_port"`
}

// ReviewConfig holds AI review runner settings.
type ReviewConfig struct {
	Command       string `toml:"command" json:"command"`
	WrapperScript string `toml:"wrapper_script" json:"wrapper_script"`
	PromptFile    string `toml:"prompt_file" json:"prompt_file"`
	Prompt        string `toml:"prompt" json:"prompt"`
}

// EffectivePrompt returns PromptFile content if exists, or Prompt, or DefaultReviewPrompt.
func (r *ReviewConfig) EffectivePrompt() string {
	if r.PromptFile != "" {
		p := ExpandPath(r.PromptFile)
		if content, err := os.ReadFile(p); err == nil && len(content) > 0 {
			return strings.TrimSpace(string(content))
		}
	}
	if strings.TrimSpace(r.Prompt) != "" {
		return r.Prompt
	}
	return constants.DefaultReviewPrompt
}

// PatchworkConfig holds patchwork.kernel.org integration settings.
type PatchworkConfig struct {
	Enabled           bool     `toml:"enabled" json:"enabled"`
	URL               string   `toml:"url" json:"url"`
	APIVersion        string   `toml:"api_version" json:"api_version"`
	Username          string   `toml:"username" json:"username"`
	Email             string   `toml:"email" json:"email"`
	TokenCmd          string   `toml:"token_cmd" json:"token_cmd"`
	Token             string   `toml:"token" json:"token"`
	PreferredArchives []string `toml:"preferred_archives" json:"preferred_archives"`
	DefaultProject    string   `toml:"default_project" json:"default_project"`
	DelegateUsers     []string `toml:"delegate_users" json:"delegate_users"`
	TimeoutSeconds    float64  `toml:"timeout_seconds" json:"timeout_seconds"`
}

// GetToken returns Token, or executes TokenCmd if Token is empty.
func (p *PatchworkConfig) GetToken() string {
	if p.Token != "" {
		return p.Token
	}
	if p.TokenCmd != "" {
		out, err := ExecuteSecretCommand(p.TokenCmd)
		if err == nil {
			return out
		}
	}
	return ""
}

// GetMyUsername returns configured username or user from email.
func (p *PatchworkConfig) GetMyUsername(fallbackEmail string) string {
	if p.Username != "" {
		return p.Username
	}
	e := p.GetMyEmail(fallbackEmail)
	if idx := strings.Index(e, "@"); idx > 0 {
		return e[:idx]
	}
	return e
}

// GetMyEmail returns configured email or fallbackEmail.
func (p *PatchworkConfig) GetMyEmail(fallbackEmail string) string {
	if p.Email != "" {
		return p.Email
	}
	return fallbackEmail
}

// IsMe checks if a delegate username or email corresponds to the current user.
func (p *PatchworkConfig) IsMe(delegate any, fallbackEmail string) bool {
	if delegate == nil {
		return false
	}
	myUser := strings.ToLower(p.GetMyUsername(fallbackEmail))
	myEmail := strings.ToLower(p.GetMyEmail(fallbackEmail))

	var candidates []string
	switch v := delegate.(type) {
	case string:
		candidates = append(candidates, strings.ToLower(v))
	case map[string]any:
		if u, ok := v["username"].(string); ok {
			candidates = append(candidates, strings.ToLower(u))
		}
		if em, ok := v["email"].(string); ok {
			candidates = append(candidates, strings.ToLower(em))
		}
	}

	for _, d := range p.DelegateUsers {
		candidates = append(candidates, strings.ToLower(d))
	}

	for _, c := range candidates {
		if c != "" && (c == myUser || c == myEmail) {
			return true
		}
	}
	return false
}

// GetPreferredArchives returns preferred archive list or default project.
func (p *PatchworkConfig) GetPreferredArchives() []string {
	if len(p.PreferredArchives) > 0 {
		return p.PreferredArchives
	}
	if p.DefaultProject != "" {
		return []string{p.DefaultProject}
	}
	return nil
}

// GetTodoStates returns default patchwork states synced to TODO mailbox.
func (p *PatchworkConfig) GetTodoStates() []string {
	return []string{"new", "under-review"}
}

// GetDelegateUsers returns cleaned list of configured delegate usernames.
func (p *PatchworkConfig) GetDelegateUsers() []string {
	var res []string
	seen := make(map[string]bool)
	for _, u := range p.DelegateUsers {
		for _, part := range strings.Split(u, ",") {
			trimmed := strings.TrimSpace(part)
			if trimmed != "" && !seen[trimmed] {
				seen[trimmed] = true
				res = append(res, trimmed)
			}
		}
	}
	if res == nil {
		return []string{}
	}
	return res
}

// Config represents the complete UndineD configuration tree.
type Config struct {
	General   GeneralConfig            `toml:"general" json:"general"`
	IMAP      IMAPConfig               `toml:"imap" json:"imap"`
	SMTP      SMTPConfig               `toml:"smtp" json:"smtp"`
	LKML      LKMLConfig               `toml:"lkml" json:"lkml"`
	MCP       MCPConfig                `toml:"mcp" json:"mcp"`
	Review    ReviewConfig             `toml:"review" json:"review"`
	Patchwork PatchworkConfig          `toml:"patchwork" json:"patchwork"`
	Tags      map[string]TagDefinition `toml:"tags" json:"tags"`
}

// DefaultTags returns the built-in color tags mapping.
func DefaultTags() map[string]TagDefinition {
	return map[string]TagDefinition{
		"$cl_1":   {Tag: "Red", Color: "#CC0000"},
		"$cl_2":   {Tag: "Blue", Color: "#3333FF"},
		"$cl_3":   {Tag: "Green", Color: "#006600"},
		"$cl_4":   {Tag: "Gray", Color: "#999999"},
		"$cl_5":   {Tag: "Purple", Color: "#6600CC"},
		"$cl_6":   {Tag: "Light green", Color: "#33CC00"},
		"$cl_7":   {Tag: "Orange", Color: "#FF9900"},
		"$cl_8":   {Tag: "Pink", Color: "#CC33CC"},
		"$cl_9":   {Tag: "Light blue", Color: "#33CCFF"},
		"$cl_10":  {Tag: "Yellow", Color: "#FFFF00"},
		"$label1": {Tag: "Important", Color: "#FF2600"},
		"$label2": {Tag: "Work", Color: "#FF9900"},
		"$label3": {Tag: "Personal", Color: "#009900"},
		"$label4": {Tag: "To Do", Color: "#3333FF"},
		"$label5": {Tag: "Later", Color: "#993399"},
	}
}

// ResolveTag checks tokens in flags against configured tag definitions (case-insensitive)
// and returns the first matching tag info, or nil if no match.
func (c *Config) ResolveTag(flags string) *ResolvedTag {
	if strings.TrimSpace(flags) == "" || c == nil || len(c.Tags) == 0 {
		return nil
	}
	cleanFlags := strings.ReplaceAll(flags, ",", " ")
	tokens := strings.Fields(cleanFlags)
	if len(tokens) == 0 {
		return nil
	}

	lowerMap := make(map[string]struct {
		origKey string
		tagDef  TagDefinition
	}, len(c.Tags))
	for k, v := range c.Tags {
		lowerMap[strings.ToLower(k)] = struct {
			origKey string
			tagDef  TagDefinition
		}{origKey: k, tagDef: v}
	}

	for _, tok := range tokens {
		tokLower := strings.ToLower(tok)
		if entry, ok := lowerMap[tokLower]; ok {
			return &ResolvedTag{
				Key:   entry.origKey,
				Tag:   entry.tagDef.Tag,
				Color: entry.tagDef.Color,
			}
		}
	}
	return nil
}

// NewDefaultConfig returns a Config initialized with standard defaults.
func NewDefaultConfig() *Config {
	return &Config{
		General: GeneralConfig{
			Host:                   constants.DefaultHost,
			Port:                   constants.DefaultPort,
			Debug:                  false,
			DataDir:                ExpandPath(constants.DefaultDataDir),
			MaildirPath:            ExpandPath(constants.DefaultMaildirPath),
			DBPath:                 ExpandPath(constants.DefaultDBPath),
			EditorCmd:              constants.DefaultTerminalCmd,
			TerminalCmd:            constants.DefaultTerminalCmd,
			ReplyUseExternalEditor: false,
			KernelGitDir:           ExpandPath(constants.DefaultKernelGitDir),
			LogFile:                ExpandPath(constants.DefaultLogPath),
			LogLevel:               constants.DefaultLogLevel,
			PIDFile:                ExpandPath(constants.DefaultPIDPath),
		},
		IMAP: IMAPConfig{
			Host:                constants.DefaultIMAPHost,
			Port:                constants.DefaultIMAPPort,
			UseSSL:              true,
			VerifyCert:          true,
			Username:            "dev@example.com",
			SyncFolders:         []string{},
			PollIntervalSeconds: constants.DefaultIMAPPollIntervalSeconds,
		},
		SMTP: SMTPConfig{
			Host:             constants.DefaultSMTPHost,
			Port:             constants.DefaultSMTPPort,
			UseSSL:           false,
			UseSTARTTLS:      true,
			VerifyCert:       true,
			Username:         "dev@example.com",
			FromName:         constants.DefaultFromName,
			FromEmail:        constants.DefaultFromEmail,
			SignatureFile:    ExpandPath(constants.DefaultSignaturePath),
			ContentType:      constants.DefaultContentType,
			TransferEncoding: constants.DefaultTransferEncoding,
		},
		LKML: LKMLConfig{
			B4Cmd:                     constants.DefaultB4Cmd,
			LoreURL:                   constants.DefaultLoreURL,
			AutoHydrateMissingParents: false,
		},
		MCP: MCPConfig{
			Enabled:   true,
			Transport: constants.DefaultMCPTransport,
			SSEPort:   constants.DefaultMCPPort,
		},
		Review: ReviewConfig{
			Command: constants.DefaultReviewCmd,
			Prompt:  constants.DefaultReviewPrompt,
		},
		Patchwork: PatchworkConfig{
			Enabled:           true,
			URL:               constants.DefaultPatchworkURL,
			APIVersion:        constants.DefaultPatchworkAPIVersion,
			Username:          "dev",
			Email:             "dev@example.com",
			PreferredArchives: []string{"linux-trace"},
			TimeoutSeconds:    constants.DefaultPatchworkTimeoutSeconds,
		},
		Tags: DefaultTags(),
	}
}

// ExpandPath expands leading ~ to user home directory.
func ExpandPath(path string) string {
	if path == "" {
		return ""
	}
	if strings.HasPrefix(path, "~/") || path == "~" {
		home, err := os.UserHomeDir()
		if err == nil {
			if path == "~" {
				return home
			}
			return filepath.Join(home, path[2:])
		}
	}
	return os.ExpandEnv(path)
}

// FindConfigFile locates the configuration file in standard locations.
func FindConfigFile(explicitPath string) string {
	if explicitPath != "" {
		expanded := ExpandPath(explicitPath)
		if _, err := os.Stat(expanded); err == nil {
			return expanded
		}
		return expanded
	}

	for _, env := range []string{constants.EnvUndinedConfig, constants.EnvLegacyUndineConfig} {
		if val := os.Getenv(env); val != "" {
			expanded := ExpandPath(val)
			if _, err := os.Stat(expanded); err == nil {
				return expanded
			}
		}
	}

	candidates := []string{
		ExpandPath(constants.DefaultConfigPath),
		ExpandPath("~/.config/undine/config.toml"),
	}

	for _, p := range candidates {
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}

	return ExpandPath(constants.DefaultConfigPath)
}

// Load loads and parses the TOML config from file. If file does not exist, returns default config.
func Load(explicitPath ...string) (*Config, error) {
	cfg := NewDefaultConfig()

	var path string
	if len(explicitPath) > 0 && explicitPath[0] != "" {
		path = explicitPath[0]
	}
	configPath := FindConfigFile(path)

	data, err := os.ReadFile(configPath)
	if err != nil {
		if os.IsNotExist(err) {
			return cfg, nil
		}
		return nil, fmt.Errorf("failed to read config file %s: %w", configPath, err)
	}

	if err := toml.Unmarshal(data, cfg); err != nil {
		return nil, fmt.Errorf("failed to parse config TOML %s: %w", configPath, err)
	}

	// Expand paths
	cfg.General.DataDir = ExpandPath(cfg.General.DataDir)
	cfg.General.MaildirPath = ExpandPath(cfg.General.MaildirPath)
	cfg.General.DBPath = ExpandPath(cfg.General.DBPath)
	cfg.General.KernelGitDir = ExpandPath(cfg.General.KernelGitDir)
	cfg.General.LogFile = ExpandPath(cfg.General.LogFile)
	cfg.General.PIDFile = ExpandPath(cfg.General.PIDFile)
	if cfg.General.PIDFile == "" {
		cfg.General.PIDFile = filepath.Join(cfg.General.DataDir, constants.PIDFileName)
	}
	cfg.SMTP.SignatureFile = ExpandPath(cfg.SMTP.SignatureFile)
	if cfg.Review.PromptFile != "" {
		cfg.Review.PromptFile = ExpandPath(cfg.Review.PromptFile)
	}
	if cfg.Review.WrapperScript != "" {
		cfg.Review.WrapperScript = ExpandPath(cfg.Review.WrapperScript)
	}

	// Merge default tags if not overridden
	if cfg.Tags == nil {
		cfg.Tags = DefaultTags()
	} else {
		defaults := DefaultTags()
		for k, v := range defaults {
			if _, exists := cfg.Tags[k]; !exists {
				cfg.Tags[k] = v
			}
		}
	}

	return cfg, nil
}

// ExecuteSecretCommand executes a command to retrieve a password or token (e.g. secret-tool, pass).
func ExecuteSecretCommand(cmd string) (string, error) {
	cmd = strings.TrimSpace(cmd)
	if cmd == "" {
		return "", nil
	}
	out, err := exec.Command("sh", "-c", cmd).Output()
	if err != nil {
		return "", fmt.Errorf("secret command %q failed: %w", cmd, err)
	}
	return strings.TrimRight(string(out), "\r\n"), nil
}
