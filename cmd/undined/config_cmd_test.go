package main

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func captureOutput(f func()) string {
	oldStdout := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w

	f()

	_ = w.Close()
	os.Stdout = oldStdout

	var buf bytes.Buffer
	_, _ = io.Copy(&buf, r)
	return buf.String()
}

func TestConfigSubcommands(t *testing.T) {
	tmpDir := t.TempDir()
	confPath := filepath.Join(tmpDir, "config.toml")

	initial := `[general]
host = "127.0.0.1"
port = 8080

[patchwork]
enabled = true
username = "dev"
delegate-users = ["dev", "maintainer2"]
`
	if err := os.WriteFile(confPath, []byte(initial), 0600); err != nil {
		t.Fatalf("failed to write test config: %v", err)
	}

	// 1. undined config path
	out := captureOutput(func() {
		code := runConfigCommand([]string{"-C", confPath, "path"})
		if code != 0 {
			t.Errorf("expected exit code 0, got %d", code)
		}
	})
	if !strings.Contains(out, "config.toml") {
		t.Errorf("expected path in output, got %q", out)
	}

	// 2. undined config get
	out = captureOutput(func() {
		code := runConfigCommand([]string{"-C", confPath, "get", "patchwork.delegate-users"})
		if code != 0 {
			t.Errorf("expected exit code 0, got %d", code)
		}
	})
	if !strings.Contains(out, `["dev", "maintainer2"]`) {
		t.Errorf("expected delegate users in output, got %q", out)
	}

	// 3. undined config set
	out = captureOutput(func() {
		code := runConfigCommand([]string{"-C", confPath, "set", "patchwork.delegate-users", `["dev", "maintainer2", "reviewer"]`})
		if code != 0 {
			t.Errorf("expected exit code 0, got %d", code)
		}
	})
	if !strings.Contains(out, "Updated patchwork.delegate-users") {
		t.Errorf("expected update confirmation, got %q", out)
	}

	// Verify update via get
	out = captureOutput(func() {
		code := runConfigCommand([]string{"-C", confPath, "get", "patchwork.delegate-users"})
		if code != 0 {
			t.Errorf("expected exit code 0, got %d", code)
		}
	})
	if !strings.Contains(out, `["dev", "maintainer2", "reviewer"]`) {
		t.Errorf("expected updated delegate users, got %q", out)
	}

	// 4. undined config show
	out = captureOutput(func() {
		code := runConfigCommand([]string{"-C", confPath, "show"})
		if code != 0 {
			t.Errorf("expected exit code 0, got %d", code)
		}
	})
	if !strings.Contains(out, "Configuration:") || !strings.Contains(out, `["dev", "maintainer2", "reviewer"]`) {
		t.Errorf("expected config contents in show, got %q", out)
	}

	// 5. undined config help
	out = captureOutput(func() {
		code := runConfigCommand([]string{"help"})
		if code != 0 {
			t.Errorf("expected exit code 0, got %d", code)
		}
	})
	if !strings.Contains(out, "Usage: undined config") {
		t.Errorf("expected help output, got %q", out)
	}
}

func TestConfigFlagsDirect(t *testing.T) {
	tmpDir := t.TempDir()
	confPath := filepath.Join(tmpDir, "config.toml")

	initial := `[general]
port = 9000

[patchwork]
username = "maintainer"
`
	if err := os.WriteFile(confPath, []byte(initial), 0600); err != nil {
		t.Fatalf("failed to write test config: %v", err)
	}

	// Test doShowConfigPath
	out := captureOutput(func() {
		_ = doShowConfigPath(confPath)
	})
	if !strings.Contains(out, "config.toml") {
		t.Errorf("expected path, got %q", out)
	}

	// Test doConfigGet
	out = captureOutput(func() {
		_ = doConfigGet(confPath, "patchwork.username")
	})
	if strings.TrimSpace(out) != "maintainer" {
		t.Errorf("expected 'maintainer', got %q", out)
	}

	// Test doConfigSet
	out = captureOutput(func() {
		_ = doConfigSet(confPath, "general.port=9090")
	})
	if !strings.Contains(out, "Updated general.port") {
		t.Errorf("expected confirmation, got %q", out)
	}

	out = captureOutput(func() {
		_ = doConfigGet(confPath, "general.port")
	})
	if strings.TrimSpace(out) != "9090" {
		t.Errorf("expected '9090', got %q", out)
	}

	// Test doShowConfig
	out = captureOutput(func() {
		_ = doShowConfig(confPath)
	})
	if !strings.Contains(out, "port = 9090") {
		t.Errorf("expected active 'port = 9090' in show config output: %s", out)
	}
	if !strings.Contains(out, `# host = "127.0.0.1"`) {
		t.Errorf("expected commented default host in show config output: %s", out)
	}
}
