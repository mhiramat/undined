package main

import (
	"os"
	"path/filepath"
	"testing"
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
