// Package process provides OS-level process and PID manipulation utilities.
package process

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// Common process control errors.
var (
	ErrAlreadyRunning = errors.New("undined is already running")
	ErrNotRunning     = errors.New("undined is not running")
)

// WritePIDFile writes the given PID to path, creating parent directories with mode 0700 if needed.
func WritePIDFile(path string, pid int) error {
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return fmt.Errorf("failed to create directory for PID file %s: %w", path, err)
	}
	content := fmt.Sprintf("%d\n", pid)
	if err := os.WriteFile(path, []byte(content), 0600); err != nil {
		return fmt.Errorf("failed to write PID file %s: %w", path, err)
	}
	return nil
}

// ReadPIDFile reads and parses the integer PID from path.
func ReadPIDFile(path string) (int, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return 0, err
	}
	str := strings.TrimSpace(string(data))
	pid, err := strconv.Atoi(str)
	if err != nil {
		return 0, fmt.Errorf("invalid PID in file %s: %q", path, str)
	}
	if pid <= 0 {
		return 0, fmt.Errorf("invalid non-positive PID in file %s: %d", path, pid)
	}
	return pid, nil
}

// RemovePIDFile removes the PID file at path if it exists.
func RemovePIDFile(path string) error {
	err := os.Remove(path)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

// IsProcessRunning checks whether a process with the given PID is running.
func IsProcessRunning(pid int) bool {
	if pid <= 0 {
		return false
	}
	// On Unix/Linux, sending signal 0 checks whether the process exists.
	err := syscall.Kill(pid, 0)
	if err != nil {
		if errors.Is(err, syscall.EPERM) {
			// Process exists but belongs to another user
			return true
		}
		return false
	}

	// In Unix, a terminated child process remains in the process table as a zombie ('Z' or 'X')
	// until reaped by waitpid. Check /proc/<pid>/status to ensure it is actually running.
	statusBytes, err := os.ReadFile(fmt.Sprintf("/proc/%d/status", pid))
	if err == nil {
		for _, line := range strings.Split(string(statusBytes), "\n") {
			if strings.HasPrefix(line, "State:") {
				fields := strings.Fields(line)
				if len(fields) >= 2 {
					state := fields[1]
					if state == "Z" || state == "X" {
						return false
					}
				}
				break
			}
		}
	}

	return true
}

// IsUndinedProcess checks if the process with the given PID is an undined instance.
// On Linux, it inspects /proc/<pid>/comm or /proc/<pid>/cmdline.
func IsUndinedProcess(pid int) bool {
	if !IsProcessRunning(pid) {
		return false
	}

	readSuccess := false

	// Try reading /proc/<pid>/comm
	commBytes, err := os.ReadFile(fmt.Sprintf("/proc/%d/comm", pid))
	if err == nil {
		readSuccess = true
		comm := strings.TrimSpace(string(commBytes))
		if strings.Contains(comm, "undined") {
			return true
		}
	}

	// Try reading /proc/<pid>/cmdline
	cmdlineBytes, err := os.ReadFile(fmt.Sprintf("/proc/%d/cmdline", pid))
	if err == nil {
		readSuccess = true
		cmdline := string(cmdlineBytes)
		if strings.Contains(cmdline, "undined") {
			return true
		}
	}

	// If /proc entries were read successfully but neither matched "undined",
	// it's an unrelated process.
	if readSuccess {
		return false
	}

	// If /proc entries could not be read (e.g. non-Linux or container restrictions),
	// fallback to IsProcessRunning.
	return true
}

// CheckRunning reads the PID file and checks if an undined process is currently running.
// Returns (pid, isRunning, error).
// If the PID file does not exist, returns (0, false, nil).
// If the PID file exists but the process is no longer running (stale PID), returns (pid, false, nil).
func CheckRunning(pidPath string) (int, bool, error) {
	pid, err := ReadPIDFile(pidPath)
	if err != nil {
		if os.IsNotExist(err) {
			return 0, false, nil
		}
		return 0, false, err
	}
	if IsUndinedProcess(pid) {
		return pid, true, nil
	}
	return pid, false, nil
}

// TerminateProcess sends SIGTERM to the given PID, polls up to timeout for exit,
// and falls back to SIGKILL if the process does not exit within timeout.
func TerminateProcess(pid int, timeout time.Duration) error {
	if !IsProcessRunning(pid) {
		return ErrNotRunning
	}

	// Send SIGTERM
	if err := syscall.Kill(pid, syscall.SIGTERM); err != nil {
		if errors.Is(err, syscall.ESRCH) {
			return nil
		}
		return fmt.Errorf("failed to send SIGTERM to process %d: %w", pid, err)
	}

	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		time.Sleep(50 * time.Millisecond)
		if !IsProcessRunning(pid) {
			return nil
		}
	}

	// Process did not exit on SIGTERM; fallback to SIGKILL
	if IsProcessRunning(pid) {
		_ = syscall.Kill(pid, syscall.SIGKILL)
		killDeadline := time.Now().Add(2 * time.Second)
		for time.Now().Before(killDeadline) {
			time.Sleep(50 * time.Millisecond)
			if !IsProcessRunning(pid) {
				return nil
			}
		}
		if IsProcessRunning(pid) {
			return fmt.Errorf("process %d did not terminate after SIGKILL", pid)
		}
	}

	return nil
}
