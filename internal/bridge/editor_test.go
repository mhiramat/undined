package bridge

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"undined-go/internal/config"
	"undined-go/internal/constants"
	"undined-go/internal/storage/database"
)

func TestEditorLauncherPrepareDraftContent(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "undined-editor-test-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	dbPath := filepath.Join(tmpDir, "test.db")
	db, err := database.NewDatabase(dbPath)
	if err != nil {
		t.Fatalf("Failed to init database: %v", err)
	}
	defer db.Close()

	cfg := config.NewDefaultConfig()
	cfg.SMTP.FromName = "Kernel Developer"
	cfg.SMTP.FromEmail = "dev@example.com"

	launcher := NewLauncher(cfg, db)

	// Test custom draft content
	customTo := "dev@vger.kernel.org"
	customSubj := "Re: [PATCH] tracing: Fix probe"
	customBody := "Looks good to me.\n\nSigned-off-by: Test"

	req := OpenEditorRequest{
		CustomTo:      &customTo,
		CustomSubject: &customSubj,
		CustomBody:    &customBody,
	}

	content, err := launcher.PrepareDraftContent(req)
	if err != nil {
		t.Fatalf("PrepareDraftContent failed: %v", err)
	}

	if !strings.Contains(content, "From: Kernel Developer <dev@example.com>") {
		t.Errorf("Missing From header in content: %s", content)
	}
	if !strings.Contains(content, "To: dev@vger.kernel.org") {
		t.Errorf("Missing To header in content: %s", content)
	}
	if !strings.Contains(content, "Subject: Re: [PATCH] tracing: Fix probe") {
		t.Errorf("Missing Subject header in content: %s", content)
	}
	if !strings.Contains(content, "X-Mailer: "+constants.DefaultXMailer) {
		t.Errorf("Missing X-Mailer header in content: %s", content)
	}
	if !strings.Contains(content, "Looks good to me.") {
		t.Errorf("Missing body in content: %s", content)
	}
}

func TestEditorLauncherLaunch(t *testing.T) {
	cfg := config.NewDefaultConfig()
	// Use echo command to simulate editor launch without blocking or opening GUI
	cfg.General.EditorCmd = "true {file}"

	launcher := NewLauncher(cfg, nil)

	customBody := "Quick note"
	req := OpenEditorRequest{
		CustomBody: &customBody,
	}

	res, err := launcher.Launch(req)
	if err != nil {
		t.Fatalf("Launch failed: %v", err)
	}
	defer os.Remove(res.TempFile)

	if !res.Success {
		t.Errorf("Expected success, got false")
	}

	if !strings.HasPrefix(res.Command, "true ") {
		t.Errorf("Unexpected command: %s", res.Command)
	}

	if _, err := os.Stat(res.TempFile); os.IsNotExist(err) {
		t.Errorf("Temp file was not created: %s", res.TempFile)
	}
}
