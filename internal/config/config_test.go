package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDefaultConfig(t *testing.T) {
	cfg := NewDefaultConfig()
	if cfg.General.Port != 8080 {
		t.Fatalf("expected port 8080, got %d", cfg.General.Port)
	}
	if cfg.General.Host != "127.0.0.1" {
		t.Fatalf("expected host 127.0.0.1, got %s", cfg.General.Host)
	}
	if cfg.SMTP.TransferEncoding != "8bit" {
		t.Fatalf("expected transfer encoding 8bit, got %s", cfg.SMTP.TransferEncoding)
	}
	if len(cfg.Tags) == 0 {
		t.Fatalf("expected default tags to be populated")
	}
	if cfg.Tags["$cl_1"].Color != "#CC0000" {
		t.Fatalf("expected $cl_1 color #CC0000, got %s", cfg.Tags["$cl_1"].Color)
	}
	if cfg.General.PIDFile == "" {
		t.Fatalf("expected default PIDFile to be populated")
	}
}

func TestLoadConfigFromTOML(t *testing.T) {
	tmpDir := t.TempDir()
	confPath := filepath.Join(tmpDir, "config.toml")
	customPID := filepath.Join(tmpDir, "custom.pid")

	content := `
[general]
port = 9090
host = "0.0.0.0"
editor_cmd = "nvim +{line} {file}"
pid_file = "` + customPID + `"

[mcp]
enabled = false
transport = "sse"
sse_port = 8085

[tags."$cl_1"]
tag = "Critical"
color = "#FF0000"
`
	if err := os.WriteFile(confPath, []byte(content), 0644); err != nil {
		t.Fatalf("failed to write test config: %v", err)
	}

	cfg, err := Load(confPath)
	if err != nil {
		t.Fatalf("failed to load config: %v", err)
	}

	if cfg.General.Port != 9090 {
		t.Fatalf("expected port 9090, got %d", cfg.General.Port)
	}
	if cfg.General.PIDFile != customPID {
		t.Fatalf("expected pid_file %s, got %s", customPID, cfg.General.PIDFile)
	}
	if cfg.General.Host != "0.0.0.0" {
		t.Fatalf("expected host 0.0.0.0, got %s", cfg.General.Host)
	}
	if cfg.MCP.Enabled {
		t.Fatalf("expected MCP to be disabled")
	}
	if cfg.MCP.Transport != "sse" || cfg.MCP.SSEPort != 8085 {
		t.Fatalf("expected MCP sse:8085, got %s:%d", cfg.MCP.Transport, cfg.MCP.SSEPort)
	}
	if cfg.Tags["$cl_1"].Tag != "Critical" {
		t.Fatalf("expected $cl_1 tag 'Critical', got %s", cfg.Tags["$cl_1"].Tag)
	}
	// Verify defaults merged for other tags
	if cfg.Tags["$cl_2"].Color != "#3333FF" {
		t.Fatalf("expected $cl_2 color #3333FF, got %s", cfg.Tags["$cl_2"].Color)
	}
}

func TestExpandPath(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip("no home dir")
	}

	expanded := ExpandPath("~/test/path")
	expected := filepath.Join(home, "test/path")
	if expanded != expected {
		t.Fatalf("expected %s, got %s", expected, expanded)
	}
}

func TestResolveTag(t *testing.T) {
	cfg := NewDefaultConfig()

	// 1. Empty or whitespace
	if res := cfg.ResolveTag(""); res != nil {
		t.Fatalf("expected nil for empty string, got %+v", res)
	}
	if res := cfg.ResolveTag("   "); res != nil {
		t.Fatalf("expected nil for spaces, got %+v", res)
	}

	// 2. Exact match
	res := cfg.ResolveTag("$cl_1")
	if res == nil || res.Key != "$cl_1" || res.Tag != "Red" || res.Color != "#CC0000" {
		t.Fatalf("unexpected result for $cl_1: %+v", res)
	}

	// 3. Case-insensitive
	res = cfg.ResolveTag("$CL_2")
	if res == nil || res.Key != "$cl_2" || res.Tag != "Blue" || res.Color != "#3333FF" {
		t.Fatalf("unexpected result for $CL_2: %+v", res)
	}

	// 4. Multiple flags separated by spaces and commas
	res = cfg.ResolveTag(`\Seen, $label4, something`)
	if res == nil || res.Key != "$label4" || res.Tag != "To Do" || res.Color != "#3333FF" {
		t.Fatalf("unexpected result for multiple flags: %+v", res)
	}

	// 5. No match
	if res := cfg.ResolveTag(`\Seen \Answered`); res != nil {
		t.Fatalf("expected nil for unmapped flags, got %+v", res)
	}
}
