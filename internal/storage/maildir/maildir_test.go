package maildir

import (
	"os"
	"strings"
	"testing"

	"undined-go/internal/constants"
)

func TestMaildirOperations(t *testing.T) {
	tmpDir := t.TempDir()
	store := NewMaildirStore(tmpDir)

	content := []byte("From: developer@kernel.org\nSubject: Test patch\n\nHello Linux Kernel\n")

	// 1. Add message with FlagSeen
	path, err := store.AddMessage("TODO", content, constants.FlagSeen)
	if err != nil {
		t.Fatalf("failed to add message: %v", err)
	}

	if !strings.Contains(path, "/TODO/cur/") {
		t.Errorf("expected file in /TODO/cur/, got %s", path)
	}
	if !IsRead(path) {
		t.Errorf("expected message to be read (FlagSeen)")
	}

	// 2. Read message
	readBack, err := store.ReadMessage(path)
	if err != nil {
		t.Fatalf("failed to read message: %v", err)
	}
	if string(readBack) != string(content) {
		t.Fatalf("content mismatch")
	}

	// 3. Add FlagReplied
	newPath, err := store.AddFlag(path, constants.FlagReplied)
	if err != nil {
		t.Fatalf("failed to add flag: %v", err)
	}
	if !IsReplied(newPath) {
		t.Errorf("expected message to be replied")
	}
	if !IsRead(newPath) {
		t.Errorf("expected message to still be read")
	}

	// 4. Move message to b4-imports
	movedPath, err := store.MoveMessage(newPath, "b4-imports")
	if err != nil {
		t.Fatalf("failed to move message: %v", err)
	}
	if !strings.Contains(movedPath, "/b4-imports/cur/") {
		t.Errorf("expected file in /b4-imports/cur/, got %s", movedPath)
	}

	// Verify old path is gone
	if _, err := os.Stat(newPath); !os.IsNotExist(err) {
		t.Errorf("expected old path %s to be deleted", newPath)
	}

	// 5. Delete message
	if err := store.DeleteMessage(movedPath); err != nil {
		t.Fatalf("failed to delete message: %v", err)
	}
	if _, err := os.Stat(movedPath); !os.IsNotExist(err) {
		t.Errorf("expected deleted path %s to be gone", movedPath)
	}
}

func TestNormalizeFlags(t *testing.T) {
	flags := NormalizeFlags("SRFDS")
	if flags != "DFRS" {
		t.Errorf("expected DFRS, got %s", flags)
	}
}
