// Package process provides OS-level process manipulation utilities.
package process

import (
	"runtime"
	"syscall"
	"unsafe"
)

// SetProcessName sets the Linux process comm name via prctl PR_SET_NAME.
func SetProcessName(name string) bool {
	if runtime.GOOS == "linux" {
		b := make([]byte, 16)
		copy(b, name)
		_, _, errno := syscall.Syscall(syscall.SYS_PRCTL, 15, uintptr(unsafe.Pointer(&b[0])), 0)
		return errno == 0
	}
	return false
}
