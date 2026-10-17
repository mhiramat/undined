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
}

func TestHostValidation(t *testing.T) {
	tmpDir := t.TempDir()
	sm, _ := NewSecurityManager("127.0.0.1", 8080, tmpDir)

	validHosts := []string{
		"127.0.0.1:8080",
		"localhost:8080",
		"127.0.0.1",
		"localhost",
		"test",
		"testserver",
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

	// 3. Query parameter ?token=
	req3 := httptest.NewRequest("GET", "/?token="+sm.Token, nil)
	token3 := sm.ExtractToken(req3)
	if !sm.IsTokenValid(token3) {
		t.Errorf("expected query parameter token to be valid")
	}

	// 4. Invalid token
	if sm.IsTokenValid("bad-token") {
		t.Errorf("bad token should be invalid")
	}
}
