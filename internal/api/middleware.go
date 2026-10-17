package api

import (
	"encoding/json"
	"net/http"
	"strings"
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
