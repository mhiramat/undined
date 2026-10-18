package api

import (
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/go-chi/chi/v5/middleware"

	"undined-go/internal/config"
)

// SecurityMiddleware enforces Host validation, Origin CSRF checks, and Token authentication.
func (s *Server) SecurityMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.Path

		// 1. Host Validation (defense against DNS rebinding)
		host := r.Host
		if !s.Security.ValidateHost(host) {
			http.Error(w, "Access denied: Host header validation failed.", http.StatusForbidden)
			return
		}

		// Exempt static and auth routes from token and origin checks
		isExempt := path == "/" ||
			path == "/login" ||
			strings.HasPrefix(path, "/src/") ||
			strings.HasPrefix(path, "/favicon")

		// 2. Origin Validation on state-changing requests (CSRF protection)
		if r.Method == http.MethodPost || r.Method == http.MethodPut ||
			r.Method == http.MethodDelete || r.Method == http.MethodPatch {
			origin := r.Header.Get("Origin")
			if origin != "" && !s.Security.ValidateOrigin(r.Method, origin, r.Header.Get("Referer")) {
				http.Error(w, "Access denied: Cross-origin requests are blocked.", http.StatusForbidden)
				return
			}
		}

		if isExempt {
			next.ServeHTTP(w, r)
			return
		}

		// 3. Token Authentication
		token := s.Security.ExtractToken(r)

		if !s.Security.IsTokenValid(token) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusUnauthorized)
			_ = json.NewEncoder(w).Encode(map[string]string{
				"detail": "Invalid or missing authentication token",
			})
			return
		}

		next.ServeHTTP(w, r)
	})
}

// CacheControlMiddleware prevents caching for UI HTML, login, and static assets.
func (s *Server) CacheControlMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := r.URL.Path
		if p == "/" || p == "/login" || strings.HasPrefix(p, "/src/") || strings.HasPrefix(p, "/favicon") {
			w.Header().Set("Cache-Control", "no-cache, no-store, must-revalidate")
			w.Header().Set("Pragma", "no-cache")
			w.Header().Set("Expires", "0")
		}
		next.ServeHTTP(w, r)
	})
}

// SetLogWriter sets a custom writer for REST API access logs (used for testing or custom log sinks).
func (s *Server) SetLogWriter(w io.Writer) {
	s.logMu.Lock()
	defer s.logMu.Unlock()
	s.logWriter = w
}

func (s *Server) writeAccessLog(level, message string) {
	s.logMu.Lock()
	defer s.logMu.Unlock()

	formatted := fmt.Sprintf("%s [%s] [undine.api] %s\n",
		time.Now().Format("2006-01-02 15:04:05"), level, message)

	if s.logWriter != nil {
		_, _ = s.logWriter.Write([]byte(formatted))
	} else {
		_, _ = os.Stderr.WriteString(formatted)
	}

	// Always append to configured log file if available
	logFile := ""
	if s.Config != nil && s.Config.General.LogFile != "" {
		logFile = config.ExpandPath(s.Config.General.LogFile)
	} else {
		home, _ := os.UserHomeDir()
		if home != "" {
			logFile = filepath.Join(home, ".cache", "undined", "undined.log")
		}
	}

	if logFile != "" {
		_ = os.MkdirAll(filepath.Dir(logFile), 0700)
		f, err := os.OpenFile(logFile, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0600)
		if err == nil {
			defer f.Close()
			_, _ = f.WriteString(formatted)
		}
	}
}

// AccessLogMiddleware logs each REST API access to the logfile.
func (s *Server) AccessLogMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.Path

		// Filter for REST API and auth accesses (e.g. /api/... and /login)
		isAPI := strings.HasPrefix(path, "/api") || path == "/login"
		if !isAPI {
			next.ServeHTTP(w, r)
			return
		}

		start := time.Now()
		ww := middleware.NewWrapResponseWriter(w, r.ProtoMajor)

		next.ServeHTTP(ww, r)

		duration := time.Since(start)
		status := ww.Status()
		if status == 0 {
			status = http.StatusOK
		}

		level := "INFO"
		if status >= 500 {
			level = "ERROR"
		} else if status >= 400 {
			level = "WARN"
		}

		clientIP := r.RemoteAddr
		if forwarded := r.Header.Get("X-Forwarded-For"); forwarded != "" {
			parts := strings.Split(forwarded, ",")
			clientIP = strings.TrimSpace(parts[0])
		} else if host, _, err := net.SplitHostPort(clientIP); err == nil {
			clientIP = host
		}

		uri := r.URL.RequestURI()
		if uri == "" {
			uri = r.URL.String()
			if uri == "" {
				uri = path
			}
		}

		// Redact token query parameter to prevent logging credentials
		if r.URL.Query().Has("token") {
			u := *r.URL
			q := u.Query()
			q.Set("token", "REDACTED")
			u.RawQuery = q.Encode()
			uri = u.RequestURI()
		}

		statusText := http.StatusText(status)
		if statusText == "" {
			statusText = fmt.Sprintf("Status %d", status)
		}

		msg := fmt.Sprintf("REST API access: %s %s - %d %s (from %s in %v)",
			r.Method, uri, status, statusText, clientIP, duration.Round(time.Microsecond))

		s.writeAccessLog(level, msg)
	})
}

