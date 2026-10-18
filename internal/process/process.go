// Package process provides OS-level process manipulation utilities.
package process

import (
	"os"
	"runtime"
	"syscall"
	"unsafe"
)

// SetProcessName sets the Linux process comm name via /proc/self/comm and prctl PR_SET_NAME.
func SetProcessName(name string) bool {
	if runtime.GOOS == "linux" {
		truncated := name
		if len(truncated) > 15 {
			truncated = truncated[:15]
		}
		_ = os.WriteFile("/proc/self/comm", []byte(truncated), 0644)

		b := make([]byte, 16)
		copy(b, truncated)
		_, _, errno := syscall.Syscall(syscall.SYS_PRCTL, 15, uintptr(unsafe.Pointer(&b[0])), 0)
		return errno == 0
	}
	return false
}
