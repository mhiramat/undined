package e2e

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"undined-go/internal/api"
	"undined-go/internal/bridge"
	"undined-go/internal/config"
	"undined-go/internal/imap"
	"undined-go/internal/lkml/b4"
	"undined-go/internal/lkml/review"
	"undined-go/internal/patchwork"
	"undined-go/internal/security"
	"undined-go/internal/smtp"
	"undined-go/internal/storage/database"
	"undined-go/internal/storage/maildir"
	"undined-go/test/e2e/mocks"
)

// Sample Linux kernel patch emails adhering to privacy & anonymization rules
const sampleKernelPatch1 = `From: Kernel Developer <dev@example.com>
To: linux-trace@vger.kernel.org
Subject: [PATCH v2 1/2] trace/kprobes: Add support for custom event probes
Message-ID: <patch-e2e-1@kernel.org>
Date: Sat, 24 Oct 2026 10:00:00 +0000
Content-Type: text/plain; charset=utf-8
Content-Transfer-Encoding: 8bit

This patch introduces custom event probes in the kernel tracing subsystem.

Signed-off-by: Kernel Developer <dev@example.com>
---
 kernel/trace/trace_kprobe.c | 5 +++++
 1 file changed, 5 insertions(+)

--- a/kernel/trace/trace_kprobe.c
+++ b/kernel/trace/trace_kprobe.c
@@ -100,6 +100,11 @@ int register_kprobe_event(const char *name)
 {
+	/* Custom event probe handler */
+	if (!name)
+		return -EINVAL;
+
 	return 0;
 }
-- 
2.43.0
`

const sampleKernelPatch2 = `From: Kernel Developer <dev@example.com>
To: linux-trace@vger.kernel.org
Subject: [PATCH v2 2/2] selftests/ftrace: Add test case for custom event probes
Message-ID: <patch-e2e-2@kernel.org>
In-Reply-To: <patch-e2e-1@kernel.org>
References: <patch-e2e-1@kernel.org>
Date: Sat, 24 Oct 2026 10:05:00 +0000
Content-Type: text/plain; charset=utf-8
Content-Transfer-Encoding: 8bit

Add a functional self-test verifying custom event probes.

Signed-off-by: Kernel Developer <dev@example.com>
---
 tools/testing/selftests/ftrace/test.d/kprobe.tc | 4 ++++
 1 file changed, 4 insertions(+)

--- a/tools/testing/selftests/ftrace/test.d/kprobe.tc
+++ b/tools/testing/selftests/ftrace/test.d/kprobe.tc
@@ -10,4 +10,8 @@ echo 1 > tracing_on
+echo 'p:testprobe register_kprobe_event' > kprobe_events
+grep -q testprobe kprobe_events
 exit 0
-- 
2.43.0
`

// E2EEnv encapsulates the isolated End-to-End test harness.
type E2EEnv struct {
	T            *testing.T
	WorkspaceDir string
	Config       *config.Config
	ConfigPath   string
	DB           *database.Database
	Maildir      *maildir.MaildirStore
	Security     *security.SecurityManager
	Poller       *imap.Poller
	IMAPServer   *mocks.MockIMAPServer
	SMTPServer   *mocks.MockSMTPServer
	Patchwork    *mocks.MockPatchworkServer
	B4Runner     *mocks.MockB4Runner
	B4Client     *b4.B4Runner
	PWClient     *patchwork.Client
	APIServer    *api.Server
	HTTPServer   *httptest.Server
	HTTPClient   *http.Client
	BaseURL      string
	AuthToken    string
}

