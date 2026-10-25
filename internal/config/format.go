package config

import (
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"

	"github.com/pelletier/go-toml/v2"
)

type fieldSpec struct {
	key      string
	isPath   bool
	getValue func(c *Config) any
}

type sectionSpec struct {
	name   string
	fields []fieldSpec
}

func getSectionSpecs() []sectionSpec {
	return []sectionSpec{
		{
			name: "general",
			fields: []fieldSpec{
				{key: "host", getValue: func(c *Config) any { return c.General.Host }},
				{key: "port", getValue: func(c *Config) any { return c.General.Port }},
				{key: "debug", getValue: func(c *Config) any { return c.General.Debug }},
				{key: "data_dir", isPath: true, getValue: func(c *Config) any { return c.General.DataDir }},
				{key: "maildir_path", isPath: true, getValue: func(c *Config) any { return c.General.MaildirPath }},
				{key: "db_path", isPath: true, getValue: func(c *Config) any { return c.General.DBPath }},
				{key: "editor_cmd", getValue: func(c *Config) any { return c.General.EditorCmd }},
				{key: "terminal_cmd", getValue: func(c *Config) any { return c.General.TerminalCmd }},
				{key: "reply_use_external_editor", getValue: func(c *Config) any { return c.General.ReplyUseExternalEditor }},
				{key: "kernel_git_dir", isPath: true, getValue: func(c *Config) any { return c.General.KernelGitDir }},
				{key: "composer_window_mode", getValue: func(c *Config) any { return c.General.ComposerWindowMode }},
				{key: "log_file", isPath: true, getValue: func(c *Config) any { return c.General.LogFile }},
				{key: "log_level", getValue: func(c *Config) any { return c.General.LogLevel }},
				{key: "pid_file", isPath: true, getValue: func(c *Config) any { return c.General.PIDFile }},
			},
		},
		{
			name: "imap",
			fields: []fieldSpec{
				{key: "host", getValue: func(c *Config) any { return c.IMAP.Host }},
				{key: "port", getValue: func(c *Config) any { return c.IMAP.Port }},
				{key: "use_ssl", getValue: func(c *Config) any { return c.IMAP.UseSSL }},
				{key: "verify_cert", getValue: func(c *Config) any { return c.IMAP.VerifyCert }},
				{key: "ca_cert_path", isPath: true, getValue: func(c *Config) any { return c.IMAP.CACertPath }},
				{key: "username", getValue: func(c *Config) any { return c.IMAP.Username }},
				{key: "password", getValue: func(c *Config) any { return c.IMAP.Password }},
				{key: "password_cmd", getValue: func(c *Config) any { return c.IMAP.PasswordCmd }},
				{key: "sync_folders", getValue: func(c *Config) any { return c.IMAP.SyncFolders }},
				{key: "poll_interval_seconds", getValue: func(c *Config) any { return c.IMAP.PollIntervalSeconds }},
			},
		},
		{
			name: "smtp",
			fields: []fieldSpec{
				{key: "host", getValue: func(c *Config) any { return c.SMTP.Host }},
				{key: "port", getValue: func(c *Config) any { return c.SMTP.Port }},
				{key: "use_ssl", getValue: func(c *Config) any { return c.SMTP.UseSSL }},
				{key: "use_starttls", getValue: func(c *Config) any { return c.SMTP.UseSTARTTLS }},
				{key: "use_smtputf8", getValue: func(c *Config) any { return c.SMTP.UseSMTPUTF8 }},
				{key: "verify_cert", getValue: func(c *Config) any { return c.SMTP.VerifyCert }},
				{key: "ca_cert_path", isPath: true, getValue: func(c *Config) any { return c.SMTP.CACertPath }},
				{key: "username", getValue: func(c *Config) any { return c.SMTP.Username }},
				{key: "password", getValue: func(c *Config) any { return c.SMTP.Password }},
				{key: "password_cmd", getValue: func(c *Config) any { return c.SMTP.PasswordCmd }},
				{key: "from_name", getValue: func(c *Config) any { return c.SMTP.FromName }},
				{key: "from_email", getValue: func(c *Config) any { return c.SMTP.FromEmail }},
				{key: "signature_file", isPath: true, getValue: func(c *Config) any { return c.SMTP.SignatureFile }},
				{key: "content_type", getValue: func(c *Config) any { return c.SMTP.ContentType }},
				{key: "transfer_encoding", getValue: func(c *Config) any { return c.SMTP.TransferEncoding }},
			},
		},
		{
			name: "lkml",
			fields: []fieldSpec{
				{key: "b4_cmd", getValue: func(c *Config) any { return c.LKML.B4Cmd }},
				{key: "lore_url", getValue: func(c *Config) any { return c.LKML.LoreURL }},
				{key: "auto_hydrate_missing_parents", getValue: func(c *Config) any { return c.LKML.AutoHydrateMissingParents }},
			},
		},
		{
			name: "mcp",
			fields: []fieldSpec{
				{key: "enabled", getValue: func(c *Config) any { return c.MCP.Enabled }},
				{key: "transport", getValue: func(c *Config) any { return c.MCP.Transport }},
				{key: "sse_port", getValue: func(c *Config) any { return c.MCP.SSEPort }},
			},
		},
		{
			name: "review",
			fields: []fieldSpec{
				{key: "command", getValue: func(c *Config) any { return c.Review.Command }},
				{key: "wrapper_script", isPath: true, getValue: func(c *Config) any { return c.Review.WrapperScript }},
				{key: "prompt_file", isPath: true, getValue: func(c *Config) any { return c.Review.PromptFile }},
				{key: "prompt", getValue: func(c *Config) any { return c.Review.Prompt }},
			},
		},
		{
			name: "patchwork",
			fields: []fieldSpec{
				{key: "enabled", getValue: func(c *Config) any { return c.Patchwork.Enabled }},
				{key: "url", getValue: func(c *Config) any { return c.Patchwork.URL }},
				{key: "api_version", getValue: func(c *Config) any { return c.Patchwork.APIVersion }},
				{key: "username", getValue: func(c *Config) any { return c.Patchwork.Username }},
				{key: "email", getValue: func(c *Config) any { return c.Patchwork.Email }},
				{key: "token", getValue: func(c *Config) any { return c.Patchwork.Token }},
				{key: "token_cmd", getValue: func(c *Config) any { return c.Patchwork.TokenCmd }},
				{key: "preferred_archives", getValue: func(c *Config) any { return c.Patchwork.PreferredArchives }},
				{key: "default_project", getValue: func(c *Config) any { return c.Patchwork.DefaultProject }},
				{key: "delegate-users", getValue: func(c *Config) any { return c.Patchwork.GetDelegateUsers() }},
				{key: "timeout_seconds", getValue: func(c *Config) any { return c.Patchwork.TimeoutSeconds }},
			},
		},
	}
}

