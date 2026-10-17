package process

import (
	"fmt"
	"os"
	"runtime"
	"strings"
	"testing"
)

func TestSetProcessName(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("prctl is Linux specific")
	}

	ok := SetProcessName("undined-test")
	if !ok {
		t.Fatalf("SetProcessName failed")
	}

	comm, err := os.ReadFile(fmt.Sprintf("/proc/%d/comm", os.Getpid()))
	if err != nil {
		t.Fatalf("failed to read /proc/self/comm: %v", err)
	}

	if !strings.HasPrefix(string(comm), "undined-test") {
		t.Errorf("expected comm to start with undined-test, got %q", string(comm))
	}
}
