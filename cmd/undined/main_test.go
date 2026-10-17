package main

import (
	"fmt"
	"log"
	"os"
	"os/exec"
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

func TestRenewTokenAndNoServerFlags(t *testing.T) {
	tmpDir := t.TempDir()
	binPath := filepath.Join(tmpDir, "undined")
	buildCmd := exec.Command("go", "build", "-tags", "fts5", "-o", binPath, ".")
	if out, err := buildCmd.CombinedOutput(); err != nil {
		t.Fatalf("failed to build undined: %v, output: %s", err, string(out))
	}

	dataDir := filepath.Join(tmpDir, "data")
	cfgPath := filepath.Join(tmpDir, "config.toml")
	cfgContent := fmt.Sprintf(`[general]
data_dir = "%s"
host = "127.0.0.1"
port = 18080
log_file = "%s"
`, dataDir, filepath.Join(tmpDir, "test.log"))
	if err := os.WriteFile(cfgPath, []byte(cfgContent), 0644); err != nil {
		t.Fatalf("failed to write config: %v", err)
	}

	// Run 1: --no-server (initial token creation)
	cmd1 := exec.Command(binPath, "--config", cfgPath, "--no-server")
	out1, err := cmd1.CombinedOutput()
	if err != nil {
		t.Fatalf("run 1 failed: %v, output: %s", err, string(out1))
	}
	outStr1 := string(out1)
	if !strings.Contains(outStr1, "UndineD Authentication Token") {
		t.Errorf("expected banner in output, got: %s", outStr1)
	}
	tokenFile := filepath.Join(dataDir, "auth_token")
	tokenBytes1, err := os.ReadFile(tokenFile)
	if err != nil {
		t.Fatalf("failed to read token file: %v", err)
	}
	token1 := strings.TrimSpace(string(tokenBytes1))
	if len(token1) != 64 {
		t.Fatalf("expected 64-char hex token, got %q", token1)
	}
	if !strings.Contains(outStr1, token1) {
		t.Errorf("expected output to contain token %s", token1)
	}

	// Run 2: --no-server without --renew-token (should preserve token)
	cmd2 := exec.Command(binPath, "--config", cfgPath, "--no-server")
	out2, err := cmd2.CombinedOutput()
	if err != nil {
		t.Fatalf("run 2 failed: %v, output: %s", err, string(out2))
	}
	tokenBytes2, err := os.ReadFile(tokenFile)
	if err != nil {
		t.Fatalf("failed to read token file: %v", err)
	}
	token2 := strings.TrimSpace(string(tokenBytes2))
	if token2 != token1 {
		t.Fatalf("expected preserved token %s, got %s", token1, token2)
	}

	// Run 3: --renew-token --no-server (should generate new token)
	cmd3 := exec.Command(binPath, "--config", cfgPath, "--renew-token", "--no-server")
	out3, err := cmd3.CombinedOutput()
	if err != nil {
		t.Fatalf("run 3 failed: %v, output: %s", err, string(out3))
	}
	outStr3 := string(out3)
	if !strings.Contains(outStr3, "UndineD Authentication Token Renewed") {
		t.Errorf("expected 'Token Renewed' banner, got: %s", outStr3)
	}
	tokenBytes3, err := os.ReadFile(tokenFile)
	if err != nil {
		t.Fatalf("failed to read token file: %v", err)
	}
	token3 := strings.TrimSpace(string(tokenBytes3))
	if len(token3) != 64 {
		t.Fatalf("expected 64-char token, got %q", token3)
	}
	if token3 == token1 {
		t.Fatalf("expected new token different from old token, got %s", token3)
	}
	if !strings.Contains(outStr3, token3) {
		t.Errorf("expected output to contain new token %s", token3)
	}
}

