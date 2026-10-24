package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/pelletier/go-toml/v2"
)

// DisplayPath returns path with user's home directory replaced by ~ for portability and privacy.
func DisplayPath(path string) string {
	if path == "" {
		return ""
	}
	home, err := os.UserHomeDir()
	if err == nil && home != "" {
		if path == home {
			return "~"
		}
		if strings.HasPrefix(path, home+string(filepath.Separator)) {
			return "~" + path[len(home):]
		}
	}
	return path
}

// NormalizeKey converts key to lowercase with hyphens replaced by underscores.
func NormalizeKey(key string) string {
	return strings.ToLower(strings.ReplaceAll(strings.TrimSpace(key), "-", "_"))
}

// FormatTOMLValue formats a raw user-provided string into a valid TOML value representation.
func FormatTOMLValue(key, val string) string {
	val = strings.TrimSpace(val)
	if val == "" {
		return `""`
	}

	// Boolean
	lower := strings.ToLower(val)
	if lower == "true" || lower == "false" {
		return lower
	}

	// Integer
	if _, err := strconv.ParseInt(val, 10, 64); err == nil {
		return val
	}

	// Float
	if _, err := strconv.ParseFloat(val, 64); err == nil && strings.Contains(val, ".") {
		return val
	}

	// Explicit bracketed array e.g. ["dev", "reviewer"] or [dev, reviewer]
	if strings.HasPrefix(val, "[") && strings.HasSuffix(val, "]") {
		// Test if already valid TOML array
		if err := toml.Unmarshal([]byte("k = "+val), &map[string]any{}); err == nil {
			return val
		}
		// Try parsing unquoted items inside brackets: [dev, reviewer]
		inner := strings.TrimSpace(val[1 : len(val)-1])
		if inner == "" {
			return "[]"
		}
		parts := strings.Split(inner, ",")
		var quoted []string
		for _, p := range parts {
			t := strings.TrimSpace(p)
			t = strings.Trim(t, "\"'`")
			quoted = append(quoted, strconv.Quote(t))
		}
		return "[" + strings.Join(quoted, ", ") + "]"
	}

	// Array field where user passed comma-separated items (e.g. delegate-users = dev, reviewer)
	normKey := NormalizeKey(key)
	if strings.Contains(normKey, "delegate") || strings.Contains(normKey, "archives") || strings.Contains(normKey, "sync_folders") {
		if strings.Contains(val, ",") {
			parts := strings.Split(val, ",")
			var quoted []string
			for _, p := range parts {
				t := strings.TrimSpace(p)
				t = strings.Trim(t, "\"'`")
				if t != "" {
					quoted = append(quoted, strconv.Quote(t))
				}
			}
			return "[" + strings.Join(quoted, ", ") + "]"
		}
	}

	// If already quoted string
	if (strings.HasPrefix(val, `"`) && strings.HasSuffix(val, `"`)) ||
		(strings.HasPrefix(val, `'`) && strings.HasSuffix(val, `'`)) {
		return val
	}

	return strconv.Quote(val)
}

