// Package api implements the REST API and Web UI serving for UndineD.
package api

import (
	"encoding/json"
	"fmt"
	"html"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"

	"undined-go/internal/bridge"
	"undined-go/internal/config"
	"undined-go/internal/constants"
	"undined-go/internal/imap"
	"undined-go/internal/lkml/b4"
	"undined-go/internal/lkml/review"
	"undined-go/internal/patchwork"
	"undined-go/internal/security"
	"undined-go/internal/smtp"
	"undined-go/internal/storage/database"
	"undined-go/internal/storage/maildir"
)

// Server encapsulates the UndineD HTTP API and web interface.
type Server struct {
	Config       *config.Config
	DB           *database.Database
	Maildir      *maildir.MaildirStore
	Security     *security.SecurityManager
	IMAP         *imap.Poller
	SMTP         *smtp.Client
	B4           *b4.B4Runner
	Review       *review.Runner
	Patchwork    *patchwork.Client
	Launcher     *bridge.Launcher
	FrontendDir  string
	router       *chi.Mux
}

// NewServer initializes the HTTP API Server.
func NewServer(
	cfg *config.Config,
	db *database.Database,
	store *maildir.MaildirStore,
	sec *security.SecurityManager,
	imapPoller *imap.Poller,
	smtpClient *smtp.Client,
	b4Runner *b4.B4Runner,
	reviewRunner *review.Runner,
	pwClient *patchwork.Client,
	launcher *bridge.Launcher,
	frontendDir string,
) *Server {
	s := &Server{
		Config:       cfg,
		DB:           db,
		Maildir:      store,
		Security:     sec,
		IMAP:         imapPoller,
		SMTP:         smtpClient,
		B4:           b4Runner,
		Review:       reviewRunner,
		Patchwork:    pwClient,
		Launcher:     launcher,
		FrontendDir:  frontendDir,
	}
	s.setupRoutes()
	return s
}

// Router returns the initialized chi http.Handler.
func (s *Server) Router() http.Handler {
	return s.router
}

func (s *Server) setupRoutes() {
	r := chi.NewRouter()

	r.Use(middleware.RequestID)
	r.Use(middleware.RealIP)
	r.Use(middleware.Recoverer)
	r.Use(s.SecurityMiddleware)
	r.Use(s.CacheControlMiddleware)

	// Static & Auth Handlers
	r.Get("/", s.handleIndex)
	r.Get("/login", s.handleGetLogin)
	r.Post("/login", s.handlePostLogin)
	r.Get("/favicon.svg", s.handleFaviconSVG)
	r.Get("/favicon.ico", s.handleFaviconICO)

	// Static assets under /src
	if s.FrontendDir != "" {
		srcDir := filepath.Join(s.FrontendDir, "src")
		fs := http.StripPrefix("/src/", http.FileServer(http.Dir(srcDir)))
		r.Get("/src/*", http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			fs.ServeHTTP(w, req)
		}))
	}

	// API Routes
	r.Route("/api", func(api chi.Router) {
		api.Get("/config", s.handleGetConfig)
		api.Get("/tags", s.handleGetTags)

		// Folders
		api.Get("/folders", s.handleListFolders)
		api.Post("/folders/{folderID}/sync-full", s.handleSyncFullFolder)
		api.Get("/folders/{folderID}/sync-status", s.handleGetFolderSyncStatus)
		api.Post("/folders/{folderID}/sync-cancel", s.handleCancelFolderSync)

		// Messages
		api.Get("/messages", s.handleListMessages)
		api.Get("/messages/{id}", s.handleGetMessageDetail)
		api.Get("/messages/{id}/raw-header", s.handleGetMessageRawHeader)
		api.Get("/messages/{id}/mbox-path", s.handleGetMessageMboxPath)
		api.Delete("/messages/{id}", s.handleDeleteMessage)
		api.Post("/messages/{id}/read", s.handleSetMessageRead)
		api.Post("/messages/{id}/replied", s.handleSetMessageReplied)
		api.Post("/messages/{id}/tag", s.handleSetMessageTag)

		// Comments
		api.Get("/messages/{id}/comments", s.handleGetComments)
		api.Post("/messages/{id}/comments", s.handleSaveComment)
		api.Post("/messages/{id}/comments/discard", s.handleDiscardComments)
		api.Delete("/messages/{id}/comments/{commentID}", s.handleDeleteComment)

		// Full text search
		api.Get("/search", s.handleSearch)

		// LKML / b4
		api.Post("/threads/import-b4", s.handleB4Import)
		api.Post("/patch/apply", s.handleB4Apply)
		api.Post("/patch/apply/abort", s.handleB4Abort)

		// AI Review
		api.Post("/review/trigger", s.handleReviewTrigger)
		api.Get("/review/status", s.handleReviewStatus)
		api.Post("/review/cancel", s.handleReviewCancel)

		// Composer & Editor
		api.Post("/composer/preview-quote", s.handlePreviewQuote)
		api.Post("/composer/open-editor", s.handleOpenEditor)
		api.Post("/composer/save-draft", s.handleSaveDraft)
		api.Post("/smtp/send", s.handleSMTPSend)

		// Patchwork
		api.Get("/patchwork/info", s.handlePatchworkInfo)
		api.Get("/patchwork/my-patches", s.handlePatchworkMyPatches)
		api.Post("/patchwork/sync-todo", s.handlePatchworkSyncTodo)
		api.Get("/patchwork/patches/{patchID}", s.handlePatchworkGetPatch)
		api.Get("/patchwork/patches/{patchID}/checks", s.handlePatchworkGetChecks)
		api.Get("/patchwork/series/{seriesID}", s.handlePatchworkGetSeries)
		api.Patch("/patchwork/patches/{patchID}", s.handlePatchworkUpdatePatch)
		api.Post("/patchwork/patches/{patchID}/delegate-to-me", s.handlePatchworkDelegateToMe)
		api.Get("/patchwork/projects", s.handlePatchworkProjects)
	})

	s.router = r
}

