// Package security enforces multi-user POSIX token authentication, DNS rebinding mitigation,
// and CSRF origin defense for UndineD.
package security

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"undined-go/internal/constants"
)

// SecurityManager coordinates authentication and request validation.
type SecurityManager struct {
	Host           string
	Port           int
	DataDir        string
	TokenPath      string
	Token          string
	AllowedHosts   map[string]struct{}
	AllowedOrigins map[string]struct{}
}

// NewSecurityManager initializes the security context, creating or loading the 0600 auth token.
func NewSecurityManager(host string, port int, dataDir string, forceRenew ...bool) (*SecurityManager, error) {
	if host == "" {
		host = constants.DefaultHost
	}
	if port <= 0 {
		port = constants.DefaultPort
	}
	if dataDir == "" {
		dataDir = constants.DefaultDataDir
	}

	sm := &SecurityManager{
		Host:           host,
		Port:           port,
		DataDir:        dataDir,
		TokenPath:      filepath.Join(dataDir, constants.AuthTokenFileName),
		AllowedHosts:   make(map[string]struct{}),
		AllowedOrigins: make(map[string]struct{}),
	}

	// Register allowed Host headers
	portStr := strconv.Itoa(port)
	hosts := []string{
		net.JoinHostPort(host, portStr),
		net.JoinHostPort("localhost", portStr),
		net.JoinHostPort("127.0.0.1", portStr),
		host,
		"localhost",
		"127.0.0.1",
	}
	for _, h := range hosts {
		sm.AllowedHosts[strings.ToLower(h)] = struct{}{}
	}

	// Register allowed Origins (strict scheme + host + port exact match)
	origins := []string{
		fmt.Sprintf("http://%s", net.JoinHostPort(host, portStr)),
		fmt.Sprintf("http://%s", net.JoinHostPort("localhost", portStr)),
		fmt.Sprintf("http://%s", net.JoinHostPort("127.0.0.1", portStr)),
	}
	if port == 80 {
		h := host
		if strings.Contains(h, ":") {
			h = "[" + h + "]"
		}
		origins = append(origins,
			fmt.Sprintf("http://%s", h),
			"http://localhost",
			"http://127.0.0.1",
		)
	}
	for _, o := range origins {
		sm.AllowedOrigins[strings.ToLower(strings.TrimRight(o, "/"))] = struct{}{}
	}

	renew := len(forceRenew) > 0 && forceRenew[0]
	var token string
	var err error
	if renew {
		token, err = sm.RenewToken()
	} else {
		token, err = sm.initToken()
	}
	if err != nil {
		return nil, fmt.Errorf("failed to initialize auth token: %w", err)
	}
	sm.Token = token

	return sm, nil
}

// RenewToken generates a fresh 256-bit cryptographically secure token,
// writes it atomically to a 0600 temp file and renames it into sm.TokenPath,
// refusing to overwrite symlinks, and updates sm.Token.
func (sm *SecurityManager) RenewToken() (string, error) {
	if err := os.MkdirAll(sm.DataDir, 0700); err != nil {
		return "", fmt.Errorf("failed to create data dir %s: %w", sm.DataDir, err)
	}

	if info, err := os.Lstat(sm.TokenPath); err == nil {
		if info.Mode()&os.ModeSymlink != 0 {
			return "", fmt.Errorf("refusing to overwrite symlink at %s", sm.TokenPath)
		}
		if !info.Mode().IsRegular() {
			return "", fmt.Errorf("auth token path %s is not a regular file", sm.TokenPath)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", fmt.Errorf("failed to stat token path %s: %w", sm.TokenPath, err)
	}

	// Generate 32 cryptographically secure random bytes (64 hex characters)
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("failed to generate random token: %w", err)
	}
	token := hex.EncodeToString(b)

	// Write atomically via a private 0600 temp file and rename so looser
	// permissions on a pre-existing file are never preserved.
	tmpFile, err := os.CreateTemp(sm.DataDir, ".auth_token.*")
	if err != nil {
		return "", fmt.Errorf("failed to create temp token file: %w", err)
	}
	tmpPath := tmpFile.Name()
	closed := false
	cleanup := true
	defer func() {
		if !closed {
			_ = tmpFile.Close() // best-effort close on error path
		}
		if cleanup {
			_ = os.Remove(tmpPath) // best-effort temp file cleanup on error path
		}
	}()

	if err := tmpFile.Chmod(0600); err != nil {
		return "", fmt.Errorf("failed to set 0600 mode on temp token file: %w", err)
	}
	if _, err := tmpFile.WriteString(token); err != nil {
		return "", fmt.Errorf("failed to write token: %w", err)
	}
	if err := tmpFile.Sync(); err != nil {
		return "", fmt.Errorf("failed to sync temp token file: %w", err)
	}
	closed = true
	if err := tmpFile.Close(); err != nil {
		return "", fmt.Errorf("failed to close temp token file: %w", err)
	}
	if err := os.Rename(tmpPath, sm.TokenPath); err != nil {
		return "", fmt.Errorf("failed to atomically replace token file: %w", err)
	}
	cleanup = false

	sm.Token = token
	return token, nil
}

