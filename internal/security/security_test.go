package security

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"undined-go/internal/constants"
)

func TestSecurityManagerInitAndPermissions(t *testing.T) {
	tmpDir := t.TempDir()
	sm, err := NewSecurityManager("127.0.0.1", 8080, tmpDir)
	if err != nil {
		t.Fatalf("failed to create SecurityManager: %v", err)
	}

	if len(sm.Token) != 64 {
		t.Fatalf("expected 64 character hex token, got %d", len(sm.Token))
	}

	// Verify POSIX 0600 permissions
	fi, err := os.Stat(filepath.Join(tmpDir, constants.AuthTokenFileName))
	if err != nil {
		t.Fatalf("failed to stat auth token file: %v", err)
	}

	perm := fi.Mode().Perm()
	if perm != 0600 {
		t.Fatalf("expected permissions 0600, got %#o", perm)
	}

	// Verify existing token is reloaded cleanly
	sm2, err := NewSecurityManager("127.0.0.1", 8080, tmpDir)
	if err != nil {
		t.Fatalf("failed to reload SecurityManager: %v", err)
	}
	if sm2.Token != sm.Token {
		t.Fatalf("expected reloaded token %s, got %s", sm.Token, sm2.Token)
	}

	// Verify RenewToken generates a different valid token
	oldToken := sm.Token
	newToken, err := sm.RenewToken()
	if err != nil {
		t.Fatalf("RenewToken failed: %v", err)
	}
	if len(newToken) != 64 || newToken == oldToken {
		t.Fatalf("expected new 64-char token different from old token, got %s", newToken)
	}
	if sm.Token != newToken {
		t.Fatalf("expected sm.Token to update to %s, got %s", newToken, sm.Token)
	}

	// Verify NewSecurityManager with forceRenew=true forces regeneration
	sm3, err := NewSecurityManager("127.0.0.1", 8080, tmpDir, true)
	if err != nil {
		t.Fatalf("failed to create SecurityManager with forceRenew: %v", err)
	}
	if sm3.Token == newToken {
		t.Fatalf("expected sm3.Token to be regenerated, got same token %s", sm3.Token)
	}
}

func TestTokenFilePermissionEnforcementAndSymlinkRejection(t *testing.T) {
	// 1. Loose 0644 token file is rotated atomically to 0600 on init
	tmpDir := t.TempDir()
	tokenPath := filepath.Join(tmpDir, constants.AuthTokenFileName)
	leakedToken := "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	if err := os.WriteFile(tokenPath, []byte(leakedToken), 0644); err != nil {
		t.Fatalf("os.WriteFile failed: %v", err)
	}
	if err := os.Chmod(tokenPath, 0644); err != nil {
		t.Fatalf("os.Chmod failed: %v", err)
	}

	sm, err := NewSecurityManager("127.0.0.1", 8080, tmpDir)
	if err != nil {
		t.Fatalf("NewSecurityManager failed on loose token file: %v", err)
	}
	if sm.Token == leakedToken {
		t.Errorf("initToken reused leaked 0644 token, want rotated token")
	}
	fi, err := os.Stat(tokenPath)
	if err != nil {
		t.Fatalf("os.Stat failed: %v", err)
	}
	if got := fi.Mode().Perm(); got != 0600 {
		t.Errorf("token file mode after init = %#o, want 0600", got)
	}

	// 2. RenewToken replaces a loosely-permissioned file atomically with 0600
	if err := os.Chmod(tokenPath, 0644); err != nil {
		t.Fatalf("os.Chmod failed: %v", err)
	}
	if _, err := sm.RenewToken(); err != nil {
		t.Fatalf("RenewToken failed: %v", err)
	}
	fi, err = os.Stat(tokenPath)
	if err != nil {
		t.Fatalf("os.Stat failed: %v", err)
	}
	if got := fi.Mode().Perm(); got != 0600 {
		t.Errorf("token file mode after RenewToken = %#o, want 0600", got)
	}

	// 3. Symlink at auth_token path must be rejected on both init and renew
	symDir := t.TempDir()
	target := filepath.Join(symDir, "target_file")
	if err := os.WriteFile(target, []byte(leakedToken), 0600); err != nil {
		t.Fatalf("os.WriteFile(target) failed: %v", err)
	}
	symTokenPath := filepath.Join(symDir, constants.AuthTokenFileName)
	if err := os.Symlink(target, symTokenPath); err != nil {
		t.Fatalf("os.Symlink failed: %v", err)
	}

	if _, err := NewSecurityManager("127.0.0.1", 8080, symDir); err == nil {
		t.Errorf("NewSecurityManager on symlink auth_token = nil error, want rejection error")
	}
	if _, err := NewSecurityManager("127.0.0.1", 8080, symDir, true); err == nil {
		t.Errorf("NewSecurityManager(forceRenew) on symlink auth_token = nil error, want rejection error")
	}
}