// FormatTOMLSettingValue formats a value into a valid TOML representation,
// applying DisplayPath if isPath is true.
func FormatTOMLSettingValue(val any, isPath bool) string {
	if val == nil {
		return `""`
	}
	switch v := val.(type) {
	case string:
		if isPath && v != "" {
			v = DisplayPath(v)
		}
		return strconv.Quote(v)
	case int:
		return strconv.Itoa(v)
	case int64:
		return strconv.FormatInt(v, 10)
	case float64:
		return fmt.Sprintf("%.1f", v)
	case bool:
		return strconv.FormatBool(v)
	case []string:
		if len(v) == 0 {
			return "[]"
		}
		var quoted []string
		for _, s := range v {
			if isPath && s != "" {
				s = DisplayPath(s)
			}
			quoted = append(quoted, strconv.Quote(s))
		}
		return "[" + strings.Join(quoted, ", ") + "]"
	default:
		return fmt.Sprintf("%v", v)
	}
}

func getSectionMap(rawMap map[string]any, sectionName string) map[string]any {
	if rawMap == nil {
		return nil
	}
	for k, v := range rawMap {
		if strings.EqualFold(k, sectionName) {
			if m, ok := v.(map[string]any); ok {
				return m
			}
		}
	}
	return nil
}

func findConfiguredKey(secMap map[string]any, schemaKey string) (bool, string) {
	if secMap == nil {
		return false, ""
	}
	normSchema := NormalizeKey(schemaKey)
	for k := range secMap {
		if NormalizeKey(k) == normSchema {
			return true, k
		}
	}
	return false, ""
}

