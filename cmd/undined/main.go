// Package main is the entrypoint for the UndineD daemon.
package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"undined-go/internal/api"
	"undined-go/internal/bridge"
	"undined-go/internal/config"
	"undined-go/internal/imap"
	"undined-go/internal/lkml/b4"
	"undined-go/internal/lkml/review"
	"undined-go/internal/mcp"
	"undined-go/internal/patchwork"
	"undined-go/internal/process"
	"undined-go/internal/security"
	"undined-go/internal/smtp"
	"undined-go/internal/storage/database"
	"undined-go/internal/storage/maildir"
)

const version = "UndineD 0.1.0"

func findFrontendDir(extraCandidates ...string) string {
	candidates := append([]string{}, extraCandidates...)
	candidates = append(candidates,
		"./frontend",
		"../frontend",
	)

	exePath, err := os.Executable()
	if err == nil {
		exeDir := filepath.Dir(exePath)
		candidates = append([]string{
			filepath.Join(exeDir, "frontend"),
			filepath.Join(exeDir, "..", "frontend"),
		}, candidates...)
	}

	for _, c := range candidates {
		if c == "" {
			continue
		}
		indexPath := filepath.Join(c, "index.html")
		if _, err := os.Stat(indexPath); err == nil {
			return c
		}
	}
	return ""
}

func setupLogging(cfg *config.Config, mcpStdio bool) *os.File {
	if cfg == nil || cfg.General.LogFile == "" {
		return nil
	}
	logFilePath := cfg.General.LogFile
	if err := os.MkdirAll(filepath.Dir(logFilePath), 0700); err != nil {
		log.Printf("Warning: failed to create log directory for %s: %v", logFilePath, err)
		return nil
	}
	f, err := os.OpenFile(logFilePath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0600)
	if err != nil {
		log.Printf("Warning: failed to open log file %s: %v", logFilePath, err)
		return nil
	}
	if mcpStdio {
		log.SetOutput(f)
	} else {
		log.SetOutput(io.MultiWriter(os.Stderr, f))
	}
	return f
}

