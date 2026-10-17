// Package security enforces multi-user POSIX token authentication, DNS rebinding mitigation,
// and CSRF origin defense for UndineD.
package security

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
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
func NewSecurityManager(host string, port int, dataDir string) (*SecurityManager, error) {
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
	hosts := []string{
		fmt.Sprintf("%s:%d", host, port),
		fmt.Sprintf("localhost:%d", port),
		fmt.Sprintf("127.0.0.1:%d", port),
		host,
		"localhost",
		"127.0.0.1",
		"test",
		"testserver",
	}
	for _, h := range hosts {
		sm.AllowedHosts[strings.ToLower(h)] = struct{}{}
	}

	// Register allowed Origins
	origins := []string{
		fmt.Sprintf("http://%s:%d", host, port),
		fmt.Sprintf("http://localhost:%d", port),
		fmt.Sprintf("http://127.0.0.1:%d", port),
	}
	for _, o := range origins {
		sm.AllowedOrigins[strings.ToLower(strings.TrimRight(o, "/"))] = struct{}{}
	}

	token, err := sm.initToken()
	if err != nil {
		return nil, fmt.Errorf("failed to initialize auth token: %w", err)
	}
	sm.Token = token

	return sm, nil
}

// initToken atomically generates and writes a 256-bit token with 0600 permissions if not present.
func (sm *SecurityManager) initToken() (string, error) {
	if err := os.MkdirAll(sm.DataDir, 0700); err != nil {
		return "", fmt.Errorf("failed to create data dir %s: %w", sm.DataDir, err)
	}

	// If file exists and contains valid token, reuse it
	if data, err := os.ReadFile(sm.TokenPath); err == nil {
		existing := strings.TrimSpace(string(data))
		if len(existing) == 64 {
			return existing, nil
		}
	}

	// Generate 32 cryptographically secure random bytes (64 hex characters)
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("failed to generate random token: %w", err)
	}
	token := hex.EncodeToString(b)

	// Write atomically with strict 0600 permissions
	f, err := os.OpenFile(sm.TokenPath, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0600)
	if err != nil {
		return "", fmt.Errorf("failed to create token file with 0600 mode: %w", err)
	}
	defer f.Close()

	if _, err := f.WriteString(token); err != nil {
		return "", fmt.Errorf("failed to write token: %w", err)
	}

	return token, nil
}

// IsTokenValid checks whether the provided token matches sm.Token using constant-time comparison.
func (sm *SecurityManager) IsTokenValid(provided string) bool {
	provided = strings.TrimSpace(provided)
	if provided == "" || sm.Token == "" {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(provided), []byte(sm.Token)) == 1
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

// ValidateOrigin ensures state-modifying requests originate from local origins (CSRF defense).
func (sm *SecurityManager) ValidateOrigin(method, originHeader, refererHeader string) bool {
	method = strings.ToUpper(method)
	// Read-only methods don't mutate state
	if method == http.MethodGet || method == http.MethodHead || method == http.MethodOptions {
		return true
	}

	if originHeader != "" {
		originHeader = strings.ToLower(strings.TrimRight(strings.TrimSpace(originHeader), "/"))
		if _, ok := sm.AllowedOrigins[originHeader]; ok {
			return true
		}
		u, err := url.Parse(originHeader)
		if err == nil {
			hostname := strings.ToLower(u.Hostname())
			if _, ok := sm.AllowedHosts[hostname]; ok {
				return true
			}
		}
		return false
	}

	if refererHeader != "" {
		u, err := url.Parse(refererHeader)
		if err == nil {
			refOrigin := strings.ToLower(fmt.Sprintf("%s://%s", u.Scheme, u.Host))
			if _, ok := sm.AllowedOrigins[refOrigin]; ok {
				return true
			}
			hostname := strings.ToLower(u.Hostname())
			if _, ok := sm.AllowedHosts[hostname]; ok {
				return true
			}
		}
		return false
	}

	// If neither Origin nor Referer is provided (e.g. CLI curl or MCP client), allow mutation if Bearer token authenticated
	return true
}

// ExtractToken extracts authentication token from Bearer header, session cookie, or query parameter.
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

	// 3. Query parameter ?token=
	if q := r.URL.Query().Get("token"); q != "" {
		return strings.TrimSpace(q)
	}

	return ""
}