// setupE2EEnv initializes all mock upstream servers and boots UndineD in an isolated environment.
func setupE2EEnv(t *testing.T) *E2EEnv {
	t.Helper()
	workspaceDir := t.TempDir()

	// 1. Start mock servers
	imapServer, err := mocks.NewMockIMAPServer()
	if err != nil {
		t.Fatalf("failed to start mock IMAP server: %v", err)
	}

	smtpServer, err := mocks.NewMockSMTPServer()
	if err != nil {
		imapServer.Close()
		t.Fatalf("failed to start mock SMTP server: %v", err)
	}

	patchworkServer := mocks.NewMockPatchworkServer()

	b4Runner, err := mocks.NewMockB4Runner()
	if err != nil {
		imapServer.Close()
		smtpServer.Close()
		patchworkServer.Close()
		t.Fatalf("failed to start mock B4 runner: %v", err)
	}

	// 2. Setup isolated paths
	dbPath := filepath.Join(workspaceDir, "undined.db")
	maildirPath := filepath.Join(workspaceDir, "maildir")
	configPath := filepath.Join(workspaceDir, "config.toml")
	kernelGitDir := filepath.Join(workspaceDir, "linux")
	_ = os.MkdirAll(kernelGitDir, 0755)

	// Initialize mock kernel git repository
	traceDir := filepath.Join(kernelGitDir, "kernel", "trace")
	_ = os.MkdirAll(traceDir, 0755)
	cFile := filepath.Join(traceDir, "trace_kprobe.c")
	_ = os.WriteFile(cFile, []byte("int trace_kprobe_init(void) {\n    return 0;\n}\n"), 0644)
	_ = exec.Command("git", "-C", kernelGitDir, "init", "-b", "master").Run()
	_ = exec.Command("git", "-C", kernelGitDir, "config", "user.name", "Kernel Developer").Run()
	_ = exec.Command("git", "-C", kernelGitDir, "config", "user.email", "dev@example.com").Run()
	_ = exec.Command("git", "-C", kernelGitDir, "add", ".").Run()
	_ = exec.Command("git", "-C", kernelGitDir, "commit", "-m", "Initial commit").Run()

	// 3. Initialize SQLite database & Maildir
	db, err := database.NewDatabase(dbPath)
	if err != nil {
		t.Fatalf("failed to init database: %v", err)
	}

	store := maildir.NewMaildirStore(maildirPath)

	// Ensure default standard folders
	_, _ = db.GetOrCreateFolder("INBOX", true)
	_, _ = db.GetOrCreateFolder("TODO", false)
	_, _ = db.GetOrCreateFolder("Sent", false)
	_, _ = db.GetOrCreateFolder("Drafts", false)
	_ = store.EnsureFolder("INBOX")
	_ = store.EnsureFolder("TODO")
	_ = store.EnsureFolder("Sent")
	_ = store.EnsureFolder("Drafts")

	// 4. Construct configuration
	cfg := config.NewDefaultConfig()
	cfg.General.DataDir = workspaceDir
	cfg.General.MaildirPath = maildirPath
	cfg.General.DBPath = dbPath
	cfg.General.KernelGitDir = kernelGitDir
	cfg.General.Host = "127.0.0.1"

	cfg.IMAP.Host = "127.0.0.1"
	cfg.IMAP.Port = imapServer.Port()
	cfg.IMAP.UseSSL = false
	cfg.IMAP.Username = "dev@example.com"
	cfg.IMAP.Password = "secret"
	cfg.IMAP.SyncFolders = []string{"INBOX"}

	cfg.SMTP.Host = "127.0.0.1"
	cfg.SMTP.Port = smtpServer.Port()
	cfg.SMTP.UseSSL = false
	cfg.SMTP.UseSTARTTLS = false
	cfg.SMTP.FromName = "Kernel Developer"
	cfg.SMTP.FromEmail = "dev@example.com"
	cfg.SMTP.Username = "dev@example.com"

	cfg.Patchwork.Enabled = true
	cfg.Patchwork.URL = patchworkServer.URL
	cfg.Patchwork.APIVersion = "1.4"
	cfg.Patchwork.Username = "dev"
	cfg.Patchwork.Email = "dev@example.com"
	cfg.Patchwork.Token = "mock-patchwork-token"
	cfg.Patchwork.PreferredArchives = []string{"linux-trace"}
	cfg.Patchwork.DelegateUsers = []string{"dev", "maintainer2", "reviewer"}

	cfg.LKML.B4Cmd = b4Runner.ScriptPath
	cfg.LKML.LoreURL = "https://lore.kernel.org/all"

	// 5. Initialize SecurityManager
	sec, err := security.NewSecurityManager("127.0.0.1", 8080, workspaceDir)
	if err != nil {
		t.Fatalf("failed to init security manager: %v", err)
	}

	// 6. Initialize subsystem components
	poller := imap.NewPoller(cfg, store, db)
	smtpClient := smtp.NewClient(cfg, store, db)
	pwClient := patchwork.NewClient(cfg, db, store)
	b4Client := b4.NewB4Runner(cfg, store, db)
	reviewRunner := review.NewRunner(cfg, store, db)
	launcher := bridge.NewLauncher(cfg, db)

	// 7. Initialize and start HTTP API server
	apiServer := api.NewServer(cfg, db, store, sec, poller, smtpClient, b4Client, reviewRunner, pwClient, launcher, "")
	ts := httptest.NewServer(apiServer.Router())

	// Register test server origin for CSRF validation
	sec.AllowedOrigins[ts.URL] = struct{}{}

	client := &http.Client{
		Timeout: 10 * time.Second,
	}

	env := &E2EEnv{
		T:            t,
		WorkspaceDir: workspaceDir,
		Config:       cfg,
		ConfigPath:   configPath,
		DB:           db,
		Maildir:      store,
		Security:     sec,
		Poller:       poller,
		IMAPServer:   imapServer,
		SMTPServer:   smtpServer,
		Patchwork:    patchworkServer,
		B4Runner:     b4Runner,
		B4Client:     b4Client,
		PWClient:     pwClient,
		APIServer:    apiServer,
		HTTPServer:   ts,
		HTTPClient:   client,
		BaseURL:      ts.URL,
		AuthToken:    sec.Token,
	}

	t.Cleanup(func() {
		ts.Close()
		imapServer.Close()
		smtpServer.Close()
		patchworkServer.Close()
		b4Runner.Cleanup()
		db.Close()
	})

	return env
}

// Request performs an authenticated HTTP request against the UndineD API server.
func (e *E2EEnv) Request(method, endpoint string, body string) (*http.Response, error) {
	url := fmt.Sprintf("%s%s", e.BaseURL, endpoint)

	var req *http.Request
	var err error
	if body != "" {
		req, err = http.NewRequest(method, url, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
	} else {
		req, err = http.NewRequest(method, url, nil)
	}
	if err != nil {
		return nil, err
	}

	req.Header.Set("Authorization", "Bearer "+e.AuthToken)
	req.Header.Set("Origin", e.BaseURL)
	return e.HTTPClient.Do(req)
}