// GetConfigValue resolves a dot-separated key path (e.g. "patchwork.delegate-users") from cfg.
func GetConfigValue(cfg *Config, keyPath string) (string, error) {
	if cfg == nil {
		return "", fmt.Errorf("configuration is nil")
	}

	parts := strings.SplitN(strings.TrimSpace(keyPath), ".", 2)
	section := strings.ToLower(parts[0])
	key := ""
	if len(parts) > 1 {
		key = NormalizeKey(parts[1])
	}

	switch section {
	case "general":
		if key == "" {
			return formatSection(map[string]any{
				"host":                      cfg.General.Host,
				"port":                      cfg.General.Port,
				"debug":                     cfg.General.Debug,
				"data_dir":                  cfg.General.DataDir,
				"maildir_path":              cfg.General.MaildirPath,
				"db_path":                   cfg.General.DBPath,
				"editor_cmd":                cfg.General.EditorCmd,
				"terminal_cmd":              cfg.General.TerminalCmd,
				"reply_use_external_editor": cfg.General.ReplyUseExternalEditor,
				"kernel_git_dir":            cfg.General.KernelGitDir,
				"pid_file":                  cfg.General.PIDFile,
				"log_file":                  cfg.General.LogFile,
				"log_level":                 cfg.General.LogLevel,
			}), nil
		}
		switch key {
		case "host":
			return cfg.General.Host, nil
		case "port":
			return strconv.Itoa(cfg.General.Port), nil
		case "debug":
			return strconv.FormatBool(cfg.General.Debug), nil
		case "data_dir":
			return cfg.General.DataDir, nil
		case "maildir_path":
			return cfg.General.MaildirPath, nil
		case "db_path":
			return cfg.General.DBPath, nil
		case "editor_cmd":
			return cfg.General.EditorCmd, nil
		case "terminal_cmd":
			return cfg.General.TerminalCmd, nil
		case "reply_use_external_editor":
			return strconv.FormatBool(cfg.General.ReplyUseExternalEditor), nil
		case "kernel_git_dir":
			return cfg.General.KernelGitDir, nil
		case "pid_file":
			return cfg.General.PIDFile, nil
		case "log_file":
			return cfg.General.LogFile, nil
		case "log_level":
			return cfg.General.LogLevel, nil
		}

	case "imap":
		if key == "" {
			return formatSection(map[string]any{
				"host":                  cfg.IMAP.Host,
				"port":                  cfg.IMAP.Port,
				"use_ssl":               cfg.IMAP.UseSSL,
				"verify_cert":           cfg.IMAP.VerifyCert,
				"username":              cfg.IMAP.Username,
				"password_cmd":          cfg.IMAP.PasswordCmd,
				"sync_folders":          cfg.IMAP.SyncFolders,
				"poll_interval_seconds": cfg.IMAP.PollIntervalSeconds,
			}), nil
		}
		switch key {
		case "host":
			return cfg.IMAP.Host, nil
		case "port":
			return strconv.Itoa(cfg.IMAP.Port), nil
		case "use_ssl":
			return strconv.FormatBool(cfg.IMAP.UseSSL), nil
		case "verify_cert":
			return strconv.FormatBool(cfg.IMAP.VerifyCert), nil
		case "username":
			return cfg.IMAP.Username, nil
		case "password_cmd":
			return cfg.IMAP.PasswordCmd, nil
		case "password":
			return cfg.IMAP.Password, nil
		case "sync_folders":
			return formatStringSlice(cfg.IMAP.SyncFolders), nil
		case "poll_interval_seconds":
			return strconv.Itoa(cfg.IMAP.PollIntervalSeconds), nil
		}

	case "smtp":
		if key == "" {
			return formatSection(map[string]any{
				"host":              cfg.SMTP.Host,
				"port":              cfg.SMTP.Port,
				"use_ssl":           cfg.SMTP.UseSSL,
				"use_starttls":      cfg.SMTP.UseSTARTTLS,
				"use_smtputf8":      cfg.SMTP.UseSMTPUTF8,
				"verify_cert":       cfg.SMTP.VerifyCert,
				"username":          cfg.SMTP.Username,
				"password_cmd":      cfg.SMTP.PasswordCmd,
				"from_name":         cfg.SMTP.FromName,
				"from_email":        cfg.SMTP.FromEmail,
				"signature_file":    cfg.SMTP.SignatureFile,
				"content_type":      cfg.SMTP.ContentType,
				"transfer_encoding": cfg.SMTP.TransferEncoding,
			}), nil
		}
		switch key {
		case "host":
			return cfg.SMTP.Host, nil
		case "port":
			return strconv.Itoa(cfg.SMTP.Port), nil
		case "use_ssl":
			return strconv.FormatBool(cfg.SMTP.UseSSL), nil
		case "use_starttls":
			return strconv.FormatBool(cfg.SMTP.UseSTARTTLS), nil
		case "use_smtputf8":
			return strconv.FormatBool(cfg.SMTP.UseSMTPUTF8), nil
		case "verify_cert":
			return strconv.FormatBool(cfg.SMTP.VerifyCert), nil
		case "username":
			return cfg.SMTP.Username, nil
		case "password_cmd":
			return cfg.SMTP.PasswordCmd, nil
		case "password":
			return cfg.SMTP.Password, nil
		case "from_name":
			return cfg.SMTP.FromName, nil
		case "from_email":
			return cfg.SMTP.FromEmail, nil
		case "signature_file":
			return cfg.SMTP.SignatureFile, nil
		case "content_type":
			return cfg.SMTP.ContentType, nil
		case "transfer_encoding":
			return cfg.SMTP.TransferEncoding, nil
		}

	case "lkml":
		if key == "" {
			return formatSection(map[string]any{
				"b4_cmd":                       cfg.LKML.B4Cmd,
				"lore_url":                     cfg.LKML.LoreURL,
				"auto_hydrate_missing_parents": cfg.LKML.AutoHydrateMissingParents,
			}), nil
		}
		switch key {
		case "b4_cmd":
			return cfg.LKML.B4Cmd, nil
		case "lore_url":
			return cfg.LKML.LoreURL, nil
		case "auto_hydrate_missing_parents":
			return strconv.FormatBool(cfg.LKML.AutoHydrateMissingParents), nil
		}

	case "mcp":
		if key == "" {
			return formatSection(map[string]any{
				"enabled":   cfg.MCP.Enabled,
				"transport": cfg.MCP.Transport,
				"sse_port":  cfg.MCP.SSEPort,
			}), nil
		}
		switch key {
		case "enabled":
			return strconv.FormatBool(cfg.MCP.Enabled), nil
		case "transport":
			return cfg.MCP.Transport, nil
		case "sse_port":
			return strconv.Itoa(cfg.MCP.SSEPort), nil
		}

	case "review":
		if key == "" {
			return formatSection(map[string]any{
				"command":        cfg.Review.Command,
				"wrapper_script": cfg.Review.WrapperScript,
				"prompt_file":    cfg.Review.PromptFile,
			}), nil
		}
		switch key {
		case "command":
			return cfg.Review.Command, nil
		case "wrapper_script":
			return cfg.Review.WrapperScript, nil
		case "prompt_file":
			return cfg.Review.PromptFile, nil
		case "prompt":
			return cfg.Review.Prompt, nil
		}

	case "patchwork":
		if key == "" {
			return formatSection(map[string]any{
				"enabled":            cfg.Patchwork.Enabled,
				"url":                cfg.Patchwork.URL,
				"api_version":        cfg.Patchwork.APIVersion,
				"username":           cfg.Patchwork.Username,
				"email":              cfg.Patchwork.Email,
				"token_cmd":          cfg.Patchwork.TokenCmd,
				"preferred_archives": cfg.Patchwork.PreferredArchives,
				"default_project":    cfg.Patchwork.DefaultProject,
				"delegate_users":     cfg.Patchwork.GetDelegateUsers(),
				"timeout_seconds":    cfg.Patchwork.TimeoutSeconds,
			}), nil
		}
		switch key {
		case "enabled":
			return strconv.FormatBool(cfg.Patchwork.Enabled), nil
		case "url":
			return cfg.Patchwork.URL, nil
		case "api_version":
			return cfg.Patchwork.APIVersion, nil
		case "username":
			return cfg.Patchwork.Username, nil
		case "email":
			return cfg.Patchwork.Email, nil
		case "token_cmd":
			return cfg.Patchwork.TokenCmd, nil
		case "token":
			return cfg.Patchwork.Token, nil
		case "preferred_archives":
			return formatStringSlice(cfg.Patchwork.PreferredArchives), nil
		case "default_project":
			return cfg.Patchwork.DefaultProject, nil
		case "delegate_users", "delegate-users":
			return formatStringSlice(cfg.Patchwork.GetDelegateUsers()), nil
		case "timeout_seconds":
			return fmt.Sprintf("%.1f", cfg.Patchwork.TimeoutSeconds), nil
		}

	case "tags":
		if key == "" {
			var b strings.Builder
			for k, v := range cfg.Tags {
				b.WriteString(fmt.Sprintf("%s: %s (%s)\n", k, v.Tag, v.Color))
			}
			return strings.TrimRight(b.String(), "\n"), nil
		}
		if tag, ok := cfg.Tags[parts[1]]; ok {
			return fmt.Sprintf("%s (%s)", tag.Tag, tag.Color), nil
		}
	}

	return "", fmt.Errorf("unknown configuration setting %q", keyPath)
}