// initToken validates and loads an existing 0600 token file, or generates a fresh one.
// Symlinks are rejected; existing files with group/world permissions are rotated to 0600.
func (sm *SecurityManager) initToken() (string, error) {
	if err := os.MkdirAll(sm.DataDir, 0700); err != nil {
		return "", fmt.Errorf("failed to create data dir %s: %w", sm.DataDir, err)
	}

	info, err := os.Lstat(sm.TokenPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return sm.RenewToken()
		}
		return "", fmt.Errorf("failed to stat token file %s: %w", sm.TokenPath, err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return "", fmt.Errorf("refusing to load auth token from symlink %s", sm.TokenPath)
	}
	if !info.Mode().IsRegular() {
		return "", fmt.Errorf("auth token path %s is not a regular file", sm.TokenPath)
	}
	if info.Mode().Perm()&0077 != 0 {
		// Token file was readable/writable by group or others; rotate atomically to 0600.
		return sm.RenewToken()
	}

	data, err := os.ReadFile(sm.TokenPath)
	if err != nil {
		return "", fmt.Errorf("failed to read token file %s: %w", sm.TokenPath, err)
	}
	existing := strings.TrimSpace(string(data))
	if len(existing) == 64 {
		if _, decErr := hex.DecodeString(existing); decErr == nil {
			return existing, nil
		}
	}

	return sm.RenewToken()
}

// IsTokenValid checks whether the provided token matches sm.Token using constant-time comparison.
func (sm *SecurityManager) IsTokenValid(provided string) bool {
	provided = strings.TrimSpace(provided)
	if provided == "" || sm.Token == "" {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(provided), []byte(sm.Token)) == 1
}

// TokenFingerprint returns the hex-encoded SHA-256 hash of the auth token.
func (sm *SecurityManager) TokenFingerprint() string {
	h := sha256.Sum256([]byte(sm.Token))
	return hex.EncodeToString(h[:])
}

// GetTokenFingerprint is an alias for TokenFingerprint retained for backward compatibility.
func (sm *SecurityManager) GetTokenFingerprint() string {
	return sm.TokenFingerprint()
}

// ValidateHost ensures the incoming Host header matches allowed interfaces (DNS rebinding defense).
func (sm *SecurityManager) ValidateHost(hostHeader string) bool {
	hostHeader = strings.ToLower(strings.TrimSpace(hostHeader))
	if hostHeader == "" {
		return false
	}
	if _, ok := sm.AllowedHosts[hostHeader]; ok {
		return true
	}
	if h, _, err := net.SplitHostPort(hostHeader); err == nil {
		if _, ok := sm.AllowedHosts[h]; ok {
			return true
		}
	}
	return false
}

// ValidateOrigin ensures state-modifying requests originate from exact allowed origins (CSRF defense).
func (sm *SecurityManager) ValidateOrigin(method, originHeader, refererHeader string) bool {
	method = strings.ToUpper(method)
	// Read-only methods don't mutate state
	if method == http.MethodGet || method == http.MethodHead || method == http.MethodOptions {
		return true
	}

	if originHeader != "" {
		originHeader = strings.ToLower(strings.TrimRight(strings.TrimSpace(originHeader), "/"))
		_, ok := sm.AllowedOrigins[originHeader]
		return ok
	}

	if refererHeader != "" {
		u, err := url.Parse(refererHeader)
		if err != nil || u.Scheme == "" || u.Host == "" {
			return false
		}
		refOrigin := strings.ToLower(fmt.Sprintf("%s://%s", u.Scheme, u.Host))
		_, ok := sm.AllowedOrigins[refOrigin]
		return ok
	}

	// If neither Origin nor Referer is provided (e.g. CLI curl or MCP client), allow mutation if Bearer token authenticated
	return true
}

// ExtractToken extracts the authentication token from the Bearer header or session cookie.
// URL query parameter authentication (?token=) is intentionally not accepted for API routes.
func (sm *SecurityManager) ExtractToken(r *http.Request) string {
	// 1. Authorization: Bearer <token>
	authHdr := r.Header.Get("Authorization")
	if strings.HasPrefix(strings.ToLower(authHdr), "bearer ") {
		return strings.TrimSpace(authHdr[7:])
	}

	// 2. Cookie undine_session
	if cookie, err := r.Cookie(constants.SessionCookieName); err == nil && cookie.Value != "" {
		return strings.TrimSpace(cookie.Value)
	}

	return ""
}