func (s *Server) renderLoginHTML(w http.ResponseWriter, errorMsg string, statusCode int) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(statusCode)

	errHTML := ""
	if errorMsg != "" {
		errHTML = fmt.Sprintf(`<div class="error-msg">%s</div>`, html.EscapeString(errorMsg))
	}

	const loginTpl = `<!DOCTYPE html>
<html lang="en">
<head>
  <meta charset="UTF-8">
  <meta name="viewport" content="width=device-width, initial-scale=1.0">
  <title>UndineD - Authentication</title>
  <link rel="icon" type="image/svg+xml" href="/favicon.svg">
  <link rel="preconnect" href="https://fonts.googleapis.com">
  <link rel="preconnect" href="https://fonts.gstatic.com" crossorigin>
  <link href="https://fonts.googleapis.com/css2?family=JetBrains+Mono:wght@400;500;600;700&display=swap" rel="stylesheet">
  <style>
    * { box-sizing: border-box; margin: 0; padding: 0; }
    body {
      background-color: #1e1e2e;
      color: #cdd6f4;
      font-family: "JetBrains Mono", monospace;
      display: flex;
      align-items: center;
      justify-content: center;
      min-height: 100vh;
      padding: 20px;
    }
    .login-box {
      background: #181825;
      border: 1px solid #313244;
      border-radius: 8px;
      padding: 32px;
      width: 100%;
      max-width: 500px;
      box-shadow: 0 8px 24px rgba(0, 0, 0, 0.4);
    }
    .brand {
      display: flex;
      align-items: center;
      gap: 12px;
      margin-bottom: 24px;
    }
    .brand h1 {
      font-size: 20px;
      font-weight: 700;
      color: #89b4fa;
      letter-spacing: -0.5px;
    }
    .brand span {
      font-size: 12px;
      color: #a6adc8;
      background: #313244;
      padding: 2px 6px;
      border-radius: 4px;
    }
    p.desc {
      font-size: 13px;
      line-height: 1.5;
      color: #a6adc8;
      margin-bottom: 20px;
    }
    .error-msg {
      background: rgba(243, 139, 168, 0.15);
      border: 1px solid #f38ba8;
      color: #f38ba8;
      padding: 10px 14px;
      border-radius: 6px;
      font-size: 12px;
      margin-bottom: 18px;
    }
    label {
      display: block;
      font-size: 12px;
      font-weight: 600;
      color: #cdd6f4;
      margin-bottom: 8px;
    }
    input[type="password"], input[type="text"] {
      width: 100%;
      padding: 10px 12px;
      background: #11111b;
      border: 1px solid #45475a;
      border-radius: 6px;
      color: #cdd6f4;
      font-family: inherit;
      font-size: 13px;
      margin-bottom: 20px;
      outline: none;
      transition: border-color 0.15s ease;
    }
    input:focus {
      border-color: #89b4fa;
    }
    button {
      width: 100%;
      background: #89b4fa;
      color: #11111b;
      font-family: inherit;
      font-weight: 600;
      font-size: 13px;
      padding: 10px;
      border: none;
      border-radius: 6px;
      cursor: pointer;
      transition: background 0.15s ease;
    }
    button:hover {
      background: #b4befe;
    }
    .hint {
      margin-top: 20px;
      font-size: 11px;
      color: #6c7086;
      line-height: 1.5;
    }
    code {
      color: #a6e3a1;
      background: #11111b;
      padding: 1px 4px;
      border-radius: 3px;
    }
  </style>
</head>
<body>
  <div class="login-box">
    <div class="brand">
      <h1>UndineD</h1>
      <span>Plaintext MUA</span>
    </div>
    <p class="desc">Authentication required to protect local mail server access in multi-user environments.</p>
    {{ERROR}}
    <form action="/login" method="POST">
      <label for="token">Authentication Token</label>
      <input type="password" id="token" name="token" placeholder="Paste 64-character token..." required autofocus autocomplete="off" />
      <button type="submit">Log In</button>
    </form>
    <div class="hint">
      Find your session token in <code>~/.local/share/undined/auth_token</code> or check your daemon startup terminal output.
    </div>
  </div>
</body>
</html>`

	doc := strings.ReplaceAll(loginTpl, "{{ERROR}}", errHTML)
	_, _ = w.Write([]byte(doc))
}