func TestHostValidation(t *testing.T) {
	tmpDir := t.TempDir()
	sm, _ := NewSecurityManager("127.0.0.1", 8080, tmpDir)

	validHosts := []string{
		"127.0.0.1:8080",
		"localhost:8080",
		"127.0.0.1",
		"localhost",
	}
	for _, h := range validHosts {
		if !sm.ValidateHost(h) {
			t.Errorf("expected host %s to be valid", h)
		}
	}

	invalidHosts := []string{
		"evil.com",
		"attacker.org:8080",
		"192.168.1.1:8080",
		"test",
		"testserver",
		"",
	}
	for _, h := range invalidHosts {
		if sm.ValidateHost(h) {
			t.Errorf("expected host %s to be invalid", h)
		}
	}
}

func TestOriginValidation(t *testing.T) {
	tmpDir := t.TempDir()
	sm, _ := NewSecurityManager("127.0.0.1", 8080, tmpDir)

	// GET is always allowed
	if !sm.ValidateOrigin("GET", "http://evil.com", "") {
		t.Errorf("GET should always pass origin validation")
	}

	// POST from trusted origin
	if !sm.ValidateOrigin("POST", "http://127.0.0.1:8080", "") {
		t.Errorf("POST from http://127.0.0.1:8080 should be allowed")
	}
	if !sm.ValidateOrigin("POST", "http://localhost:8080/", "") {
		t.Errorf("POST from http://localhost:8080/ should be allowed")
	}

	// POST from untrusted origin
	if sm.ValidateOrigin("POST", "http://malicious-website.com", "") {
		t.Errorf("POST from evil website should be rejected")
	}

	// Cross-port localhost / 127.0.0.1 CSRF attempts must be rejected
	crossPortOrigins := []string{
		"http://127.0.0.1:3000",
		"http://localhost:9999",
		"https://127.0.0.1:8080",
		"http://127.0.0.1",
	}
	for _, orig := range crossPortOrigins {
		if sm.ValidateOrigin(http.MethodPost, orig, "") {
			t.Errorf("ValidateOrigin(%q, %q, %q) = true, want false", http.MethodPost, orig, "")
		}
		if sm.ValidateOrigin(http.MethodPost, "", orig+"/attack.html") {
			t.Errorf("ValidateOrigin(%q, %q, %q) = true, want false", http.MethodPost, "", orig+"/attack.html")
		}
	}

	// Valid exact-match Referer when Origin is absent
	if !sm.ValidateOrigin(http.MethodPost, "", "http://127.0.0.1:8080/") {
		t.Errorf("ValidateOrigin(%q, %q, %q) = false, want true", http.MethodPost, "", "http://127.0.0.1:8080/")
	}

	// Default port 80 allows omitted port in Origin/Referer
	sm80, err := NewSecurityManager("127.0.0.1", 80, t.TempDir())
	if err != nil {
		t.Fatalf("NewSecurityManager(port 80) failed: %v", err)
	}
	if !sm80.ValidateOrigin(http.MethodPost, "http://127.0.0.1", "") {
		t.Errorf("ValidateOrigin(port 80, %q) = false, want true", "http://127.0.0.1")
	}
	if sm80.ValidateOrigin(http.MethodPost, "http://127.0.0.1:8080", "") {
		t.Errorf("ValidateOrigin(port 80, %q) = true, want false", "http://127.0.0.1:8080")
	}
}

func TestExtractAndValidateToken(t *testing.T) {
	tmpDir := t.TempDir()
	sm, _ := NewSecurityManager("127.0.0.1", 8080, tmpDir)

	// 1. Authorization: Bearer <token>
	req1 := httptest.NewRequest("GET", "/api/messages", nil)
	req1.Header.Set("Authorization", "Bearer "+sm.Token)
	token1 := sm.ExtractToken(req1)
	if !sm.IsTokenValid(token1) {
		t.Errorf("expected Bearer token to be valid")
	}

	// 2. Cookie undine_session
	req2 := httptest.NewRequest("GET", "/api/messages", nil)
	req2.AddCookie(&http.Cookie{Name: constants.SessionCookieName, Value: sm.Token})
	token2 := sm.ExtractToken(req2)
	if !sm.IsTokenValid(token2) {
		t.Errorf("expected Cookie token to be valid")
	}

	// 3. Query parameter ?token= must NOT be extracted by ExtractToken (restricted to / bootstrap)
	req3 := httptest.NewRequest(http.MethodGet, "/api/messages?token="+sm.Token, nil)
	if got := sm.ExtractToken(req3); got != "" {
		t.Errorf("ExtractToken(%q) = %q, want empty string", req3.URL.String(), got)
	}

	// 4. Invalid token
	if sm.IsTokenValid("bad-token") {
		t.Errorf("bad token should be invalid")
	}
}
