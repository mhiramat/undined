package process

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

func TestPIDFileReadWriteRemove(t *testing.T) {
	tmpDir := t.TempDir()
	pidPath := filepath.Join(tmpDir, "subdir", "test.pid")

	// 1. Initial read should fail
	_, err := ReadPIDFile(pidPath)
	if err == nil {
		t.Fatalf("expected error reading non-existent PID file")
	}

	// 2. Write PID file
	testPID := 12345
	if err := WritePIDFile(pidPath, testPID); err != nil {
		t.Fatalf("failed to write PID file: %v", err)
	}

	// 3. Read PID file
	readPID, err := ReadPIDFile(pidPath)
	if err != nil {
		t.Fatalf("failed to read PID file: %v", err)
	}
	if readPID != testPID {
		t.Fatalf("expected PID %d, got %d", testPID, readPID)
	}

	// 4. Remove PID file
	if err := RemovePIDFile(pidPath); err != nil {
		t.Fatalf("failed to remove PID file: %v", err)
	}

	// 5. Remove non-existent PID file should not error
	if err := RemovePIDFile(pidPath); err != nil {
		t.Fatalf("removing non-existent PID file returned error: %v", err)
	}
}

func TestIsProcessRunning(t *testing.T) {
	myPID := os.Getpid()
	if !IsProcessRunning(myPID) {
		t.Errorf("expected current process %d to be running", myPID)
	}

	// Invalid / non-existent PID
	if IsProcessRunning(-1) {
		t.Errorf("expected negative PID to not be running")
	}
	if IsProcessRunning(0) {
		t.Errorf("expected PID 0 to not be running")
	}
	if IsProcessRunning(99999999) {
		t.Errorf("expected huge PID to not be running")
	}
}

func TestCheckRunning(t *testing.T) {
	tmpDir := t.TempDir()
	pidPath := filepath.Join(tmpDir, "undined.pid")

	// 1. No file -> not running
	pid, running, err := CheckRunning(pidPath)
	if err != nil || running || pid != 0 {
		t.Fatalf("expected 0, false, nil; got %d, %v, %v", pid, running, err)
	}

	// 2. Stale PID -> not running
	if err := WritePIDFile(pidPath, 9999999); err != nil {
		t.Fatalf("failed to write stale PID: %v", err)
	}
	pid, running, err = CheckRunning(pidPath)
	if err != nil || running || pid != 9999999 {
		t.Fatalf("expected 9999999, false, nil; got %d, %v, %v", pid, running, err)
	}

	// 3. Current process with name set to undined
	SetProcessName("undined")
	if err := WritePIDFile(pidPath, os.Getpid()); err != nil {
		t.Fatalf("failed to write current PID: %v", err)
	}
	pid, running, err = CheckRunning(pidPath)
	if err != nil || !running || pid != os.Getpid() {
		t.Fatalf("expected %d, true, nil; got %d, %v, %v", os.Getpid(), pid, running, err)
	}
}

func TestTerminateProcess(t *testing.T) {
	// Spawn a dummy process: sleep 30
	cmd := exec.Command("sleep", "30")
	if err := cmd.Start(); err != nil {
		t.Fatalf("failed to start sleep: %v", err)
	}
	pid := cmd.Process.Pid

	if !IsProcessRunning(pid) {
		t.Fatalf("spawned process %d should be running", pid)
	}

	// Terminate process
	if err := TerminateProcess(pid, 2*time.Second); err != nil {
		t.Fatalf("failed to terminate process %d: %v", pid, err)
	}

	// Ensure process is dead
	_ = cmd.Wait()
	if IsProcessRunning(pid) {
		t.Fatalf("process %d still running after TerminateProcess", pid)
	}

	// Terminating non-running process returns ErrNotRunning
	if err := TerminateProcess(pid, 1*time.Second); err != ErrNotRunning {
		t.Fatalf("expected ErrNotRunning, got %v", err)
	}
}