func formatStringSlice(slice []string) string {
	if len(slice) == 0 {
		return "[]"
	}
	var quoted []string
	for _, s := range slice {
		quoted = append(quoted, strconv.Quote(s))
	}
	return "[" + strings.Join(quoted, ", ") + "]"
}

func formatSection(fields map[string]any) string {
	b, err := json.MarshalIndent(fields, "", "  ")
	if err == nil {
		return string(b)
	}
	return fmt.Sprintf("%+v", fields)
}

// SetTOMLValue modifies or sets a key under the specified section in a TOML file.
// It preserves all existing comments, whitespace, and formatting, validates syntax
// with toml.Unmarshal, and atomically writes the file with mode 0600.
func SetTOMLValue(filePath string, keyPath string, rawValue string) error {
	parts := strings.SplitN(strings.TrimSpace(keyPath), ".", 2)
	if len(parts) != 2 || strings.TrimSpace(parts[0]) == "" || strings.TrimSpace(parts[1]) == "" {
		return fmt.Errorf("invalid setting key format %q: expected <section>.<key> (e.g. patchwork.delegate-users)", keyPath)
	}

	targetSection := strings.ToLower(strings.TrimSpace(parts[0]))
	targetKey := strings.TrimSpace(parts[1])
	normTargetKey := NormalizeKey(targetKey)

	formattedVal := FormatTOMLValue(targetKey, rawValue)

	// Ensure file exists or create it
	var existingContent string
	data, err := os.ReadFile(filePath)
	if err != nil {
		if os.IsNotExist(err) {
			dir := filepath.Dir(filePath)
			if err := os.MkdirAll(dir, 0700); err != nil {
				return fmt.Errorf("failed to create config directory %s: %w", dir, err)
			}
			existingContent = fmt.Sprintf("[%s]\n%s = %s\n", targetSection, targetKey, formattedVal)
		} else {
			return fmt.Errorf("failed to read config file %s: %w", filePath, err)
		}
	} else {
		existingContent = string(data)
	}

	var newLines []string
	if data != nil {
		lines := strings.Split(existingContent, "\n")
		currentSection := ""
		sectionFound := false
		keyFound := false
		sectionStartIdx := -1
		nextSectionIdx := -1

		for i, line := range lines {
			trimmed := strings.TrimSpace(line)
			// Check for section header: [section]
			if strings.HasPrefix(trimmed, "[") && strings.HasSuffix(trimmed, "]") && !strings.HasPrefix(trimmed, "[[") {
				secName := strings.ToLower(strings.TrimSpace(trimmed[1 : len(trimmed)-1]))
				if secName == targetSection {
					currentSection = secName
					sectionFound = true
					sectionStartIdx = i
				} else if currentSection == targetSection {
					// We were in targetSection and encountered next section
					nextSectionIdx = i
					currentSection = secName
				} else {
					currentSection = secName
				}
			} else if currentSection == targetSection && !keyFound {
				// Check for key assignment: key = ... or key=...
				if idx := strings.Index(trimmed, "="); idx > 0 {
					k := strings.TrimSpace(trimmed[:idx])
					if NormalizeKey(k) == normTargetKey {
						// Preserve leading indentation if any
						indent := line[:len(line)-len(strings.TrimLeft(line, " \t"))]
						lines[i] = fmt.Sprintf("%s%s = %s", indent, targetKey, formattedVal)
						keyFound = true
					}
				}
			}
		}

		if !sectionFound {
			// Append new section and key to end of file
			if len(lines) > 0 && strings.TrimSpace(lines[len(lines)-1]) != "" {
				lines = append(lines, "")
			}
			lines = append(lines, fmt.Sprintf("[%s]", targetSection), fmt.Sprintf("%s = %s", targetKey, formattedVal))
		} else if !keyFound {
			// Section exists, but key was not found. Insert key into section.
			newKeyLine := fmt.Sprintf("%s = %s", targetKey, formattedVal)
			if nextSectionIdx >= 0 {
				// Insert before next section
				insertIdx := nextSectionIdx
				// If line before next section is empty, insert before the empty line
				if insertIdx > 0 && strings.TrimSpace(lines[insertIdx-1]) == "" {
					insertIdx--
				}
				lines = append(lines[:insertIdx], append([]string{newKeyLine}, lines[insertIdx:]...)...)
			} else {
				// Insert at end of file (inside last section)
				// Find last non-empty line
				lastNonEmpty := len(lines) - 1
				for lastNonEmpty >= sectionStartIdx && strings.TrimSpace(lines[lastNonEmpty]) == "" {
					lastNonEmpty--
				}
				lines = append(lines[:lastNonEmpty+1], append([]string{newKeyLine}, lines[lastNonEmpty+1:]...)...)
			}
		}
		newLines = lines
		existingContent = strings.Join(newLines, "\n")
	}

	// Validate resulting TOML syntax
	var dummy Config
	if err := toml.Unmarshal([]byte(existingContent), &dummy); err != nil {
		return fmt.Errorf("invalid TOML after modification: %w", err)
	}

	// Atomic write
	dir := filepath.Dir(filePath)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return fmt.Errorf("failed to create config directory %s: %w", dir, err)
	}

	tmpFile := filepath.Join(dir, fmt.Sprintf(".config.tmp.%d", os.Getpid()))
	if err := os.WriteFile(tmpFile, []byte(existingContent), 0600); err != nil {
		return fmt.Errorf("failed to write temporary config file: %w", err)
	}

	if err := os.Rename(tmpFile, filePath); err != nil {
		_ = os.Remove(tmpFile)
		return fmt.Errorf("failed to commit config file: %w", err)
	}

	return nil
}
