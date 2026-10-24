package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDisplayPath(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		t.Skip("no home directory available")
	}

	p1 := filepath.Join(home, ".config", "undined", "config.toml")
	disp1 := DisplayPath(p1)
	if disp1 != "~/.config/undined/config.toml" {
		t.Errorf("expected ~/.config/undined/config.toml, got %s", disp1)
	}

	if DisplayPath("") != "" {
		t.Errorf("expected empty string for empty path")
	}

	if DisplayPath("/var/log/syslog") != "/var/log/syslog" {
		t.Errorf("expected unchanged non-home path")
	}
}

func TestFormatTOMLValue(t *testing.T) {
	tests := []struct {
		key      string
		val      string
		expected string
	}{
		{"general.port", "8080", "8080"},
		{"patchwork.enabled", "true", "true"},
		{"patchwork.enabled", "FALSE", "false"},
		{"patchwork.timeout_seconds", "15.5", "15.5"},
		{"general.editor_cmd", "nvim +{line} {file}", `"nvim +{line} {file}"`},
		{"patchwork.delegate-users", `["dev", "reviewer"]`, `["dev", "reviewer"]`},
		{"patchwork.delegate-users", `[dev, reviewer]`, `["dev", "reviewer"]`},
		{"patchwork.delegate-users", `dev, reviewer`, `["dev", "reviewer"]`},
		{"patchwork.preferred_archives", `linux-trace`, `"linux-trace"`},
	}

	for _, tc := range tests {
		got := FormatTOMLValue(tc.key, tc.val)
		if got != tc.expected {
			t.Errorf("FormatTOMLValue(%q, %q) = %q, expected %q", tc.key, tc.val, got, tc.expected)
		}
	}
}

func TestGetConfigValue(t *testing.T) {
	cfg := NewDefaultConfig()
	cfg.General.Port = 9090
	cfg.Patchwork.Username = "dev"
	cfg.Patchwork.DelegateUsers = []string{"dev", "maintainer2"}

	val, err := GetConfigValue(cfg, "general.port")
	if err != nil || val != "9090" {
		t.Fatalf("expected 9090, got %q (err: %v)", val, err)
	}

	val, err = GetConfigValue(cfg, "patchwork.username")
	if err != nil || val != "dev" {
		t.Fatalf("expected dev, got %q (err: %v)", val, err)
	}

	val, err = GetConfigValue(cfg, "patchwork.delegate-users")
	if err != nil || val != `["dev", "maintainer2"]` {
		t.Fatalf(`expected ["dev", "maintainer2"], got %q (err: %v)`, val, err)
	}

	val, err = GetConfigValue(cfg, "patchwork.delegate_users")
	if err != nil || val != `["dev", "maintainer2"]` {
		t.Fatalf(`expected ["dev", "maintainer2"], got %q (err: %v)`, val, err)
	}

	_, err = GetConfigValue(cfg, "unknown.key")
	if err == nil {
		t.Fatalf("expected error for unknown key")
	}
}

func TestSetTOMLValue(t *testing.T) {
	tmpDir := t.TempDir()
	confPath := filepath.Join(tmpDir, "config.toml")

	initialContent := `# Configuration File
[general]
# Web server port
port = 8080
host = "127.0.0.1"

[patchwork]
# Existing comment
enabled = true
# Another comment
delegate-users = ["dev"]

[tags."$cl_1"]
tag = "Critical"
`
	if err := os.WriteFile(confPath, []byte(initialContent), 0600); err != nil {
		t.Fatalf("failed to write initial file: %v", err)
	}

	// 1. Update existing key: patchwork.delegate-users
	err := SetTOMLValue(confPath, "patchwork.delegate-users", `["dev", "reviewer", "maintainer"]`)
	if err != nil {
		t.Fatalf("SetTOMLValue failed: %v", err)
	}

	contentBytes, _ := os.ReadFile(confPath)
	content := string(contentBytes)

	if !strings.Contains(content, `delegate-users = ["dev", "reviewer", "maintainer"]`) {
		t.Errorf("expected updated delegate-users in file, got:\n%s", content)
	}
	// Verify comments were preserved
	if !strings.Contains(content, "# Existing comment") || !strings.Contains(content, "# Another comment") {
		t.Errorf("expected comments to be preserved, got:\n%s", content)
	}

	// 2. Insert new key in existing section: patchwork.default_project
	err = SetTOMLValue(confPath, "patchwork.default_project", "linux-trace")
	if err != nil {
		t.Fatalf("SetTOMLValue insert failed: %v", err)
	}

	contentBytes, _ = os.ReadFile(confPath)
	content = string(contentBytes)
	if !strings.Contains(content, `default_project = "linux-trace"`) {
		t.Errorf("expected inserted default_project in file, got:\n%s", content)
	}

	// 3. Add key to brand new section: lkml.b4_cmd
	err = SetTOMLValue(confPath, "lkml.b4_cmd", "b4")
	if err != nil {
		t.Fatalf("SetTOMLValue new section failed: %v", err)
	}

	contentBytes, _ = os.ReadFile(confPath)
	content = string(contentBytes)
	if !strings.Contains(content, "[lkml]") || !strings.Contains(content, `b4_cmd = "b4"`) {
		t.Errorf("expected new section [lkml] with b4_cmd, got:\n%s", content)
	}

	// 4. Verify file can be cleanly loaded by config.Load
	cfg, err := Load(confPath)
	if err != nil {
		t.Fatalf("failed to reload modified config: %v", err)
	}
	users := cfg.Patchwork.GetDelegateUsers()
	if len(users) != 3 || users[1] != "reviewer" {
		t.Errorf("unexpected loaded delegate users: %#v", users)
	}
	if cfg.LKML.B4Cmd != "b4" {
		t.Errorf("expected b4_cmd 'b4', got %q", cfg.LKML.B4Cmd)
	}
	if cfg.Patchwork.DefaultProject != "linux-trace" {
		t.Errorf("expected default_project 'linux-trace', got %q", cfg.Patchwork.DefaultProject)
	}

	// 5. Verify error on invalid key format
	err = SetTOMLValue(confPath, "invalidkey", "val")
	if err == nil {
		t.Fatalf("expected error on invalid key format without dot")
	}

	// 6. Verify atomic write created file with 0600 mode
	info, err := os.Stat(confPath)
	if err != nil {
		t.Fatalf("stat failed: %v", err)
	}
	// On unix, check owner read/write (0600)
	if info.Mode().Perm()&0077 != 0 {
		t.Errorf("expected strict permissions (0600), got %o", info.Mode().Perm())
	}
}
