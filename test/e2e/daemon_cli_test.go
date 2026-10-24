package e2e

import (
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

func getFreePort(t *testing.T) int {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to obtain free port: %v", err)
	}
	port := l.Addr().(*net.TCPAddr).Port
	_ = l.Close()
	return port
}

func findRepoRoot(t *testing.T) string {
	out, err := exec.Command("git", "rev-parse", "--show-toplevel").Output()
	if err != nil {
		t.Fatalf("failed to find git repository root: %v", err)
	}
	return strings.TrimSpace(string(out))
}

// Scenario S9: Daemon Process Lifecycle & Config CLI
func TestE2E_DaemonLifecycleAndCLIConfig(t *testing.T) {
	repoRoot := findRepoRoot(t)
	workspaceDir := t.TempDir()

	binPath := filepath.Join(workspaceDir, "undined")
	buildCmd := exec.Command("go", "build", "-tags", "fts5", "-o", binPath, "./cmd/undined")
	buildCmd.Dir = repoRoot
	if out, err := buildCmd.CombinedOutput(); err != nil {
		t.Fatalf("failed to build undined: %s (%v)", string(out), err)
	}

	port := getFreePort(t)
	configPath := filepath.Join(workspaceDir, "config.toml")
	pidPath := filepath.Join(workspaceDir, "undined.pid")
	logPath := filepath.Join(workspaceDir, "undined.log")
	dbPath := filepath.Join(workspaceDir, "undined.db")
	maildirPath := filepath.Join(workspaceDir, "maildir")

	confContent := fmt.Sprintf(`[general]
data_dir = %q
maildir_path = %q
db_path = %q
pid_file = %q
log_file = %q
host = "127.0.0.1"
port = %d

[patchwork]
enabled = true
delegate-users = ["dev", "maintainer"]

[imap]
poll_interval_seconds = 3600
`, workspaceDir, maildirPath, dbPath, pidPath, logPath, port)

	if err := os.WriteFile(configPath, []byte(confContent), 0600); err != nil {
		t.Fatalf("failed to write config.toml: %v", err)
	}

	// 1. Test CLI 'config get'
	getCmd := exec.Command(binPath, "config", "get", "patchwork.delegate-users", "-C", configPath)
	out, err := getCmd.CombinedOutput()
	if err != nil {
		t.Fatalf("config get failed: %s: %v", string(out), err)
	}
	if !strings.Contains(string(out), "dev") || !strings.Contains(string(out), "maintainer") {
		t.Errorf("expected delegate users in config get output, got: %s", string(out))
	}

	// 2. Test CLI 'config set'
	setCmd := exec.Command(binPath, "config", "set", "patchwork.delegate-users", "dev,maintainer2,reviewer", "-C", configPath)
	out, err = setCmd.CombinedOutput()
	if err != nil {
		t.Fatalf("config set failed: %s: %v", string(out), err)
	}

	// Verify 'config get' reflects updated delegate-users
	getCmd2 := exec.Command(binPath, "config", "get", "patchwork.delegate-users", "-C", configPath)
	out, err = getCmd2.CombinedOutput()
	if err != nil {
		t.Fatalf("config get after set failed: %s: %v", string(out), err)
	}
	if !strings.Contains(string(out), "maintainer2") || !strings.Contains(string(out), "reviewer") {
		t.Errorf("expected updated delegate users, got: %s", string(out))
	}

	// 3. Start daemon with --daemon
	startCmd := exec.Command(binPath, "--daemon", "-C", configPath)
	out, err = startCmd.CombinedOutput()
	if err != nil {
		t.Fatalf("undined --daemon failed: %s: %v", string(out), err)
	}

	// Register cleanup to ensure daemon is terminated
	t.Cleanup(func() {
		stopCmd := exec.Command(binPath, "--quit", "-C", configPath)
		_ = stopCmd.Run()
		if pidBytes, err := os.ReadFile(pidPath); err == nil {
			if pid, err := strconv.Atoi(strings.TrimSpace(string(pidBytes))); err == nil && pid > 0 {
				_ = syscall.Kill(pid, syscall.SIGKILL)
			}
		}
	})

	// 4. Verify PID file exists and contains a valid running process PID
	pidBytes, err := os.ReadFile(pidPath)
	if err != nil {
		t.Fatalf("failed to read PID file: %v", err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(pidBytes)))
	if err != nil || pid <= 0 {
		t.Fatalf("invalid PID in PID file: %q", string(pidBytes))
	}

	if err := syscall.Kill(pid, 0); err != nil {
		t.Fatalf("daemon process %d is not running: %v", pid, err)
	}

	// 5. Verify HTTP service is accessible
	client := &http.Client{Timeout: 3 * time.Second}
	loginURL := fmt.Sprintf("http://127.0.0.1:%d/login", port)

	var resp *http.Response
	for i := 0; i < 10; i++ {
		resp, err = client.Get(loginURL)
		if err == nil {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if err != nil {
		t.Fatalf("failed to connect to daemon HTTP endpoint %s: %v", loginURL, err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected 200 OK from %s, got %d", loginURL, resp.StatusCode)
	}

	// 6. Stop daemon with --quit
	stopCmd := exec.Command(binPath, "--quit", "-C", configPath)
	out, err = stopCmd.CombinedOutput()
	if err != nil {
		t.Fatalf("undined --quit failed: %s: %v", string(out), err)
	}

	// Verify daemon terminates
	stopped := false
	for i := 0; i < 30; i++ {
		if err := syscall.Kill(pid, 0); err != nil {
			stopped = true
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if !stopped {
		t.Errorf("daemon process %d did not terminate after --quit", pid)
	}

	// Verify PID file is cleaned up
	if _, err := os.Stat(pidPath); !os.IsNotExist(err) {
		t.Errorf("PID file %s was not cleaned up after --quit", pidPath)
	}
}