func main() {
	var configPath string
	var host string
	var port int
	var mcpStdio bool
	var logLevel string
	var showVersion bool
	var renewToken bool
	var noServer bool

	flag.StringVar(&configPath, "config", "", "Path to config.toml configuration file.")
	flag.StringVar(&configPath, "C", "", "Path to config.toml configuration file (shorthand).")
	flag.StringVar(&host, "host", "", "Host to bind Web UI and API (overrides config).")
	flag.IntVar(&port, "port", 0, "Port number to bind Web UI and API (overrides config).")
	flag.IntVar(&port, "p", 0, "Port number to bind Web UI and API (shorthand).")
	flag.BoolVar(&mcpStdio, "mcp-stdio", false, "Run in MCP STDIO mode (JSON-RPC 2.0 over stdin/stdout).")
	flag.StringVar(&logLevel, "log-level", "", "Logging level (debug, info, warning, error).")
	flag.BoolVar(&showVersion, "version", false, "Print version and exit.")
	flag.BoolVar(&showVersion, "v", false, "Print version and exit (shorthand).")
	flag.BoolVar(&renewToken, "renew-token", false, "Renew authentication token (generates a new token and overwrites auth_token file).")
	flag.BoolVar(&noServer, "no-server", false, "Do not start the HTTP server (useful with --renew-token to only rotate token and exit).")

	flag.Parse()

	if showVersion {
		fmt.Println(version)
		os.Exit(0)
	}

	// Always set process comm name to undined on Linux
	process.SetProcessName("undined")

	// 1. Load configuration
	cfg, err := config.Load(configPath)
	if err != nil {
		if configPath != "" {
			log.Fatalf("Failed to load specified config %s: %v", configPath, err)
		}
		cfg = config.NewDefaultConfig()
	}

	if host != "" {
		cfg.General.Host = host
	}
	if port != 0 {
		cfg.General.Port = port
	}
	if logLevel != "" {
		cfg.General.LogLevel = logLevel
	}

	// 2. Set up logging to file and display log file path immediately
	logFileHandle := setupLogging(cfg, mcpStdio)
	if logFileHandle != nil {
		defer logFileHandle.Close()
	}

	if mcpStdio {
		fmt.Fprintf(os.Stderr, "UndineD MCP running. Log file: %s\n", cfg.General.LogFile)
	} else {
		log.Printf("UndineD log file: %s", cfg.General.LogFile)
	}

	// 3. Initialize common storage and components
	store := maildir.NewMaildirStore(cfg.General.MaildirPath)
	db, err := database.NewDatabase(cfg.General.DBPath)
	if err != nil {
		log.Fatalf("Failed to initialize database at %s: %v", cfg.General.DBPath, err)
	}
	defer db.Close()

	b4Runner := b4.NewB4Runner(cfg, store, db)
	pwClient := patchwork.NewClient(cfg, db, store)

	// Mode A: MCP STDIO Server Mode
	if mcpStdio {
		// In STDIO mode, stdout is strictly reserved for JSON-RPC messages.
		mcpServer := mcp.NewServer(cfg, store, db, b4Runner, pwClient)
		if err := mcpServer.HandleSTDIO(os.Stdin, os.Stdout); err != nil && err != io.EOF {
			fmt.Fprintf(os.Stderr, "MCP STDIO error: %v\n", err)
			os.Exit(1)
		}
		return
	}

	// Mode B: Main Daemon Web UI and REST API Mode
	sec, err := security.NewSecurityManager(cfg.General.Host, cfg.General.Port, cfg.General.DataDir, renewToken)
	if err != nil {
		log.Fatalf("Failed to initialize security auth token: %v", err)
	}

	if renewToken {
		log.Printf("UndineD security token renewed. Token SHA-256 fingerprint: %s", sec.GetTokenFingerprint())
	} else {
		log.Printf("UndineD security initialized. Token SHA-256 fingerprint: %s", sec.GetTokenFingerprint())
	}

	if noServer {
		fmt.Println()
		fmt.Println("=================================================================")
		if renewToken {
			fmt.Println(" UndineD Authentication Token Renewed")
		} else {
			fmt.Println(" UndineD Authentication Token")
		}
		fmt.Println("=================================================================")
		fmt.Printf("🔑 Auth Token file:      %s\n", sec.TokenPath)
		fmt.Printf("🔑 Authentication Token: %s\n", sec.Token)
		tokenURL := fmt.Sprintf("http://%s:%d/?token=%s", cfg.General.Host, cfg.General.Port, sec.Token)
		fmt.Printf("🚀 Web UI URL:           %s\n", tokenURL)
		if cfg.General.LogFile != "" {
			fmt.Printf("📝 Log file:             %s\n", cfg.General.LogFile)
		}
		fmt.Println("=================================================================")
		fmt.Println()
		return
	}

	reviewRunner := review.NewRunner(cfg, store, db)
	smtpClient := smtp.NewClient(cfg, store, db)
	launcher := bridge.NewLauncher(cfg, db)

	var poller *imap.Poller
	if cfg.IMAP.Username != "" {
		poller = imap.NewPoller(cfg, store, db)
	}

	frontendDir := findFrontendDir()
	apiServer := api.NewServer(cfg, db, store, sec, poller, smtpClient, b4Runner, reviewRunner, pwClient, launcher, frontendDir)

	tokenURL := fmt.Sprintf("http://%s:%d/?token=%s", cfg.General.Host, cfg.General.Port, sec.Token)

	fmt.Println()
	fmt.Println("=================================================================")
	fmt.Println(" Starting Project UndineD (Linux Kernel MUA Daemon & Web UI)")
	fmt.Println("=================================================================")
	fmt.Printf("🚀 Web UI running at: %s (Token authentication required)\n", tokenURL)
	fmt.Printf("🤖 MCP Server tool:   undined --mcp-stdio\n")
	fmt.Printf("🔑 Auth Token file:   %s\n", sec.TokenPath)
	fmt.Printf("🔑 Authentication Token: %s\n", sec.Token)
	fmt.Printf("📝 Log file:          %s\n", cfg.General.LogFile)
	fmt.Println("=================================================================")
	fmt.Println()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	if poller != nil {
		go poller.Start(ctx)
	}

	serverAddr := fmt.Sprintf("%s:%d", cfg.General.Host, cfg.General.Port)
	srv := &http.Server{
		Addr:    serverAddr,
		Handler: apiServer.Router(),
	}

	go func() {
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("HTTP server failed to listen on %s: %v", serverAddr, err)
		}
	}()

	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, os.Interrupt, syscall.SIGTERM)
	<-sigChan

	log.Println("Shutting down UndineD gracefully...")
	cancel()

	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer shutdownCancel()

	if err := srv.Shutdown(shutdownCtx); err != nil {
		log.Printf("Server shutdown error: %v", err)
	}

	log.Println("UndineD daemon terminated.")
}