func (s *Server) handleIndex(w http.ResponseWriter, r *http.Request) {
	// 1. Startup URL bootstrap (?token=...)
	tokenParam := r.URL.Query().Get("token")
	if tokenParam != "" {
		if s.Security.IsTokenValid(tokenParam) {
			http.SetCookie(w, &http.Cookie{
				Name:     constants.SessionCookieName,
				Value:    s.Security.Token,
				Path:     "/",
				HttpOnly: true,
				SameSite: http.SameSiteStrictMode,
			})
			http.Redirect(w, r, "/", http.StatusFound)
			return
		}
		s.renderLoginHTML(w, "Invalid token provided in URL.", http.StatusUnauthorized)
		return
	}

	// 2. Cookie authentication
	cookie, err := r.Cookie(constants.SessionCookieName)
	if err == nil && s.Security.IsTokenValid(cookie.Value) {
		indexPath := filepath.Join(s.FrontendDir, "index.html")
		if _, statErr := os.Stat(indexPath); statErr == nil {
			http.ServeFile(w, r, indexPath)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"status":"UndineD Backend Active (frontend/index.html not found)"}`))
		return
	}

	// 3. Unauthenticated access -> Render login HTML
	s.renderLoginHTML(w, "", http.StatusUnauthorized)
}

func (s *Server) handleGetLogin(w http.ResponseWriter, r *http.Request) {
	cookie, err := r.Cookie(constants.SessionCookieName)
	if err == nil && s.Security.IsTokenValid(cookie.Value) {
		http.Redirect(w, r, "/", http.StatusFound)
		return
	}
	s.renderLoginHTML(w, "", http.StatusOK)
}

func (s *Server) handlePostLogin(w http.ResponseWriter, r *http.Request) {
	token := r.FormValue("token")
	if token == "" {
		// Try JSON body
		var body struct {
			Token string `json:"token"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		token = body.Token
	}

	if s.Security.IsTokenValid(token) {
		http.SetCookie(w, &http.Cookie{
			Name:     constants.SessionCookieName,
			Value:    s.Security.Token,
			Path:     "/",
			HttpOnly: true,
			SameSite: http.SameSiteStrictMode,
		})
		http.Redirect(w, r, "/", http.StatusFound)
		return
	}

	s.renderLoginHTML(w, "Invalid authentication token. Please check your token and try again.", http.StatusUnauthorized)
}

func (s *Server) handleFaviconSVG(w http.ResponseWriter, r *http.Request) {
	fav := filepath.Join(s.FrontendDir, "favicon.svg")
	if _, err := os.Stat(fav); err == nil {
		w.Header().Set("Content-Type", "image/svg+xml")
		http.ServeFile(w, r, fav)
		return
	}
	http.NotFound(w, r)
}

func (s *Server) handleFaviconICO(w http.ResponseWriter, r *http.Request) {
	fav := filepath.Join(s.FrontendDir, "favicon.ico")
	if _, err := os.Stat(fav); err == nil {
		w.Header().Set("Content-Type", "image/x-icon")
		http.ServeFile(w, r, fav)
		return
	}
	s.handleFaviconSVG(w, r)
}
