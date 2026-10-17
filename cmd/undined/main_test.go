package main

import (
	"log"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"undined-go/internal/config"
)

func TestFindFrontendDir(t *testing.T) {
	tmpDir := t.TempDir()
	frontendDir := filepath.Join(tmpDir, "frontend")
	_ = os.MkdirAll(frontendDir, 0755)
	_ = os.WriteFile(filepath.Join(frontendDir, "index.html"), []byte("<html></html>"), 0644)

	found := findFrontendDir(frontendDir)
	if found != frontendDir {
		t.Errorf("expected to find %s, got %s", frontendDir, found)
	}

	notFound := findFrontendDir("/nonexistent/path")
	if notFound == "/nonexistent/path" {
		t.Errorf("expected not to return nonexistent path")
	}
}

func TestSetupLogging(t *testing.T) {
	tmpDir := t.TempDir()
	logPath := filepath.Join(tmpDir, "logs", "undined.log")

	cfg := &config.Config{
		General: config.GeneralConfig{
			LogFile: logPath,
		},
	}

	f := setupLogging(cfg, false)
	if f == nil {
		t.Fatalf("expected non-nil file handle from setupLogging")
	}
	defer func() {
		_ = f.Close()
		log.SetOutput(os.Stderr)
	}()

	testMsg := "test log message for undined"
	log.Println(testMsg)

	// Verify file was written
	content, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("failed to read log file: %v", err)
	}
	if !strings.Contains(string(content), testMsg) {
		t.Errorf("expected log file to contain %q, got: %s", testMsg, string(content))
	}
}

func TestSetupLoggingEmpty(t *testing.T) {
	f := setupLogging(nil, false)
	if f != nil {
		t.Errorf("expected nil for nil config")
	}

	cfg := &config.Config{}
	f = setupLogging(cfg, false)
	if f != nil {
		t.Errorf("expected nil for empty log file path")
	}
}

func TestSetupLoggingMCPStdio(t *testing.T) {
	tmpDir := t.TempDir()
	logPath := filepath.Join(tmpDir, "mcp.log")

	cfg := &config.Config{
		General: config.GeneralConfig{
			LogFile: logPath,
		},
	}

	f := setupLogging(cfg, true)
	if f == nil {
		t.Fatalf("expected non-nil file handle for MCP mode")
	}
	defer func() {
		_ = f.Close()
		log.SetOutput(os.Stderr)
	}()

	testMsg := "mcp mode log entry"
	log.Println(testMsg)

	content, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("failed to read mcp log file: %v", err)
	}
	if !strings.Contains(string(content), testMsg) {
		t.Errorf("expected mcp log to contain %q, got: %s", testMsg, string(content))
	}
}
