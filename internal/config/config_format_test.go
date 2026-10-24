package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestFormatActiveConfig_NonExistentFile(t *testing.T) {
	tmpDir := t.TempDir()
	confPath := filepath.Join(tmpDir, "does_not_exist.toml")

	out, err := FormatActiveConfig(confPath)
	if err != nil {
		t.Fatalf("unexpected error for non-existent config file: %v", err)
	}

	// Verify all expected sections are present
	sections := []string{"[general]", "[imap]", "[smtp]", "[lkml]", "[mcp]", "[review]", "[patchwork]", "[tags]"}
	for _, sec := range sections {
		if !strings.Contains(out, sec) {
			t.Errorf("expected section %s in output, got:\n%s", sec, out)
		}
	}

	// In a non-existent file, all settings must be commented out with defaults
	expectedCommented := []string{
		`# host = "127.0.0.1"`,
		`# port = 8080`,
		`# debug = false`,
		`# poll_interval_seconds = 60`,
		`# from_name = "Kernel Developer"`,
		`# delegate-users = []`,
		`# "$cl_1" = { tag = "Red", color = "#CC0000" }`,
	}
	for _, exp := range expectedCommented {
		if !strings.Contains(out, exp) {
			t.Errorf("expected commented default %q in output, got:\n%s", exp, out)
		}
	}
}

func TestFormatActiveConfig_ConfiguredAndObsolete(t *testing.T) {
	tmpDir := t.TempDir()
	confPath := filepath.Join(tmpDir, "config.toml")

	initialContent := `# Custom test config with obsolete settings
[general]
port = 9999
reply_use_external_editor = true
obsolete_unused_option = "obsolete_value"

[patchwork]
delegate-users = ["dev", "maintainer2"]
deprecated_patchwork_param = 42

[unknown_section]
junk = true
`
	if err := os.WriteFile(confPath, []byte(initialContent), 0600); err != nil {
		t.Fatalf("failed to write test config: %v", err)
	}

	out, err := FormatActiveConfig(confPath)
	if err != nil {
		t.Fatalf("FormatActiveConfig failed: %v", err)
	}

	// 1. Configured settings must be present without a leading '#'
	lines := strings.Split(out, "\n")
	hasActivePort := false
	hasActiveReplyEditor := false
	hasActiveDelegateUsers := false

	for _, l := range lines {
		trimmed := strings.TrimSpace(l)
		if trimmed == `port = 9999` {
			hasActivePort = true
		}
		if trimmed == `reply_use_external_editor = true` {
			hasActiveReplyEditor = true
		}
		if trimmed == `delegate-users = ["dev", "maintainer2"]` {
			hasActiveDelegateUsers = true
		}
	}

	if !hasActivePort {
		t.Errorf("expected active 'port = 9999' in output:\n%s", out)
	}
	if !hasActiveReplyEditor {
		t.Errorf("expected active 'reply_use_external_editor = true' in output:\n%s", out)
	}
	if !hasActiveDelegateUsers {
		t.Errorf("expected active 'delegate-users = [\"dev\", \"maintainer2\"]' in output:\n%s", out)
	}

	// 2. Unconfigured settings in configured sections must be preceded by '#'
	hasCommentedHost := false
	hasCommentedDebug := false
	hasCommentedURL := false

	for _, l := range lines {
		trimmed := strings.TrimSpace(l)
		if trimmed == `# host = "127.0.0.1"` {
			hasCommentedHost = true
		}
		if trimmed == `# debug = false` {
			hasCommentedDebug = true
		}
		if trimmed == `# url = "https://patchwork.kernel.org"` {
			hasCommentedURL = true
		}
	}

	if !hasCommentedHost {
		t.Errorf("expected '# host = \"127.0.0.1\"' in output:\n%s", out)
	}
	if !hasCommentedDebug {
		t.Errorf("expected '# debug = false' in output:\n%s", out)
	}
	if !hasCommentedURL {
		t.Errorf("expected '# url = \"https://patchwork.kernel.org\"' in output:\n%s", out)
	}

	// 3. Obsolete settings and unknown sections must be omitted
	if strings.Contains(out, "obsolete_unused_option") {
		t.Errorf("expected obsolete_unused_option to be omitted from output:\n%s", out)
	}
	if strings.Contains(out, "deprecated_patchwork_param") {
		t.Errorf("expected deprecated_patchwork_param to be omitted from output:\n%s", out)
	}
	if strings.Contains(out, "unknown_section") || strings.Contains(out, "junk") {
		t.Errorf("expected unknown_section to be omitted from output:\n%s", out)
	}
}

func TestFormatActiveConfig_CustomTags(t *testing.T) {
	tmpDir := t.TempDir()
	confPath := filepath.Join(tmpDir, "config.toml")

	initialContent := `[tags."$cl_1"]
tag = "Critical"
color = "#FF0000"
`
	if err := os.WriteFile(confPath, []byte(initialContent), 0600); err != nil {
		t.Fatalf("failed to write test config: %v", err)
	}

	out, err := FormatActiveConfig(confPath)
	if err != nil {
		t.Fatalf("FormatActiveConfig failed: %v", err)
	}

	// Active tag should be uncommented
	if !strings.Contains(out, `"$cl_1" = { tag = "Critical", color = "#FF0000" }`) {
		t.Errorf("expected active custom tag in output:\n%s", out)
	}

	// Unconfigured default tag should be commented with '#'
	if !strings.Contains(out, `# "$cl_2" = { tag = "Blue", color = "#3333FF" }`) {
		t.Errorf("expected commented default tag $cl_2 in output:\n%s", out)
	}
}
