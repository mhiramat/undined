package main

import (
	"fmt"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"undined-go/internal/config"
	"undined-go/internal/process"
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

func TestDaemonAndQuitFlags(t *testing.T) {
	tmpDir := t.TempDir()
	binPath := filepath.Join(tmpDir, "undined")
	buildCmd := exec.Command("go", "build", "-tags", "fts5", "-o", binPath, ".")
	if out, err := buildCmd.CombinedOutput(); err != nil {
		t.Fatalf("failed to build undined: %v, output: %s", err, string(out))
	}

	dataDir := filepath.Join(tmpDir, "data")
	pidPath := filepath.Join(tmpDir, "test.pid")
	logPath := filepath.Join(tmpDir, "daemon.log")
	cfgPath := filepath.Join(tmpDir, "config.toml")
	port := 28080

	cfgContent := fmt.Sprintf(`[general]
data_dir = "%s"
host = "127.0.0.1"
port = %d
log_file = "%s"
pid_file = "%s"
`, dataDir, port, logPath, pidPath)
	if err := os.WriteFile(cfgPath, []byte(cfgContent), 0644); err != nil {
		t.Fatalf("failed to write config: %v", err)
	}

	// 1. Initial quit when not running should report not running and exit 0
	cmdQuitInit := exec.Command(binPath, "--config", cfgPath, "--quit")
	outQuitInit, err := cmdQuitInit.CombinedOutput()
	if err != nil {
		t.Fatalf("quit when not running returned error: %v, output: %s", err, string(outQuitInit))
	}
	if !strings.Contains(string(outQuitInit), "not running") {
		t.Errorf("expected output to mention not running, got: %s", string(outQuitInit))
	}

	// 2. Start daemon
	cmdDaemon := exec.Command(binPath, "--config", cfgPath, "--daemon")
	outDaemon, err := cmdDaemon.CombinedOutput()
	if err != nil {
		t.Fatalf("daemon start failed: %v, output: %s", err, string(outDaemon))
	}
	outDaemonStr := string(outDaemon)

	// Verify startup information was displayed
	if !strings.Contains(outDaemonStr, "Starting Project UndineD") {
		t.Errorf("expected banner in daemon output: %s", outDaemonStr)
	}
	if !strings.Contains(outDaemonStr, fmt.Sprintf("127.0.0.1:%d", port)) {
		t.Errorf("expected port in daemon output: %s", outDaemonStr)
	}
	if !strings.Contains(outDaemonStr, pidPath) {
		t.Errorf("expected PID file in daemon output: %s", outDaemonStr)
	}
	if !strings.Contains(outDaemonStr, "Daemon started in background") {
		t.Errorf("expected daemon note in output: %s", outDaemonStr)
	}

	// 3. Verify PID file was written
	pidBytes, err := os.ReadFile(pidPath)
	if err != nil {
		t.Fatalf("failed to read PID file: %v", err)
	}
	daemonPID, err := strconv.Atoi(strings.TrimSpace(string(pidBytes)))
	if err != nil || daemonPID <= 0 {
		t.Fatalf("invalid daemon PID: %v (%s)", err, string(pidBytes))
	}

	// 4. Verify daemon process is running and responding on HTTP
	if !process.IsProcessRunning(daemonPID) {
		t.Fatalf("daemon PID %d is not running", daemonPID)
	}

	// Make HTTP request to verify server is listening
	resp, err := http.Get(fmt.Sprintf("http://127.0.0.1:%d/api/v1/auth/status", port))
	if err != nil {
		t.Fatalf("failed to query running daemon HTTP endpoint: %v", err)
	}
	_ = resp.Body.Close()

	// 5. Attempting to start another daemon with same PID file should fail
	cmdDuplicate := exec.Command(binPath, "--config", cfgPath, "--daemon")
	outDup, err := cmdDuplicate.CombinedOutput()
	if err == nil {
		t.Fatalf("expected duplicate daemon start to fail, but succeeded with output: %s", string(outDup))
	}
	if !strings.Contains(string(outDup), "already running") {
		t.Errorf("expected 'already running' in duplicate output, got: %s", string(outDup))
	}

	// 6. Terminate running daemon using --quit
	cmdQuit := exec.Command(binPath, "--config", cfgPath, "--quit")
	outQuit, err := cmdQuit.CombinedOutput()
	if err != nil {
		t.Fatalf("quit failed: %v, output: %s", err, string(outQuit))
	}
	outQuitStr := string(outQuit)
	if !strings.Contains(outQuitStr, "stopped successfully") {
		t.Errorf("expected 'stopped successfully' in quit output, got: %s", outQuitStr)
	}

	// Wait up to 2 seconds to ensure daemon process is completely dead
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if !process.IsProcessRunning(daemonPID) {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if process.IsProcessRunning(daemonPID) {
		t.Fatalf("daemon PID %d still running after --quit", daemonPID)
	}

	// Verify PID file was cleaned up
	if _, err := os.Stat(pidPath); !os.IsNotExist(err) {
		t.Errorf("expected PID file %s to be removed, but still exists", pidPath)
	}

	// 7. Another quit after termination should report not running
	cmdQuitAgain := exec.Command(binPath, "--config", cfgPath, "-q")
	outQuitAgain, err := cmdQuitAgain.CombinedOutput()
	if err != nil {
		t.Fatalf("second quit failed: %v, output: %s", err, string(outQuitAgain))
	}
	if !strings.Contains(string(outQuitAgain), "not running") {
		t.Errorf("expected 'not running' in second quit output, got: %s", string(outQuitAgain))
	}
}

func TestDaemonAndMCPConflict(t *testing.T) {
	tmpDir := t.TempDir()
	binPath := filepath.Join(tmpDir, "undined")
	buildCmd := exec.Command("go", "build", "-tags", "fts5", "-o", binPath, ".")
	if out, err := buildCmd.CombinedOutput(); err != nil {
		t.Fatalf("failed to build undined: %v, output: %s", err, string(out))
	}

	cmd := exec.Command(binPath, "--daemon", "--mcp-stdio")
	out, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatalf("expected --daemon with --mcp-stdio to fail, but succeeded with output: %s", string(out))
	}
	if !strings.Contains(string(out), "cannot be used with --mcp-stdio") {
		t.Errorf("expected conflict message in output, got: %s", string(out))
	}
}

func TestForegroundPIDFile(t *testing.T) {
	tmpDir := t.TempDir()
	binPath := filepath.Join(tmpDir, "undined")
	buildCmd := exec.Command("go", "build", "-tags", "fts5", "-o", binPath, ".")
	if out, err := buildCmd.CombinedOutput(); err != nil {
		t.Fatalf("failed to build undined: %v, output: %s", err, string(out))
	}

	dataDir := filepath.Join(tmpDir, "data")
	pidPath := filepath.Join(tmpDir, "foreground.pid")
	logPath := filepath.Join(tmpDir, "foreground.log")
	cfgPath := filepath.Join(tmpDir, "config.toml")
	port := 28081

	cfgContent := fmt.Sprintf(`[general]
data_dir = "%s"
host = "127.0.0.1"
port = %d
log_file = "%s"
pid_file = "%s"
`, dataDir, port, logPath, pidPath)
	if err := os.WriteFile(cfgPath, []byte(cfgContent), 0644); err != nil {
		t.Fatalf("failed to write config: %v", err)
	}

	cmd := exec.Command(binPath, "--config", cfgPath)
	if err := cmd.Start(); err != nil {
		t.Fatalf("failed to start foreground server: %v", err)
	}

	// Wait up to 3 seconds for PID file and HTTP server to become ready
	var pid int
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if pidBytes, err := os.ReadFile(pidPath); err == nil {
			if p, err := strconv.Atoi(strings.TrimSpace(string(pidBytes))); err == nil && p == cmd.Process.Pid {
				pid = p
				break
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	if pid == 0 {
		_ = cmd.Process.Kill()
		t.Fatalf("foreground server did not write PID file within deadline")
	}

	// Verify server responds
	resp, err := http.Get(fmt.Sprintf("http://127.0.0.1:%d/api/v1/auth/status", port))
	if err != nil {
		_ = cmd.Process.Kill()
		t.Fatalf("foreground server failed to respond on HTTP: %v", err)
	}
	_ = resp.Body.Close()

	// Stop using --quit
	cmdQuit := exec.Command(binPath, "--config", cfgPath, "--quit")
	if out, err := cmdQuit.CombinedOutput(); err != nil {
		t.Fatalf("quit failed: %v, output: %s", err, string(out))
	}

	// Wait for foreground process to exit
	_ = cmd.Wait()

	// Verify PID file was removed
	if _, err := os.Stat(pidPath); !os.IsNotExist(err) {
		t.Errorf("expected PID file %s to be removed, but still exists", pidPath)
	}
}