// FormatActiveConfig organizes and formats configuration settings by section.
// Active/configured settings are displayed as 'key = value', while unconfigured
// settings are displayed as '# key = <default_value>'. Obsolete settings in raw
// files are ignored and omitted from the output.
func FormatActiveConfig(configPath string) (string, error) {
	targetPath := FindConfigFile(configPath)
	displayPath := DisplayPath(targetPath)

	var rawData []byte
	fileExists := false
	if targetPath != "" {
		if data, err := os.ReadFile(targetPath); err == nil {
			rawData = data
			fileExists = true
		} else if !os.IsNotExist(err) {
			return "", fmt.Errorf("failed to read config file %s: %w", displayPath, err)
		}
	}

	var rawMap map[string]any
	if fileExists && len(rawData) > 0 {
		if err := toml.Unmarshal(rawData, &rawMap); err != nil {
			return "", fmt.Errorf("failed to parse config TOML %s: %w", displayPath, err)
		}
	}

	activeCfg, err := Load(configPath)
	if err != nil {
		return "", err
	}
	defCfg := NewDefaultConfig()

	var sb strings.Builder
	if fileExists {
		sb.WriteString(fmt.Sprintf("# Configuration: %s\n\n", displayPath))
	} else {
		sb.WriteString(fmt.Sprintf("# Configuration: %s (not found, default settings shown)\n\n", displayPath))
	}

	sections := getSectionSpecs()
	for _, sec := range sections {
		sb.WriteString(fmt.Sprintf("[%s]\n", sec.name))
		secMap := getSectionMap(rawMap, sec.name)

		for _, f := range sec.fields {
			isConfigured, rawKey := findConfiguredKey(secMap, f.key)
			displayKey := f.key
			if isConfigured && rawKey != "" {
				displayKey = rawKey
			}

			if isConfigured {
				activeVal := f.getValue(activeCfg)
				valStr := FormatTOMLSettingValue(activeVal, f.isPath)
				sb.WriteString(fmt.Sprintf("%s = %s\n", displayKey, valStr))
			} else {
				defaultVal := f.getValue(defCfg)
				valStr := FormatTOMLSettingValue(defaultVal, f.isPath)
				sb.WriteString(fmt.Sprintf("# %s = %s\n", displayKey, valStr))
			}
		}
		sb.WriteString("\n")
	}

	// Tags section
	sb.WriteString("[tags]\n")
	defaultTagKeys := []string{
		"$cl_1", "$cl_2", "$cl_3", "$cl_4", "$cl_5",
		"$cl_6", "$cl_7", "$cl_8", "$cl_9", "$cl_10",
		"$label1", "$label2", "$label3", "$label4", "$label5",
	}
	defTags := DefaultTags()
	rawTags := getSectionMap(rawMap, "tags")
	emitted := make(map[string]bool)

	if rawTags != nil {
		var customKeys []string
		for k := range rawTags {
			customKeys = append(customKeys, k)
		}
		sort.Strings(customKeys)
		for _, k := range customKeys {
			if tDef, ok := activeCfg.Tags[k]; ok {
				sb.WriteString(fmt.Sprintf("%s = { tag = %q, color = %q }\n", strconv.Quote(k), tDef.Tag, tDef.Color))
				emitted[strings.ToLower(k)] = true
			}
		}
	}

	for _, k := range defaultTagKeys {
		if emitted[strings.ToLower(k)] {
			continue
		}
		if tDef, ok := defTags[k]; ok {
			sb.WriteString(fmt.Sprintf("# %s = { tag = %q, color = %q }\n", strconv.Quote(k), tDef.Tag, tDef.Color))
		}
	}
	sb.WriteString("\n")

	return sb.String(), nil
}
