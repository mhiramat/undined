// Package main is the entrypoint for the UndineD daemon.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
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
	if mcpStdio || os.Getenv("UNDINED_DAEMON_CHILD") == "1" {
		log.SetOutput(f)
	} else {
		log.SetOutput(io.MultiWriter(os.Stderr, f))
	}
	return f
}

type daemonReadyMsg struct {
	Status        string `json:"status"`
	Error         string `json:"error,omitempty"`
	PID           int    `json:"pid,omitempty"`
	PIDFile       string `json:"pid_file,omitempty"`
	WebURL        string `json:"web_url,omitempty"`
	AuthTokenFile string `json:"auth_token_file,omitempty"`
	AuthToken     string `json:"auth_token,omitempty"`
	LogFile       string `json:"log_file,omitempty"`
}

func sendDaemonMsg(pipe *os.File, msg daemonReadyMsg) {
	if pipe == nil {
		return
	}
	_ = json.NewEncoder(pipe).Encode(msg)
	_ = pipe.Close()
}

func main() {
	if len(os.Args) > 1 && os.Args[1] == "config" {
		os.Exit(runConfigCommand(os.Args[2:]))
	}

	var configPath string
	var host string
	var port int
	var mcpStdio bool
	var logLevel string
	var showVersion bool
	var renewToken bool
	var noServer bool
	var daemonMode bool
	var quitMode bool
	var pidFileFlag string
	var showConfig bool
	var getConfigKey string
	var setConfigVal string
	var editConfig bool
	var showConfigPath bool

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
	flag.BoolVar(&daemonMode, "daemon", false, "Run UndineD as a background daemon detached from terminal.")
	flag.BoolVar(&daemonMode, "d", false, "Run UndineD as a background daemon (shorthand).")
	flag.BoolVar(&quitMode, "quit", false, "Terminate running UndineD daemon instance.")
	flag.BoolVar(&quitMode, "q", false, "Terminate running UndineD daemon instance (shorthand).")
	flag.StringVar(&pidFileFlag, "pid-file", "", "Path to PID file (overrides config).")
	flag.BoolVar(&showConfig, "show-config", false, "Print active configuration and exit.")
	flag.StringVar(&getConfigKey, "config-get", "", "Print value of a config setting (e.g. patchwork.delegate-users) and exit.")
	flag.StringVar(&setConfigVal, "config-set", "", "Set a config setting in section.key=value format and exit.")
	flag.BoolVar(&editConfig, "config-edit", false, "Open configuration file in $EDITOR and exit.")
	flag.BoolVar(&editConfig, "edit-config", false, "Open configuration file in $EDITOR (shorthand).")
	flag.BoolVar(&showConfigPath, "config-path", false, "Print active configuration file path and exit.")

	flag.Parse()

	if showVersion {
		fmt.Println(version)
		os.Exit(0)
	}

	if showConfigPath {
		if err := doShowConfigPath(configPath); err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(1)
		}
		os.Exit(0)
	}

	if showConfig {
		if err := doShowConfig(configPath); err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(1)
		}
		os.Exit(0)
	}

	if getConfigKey != "" {
		if err := doConfigGet(configPath, getConfigKey); err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(1)
		}
		os.Exit(0)
	}

	if setConfigVal != "" {
		if err := doConfigSet(configPath, setConfigVal); err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(1)
		}
		os.Exit(0)
	}

	if editConfig {
		if err := doConfigEdit(configPath); err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(1)
		}
		os.Exit(0)
	}

	if mcpStdio && daemonMode {
		fmt.Fprintln(os.Stderr, "Error: --daemon cannot be used with --mcp-stdio (STDIO mode requires interactive stdin/stdout).")
		os.Exit(1)
	}

	if quitMode {
		cfg, err := config.Load(configPath)
		if err != nil && configPath != "" {
			log.Fatalf("Failed to load specified config %s: %v", configPath, err)
		}
		if cfg == nil {
			cfg = config.NewDefaultConfig()
		}
		pidFile := cfg.General.PIDFile
		if pidFileFlag != "" {
			pidFile = config.ExpandPath(pidFileFlag)
		}

		pid, running, err := process.CheckRunning(pidFile)
		if err != nil {
			if os.IsNotExist(err) {
				fmt.Printf("UndineD is not running (PID file %s does not exist).\n", pidFile)
				os.Exit(0)
			}
			fmt.Fprintf(os.Stderr, "Error reading PID file %s: %v\n", pidFile, err)
			os.Exit(1)
		}

		if !running {
			_ = process.RemovePIDFile(pidFile)
			fmt.Printf("UndineD is not running (removed stale PID file %s).\n", pidFile)
			os.Exit(0)
		}

		fmt.Printf("Stopping UndineD (PID %d)...\n", pid)
		if err := process.TerminateProcess(pid, 5*time.Second); err != nil {
			fmt.Fprintf(os.Stderr, "Failed to terminate UndineD (PID %d): %v\n", pid, err)
			os.Exit(1)
		}
		_ = process.RemovePIDFile(pidFile)
		fmt.Printf("UndineD (PID %d) stopped successfully.\n", pid)
		os.Exit(0)
	}

	isDaemonChild := os.Getenv("UNDINED_DAEMON_CHILD") == "1"
	var readyPipe *os.File
	if isDaemonChild {
		readyPipe = os.NewFile(uintptr(3), "ready_pipe")
	}

	if daemonMode && !isDaemonChild {
		// Parent process in daemon mode: check if already running, fork child with setsid, wait for readiness, print info and exit.
		cfg, err := config.Load(configPath)
		if err != nil && configPath != "" {
			log.Fatalf("Failed to load specified config %s: %v", configPath, err)
		}
		if cfg == nil {
			cfg = config.NewDefaultConfig()
		}
		pidFile := cfg.General.PIDFile
		if pidFileFlag != "" {
			pidFile = config.ExpandPath(pidFileFlag)
		}

		if pid, running, _ := process.CheckRunning(pidFile); running {
			fmt.Fprintf(os.Stderr, "Error: UndineD is already running (PID: %d). Use 'undined --quit' to stop it.\n", pid)
			os.Exit(1)
		}

		readyR, readyW, err := os.Pipe()
		if err != nil {
			log.Fatalf("Failed to create pipe for daemonization: %v", err)
		}

		exe, err := os.Executable()
		if err != nil {
			log.Fatalf("Failed to determine executable path: %v", err)
		}

		cmd := exec.Command(exe, os.Args[1:]...)
		cmd.Env = append(os.Environ(), "UNDINED_DAEMON_CHILD=1")
		cmd.SysProcAttr = &syscall.SysProcAttr{
			Setsid: true,
		}
		devNull, err := os.Open(os.DevNull)
		if err == nil {
			cmd.Stdin = devNull
			cmd.Stdout = devNull
			cmd.Stderr = devNull
			defer devNull.Close()
		}
		cmd.ExtraFiles = []*os.File{readyW}

		if err := cmd.Start(); err != nil {
			log.Fatalf("Failed to spawn daemon process: %v", err)
		}
		_ = readyW.Close()

		resultChan := make(chan daemonReadyMsg, 1)
		errChan := make(chan error, 1)

		go func() {
			decoder := json.NewDecoder(readyR)
			var msg daemonReadyMsg
			if err := decoder.Decode(&msg); err != nil {
				errChan <- err
				return
			}
			resultChan <- msg
		}()

		select {
		case msg := <-resultChan:
			_ = readyR.Close()
			if msg.Status != "ok" {
				fmt.Fprintf(os.Stderr, "Failed to start UndineD daemon: %s\n", msg.Error)
				os.Exit(1)
			}
			fmt.Println()
			fmt.Println("=================================================================")
			fmt.Println(" Starting Project UndineD (Linux Kernel MUA Daemon & Web UI)")
			fmt.Println("=================================================================")
			fmt.Printf("🚀 Web UI running at:    %s (Token authentication required)\n", msg.WebURL)
			fmt.Printf("🤖 MCP Server tool:      undined --mcp-stdio\n")
			fmt.Printf("🔑 Auth Token file:      %s\n", msg.AuthTokenFile)
			fmt.Printf("🔑 Authentication Token: %s\n", msg.AuthToken)
			if msg.LogFile != "" {
				fmt.Printf("📝 Log file:             %s\n", msg.LogFile)
			}
			fmt.Printf("📌 PID file:             %s (PID: %d)\n", msg.PIDFile, msg.PID)
			fmt.Println("=================================================================")
			fmt.Printf("Daemon started in background. To terminate, run: undined --quit\n\n")
			os.Exit(0)
		case err := <-errChan:
			_ = readyR.Close()
			_ = cmd.Wait()
			fmt.Fprintf(os.Stderr, "Daemon child process failed during startup: %v\n", err)
			os.Exit(1)
		case <-time.After(10 * time.Second):
			_ = readyR.Close()
			if cmd.Process != nil {
				_ = cmd.Process.Kill()
			}
			fmt.Fprintf(os.Stderr, "Timed out waiting for daemon to start\n")
			os.Exit(1)
		}
	}

	// Always set process comm name to undined on Linux
	process.SetProcessName("undined")

	// 1. Load configuration
	cfg, err := config.Load(configPath)
	if err != nil {
		if configPath != "" {
			if isDaemonChild {
				sendDaemonMsg(readyPipe, daemonReadyMsg{
					Status: "error",
					Error:  fmt.Sprintf("Failed to load specified config %s: %v", configPath, err),
				})
				os.Exit(1)
			}
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
	if pidFileFlag != "" {
		cfg.General.PIDFile = config.ExpandPath(pidFileFlag)
	}
	pidFile := cfg.General.PIDFile

	// 2. Set up logging to file
	logFileHandle := setupLogging(cfg, mcpStdio)
	if logFileHandle != nil {
		defer logFileHandle.Close()
	}

	if mcpStdio {
		fmt.Fprintf(os.Stderr, "UndineD MCP running. Log file: %s\n", cfg.General.LogFile)
	} else if !isDaemonChild {
		log.Printf("UndineD log file: %s", cfg.General.LogFile)
	}

	// Check if already running (for server modes)
	if !mcpStdio && !noServer {
		pid, running, err := process.CheckRunning(pidFile)
		if err != nil {
			log.Printf("Warning: error reading existing PID file %s: %v (overwriting)", pidFile, err)
		} else if running && pid != os.Getpid() {
			errMsg := fmt.Sprintf("UndineD is already running (PID: %d). Use 'undined --quit' to stop it.", pid)
			if isDaemonChild {
				sendDaemonMsg(readyPipe, daemonReadyMsg{
					Status: "error",
					Error:  errMsg,
				})
				os.Exit(1)
			}
			log.Fatalf("%s", errMsg)
		}
	}

	// 3. Initialize common storage and components
	store := maildir.NewMaildirStore(cfg.General.MaildirPath)
	db, err := database.NewDatabase(cfg.General.DBPath)
	if err != nil {
		errMsg := fmt.Sprintf("Failed to initialize database at %s: %v", cfg.General.DBPath, err)
		if isDaemonChild {
			sendDaemonMsg(readyPipe, daemonReadyMsg{
				Status: "error",
				Error:  errMsg,
			})
			os.Exit(1)
		}
		log.Fatalf("%s", errMsg)
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
		errMsg := fmt.Sprintf("Failed to initialize security auth token: %v", err)
		if isDaemonChild {
			sendDaemonMsg(readyPipe, daemonReadyMsg{
				Status: "error",
				Error:  errMsg,
			})
			os.Exit(1)
		}
		log.Fatalf("%s", errMsg)
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

	serverAddr := fmt.Sprintf("%s:%d", cfg.General.Host, cfg.General.Port)
	ln, err := net.Listen("tcp", serverAddr)
	if err != nil {
		errMsg := fmt.Sprintf("failed to listen on %s: %v", serverAddr, err)
		if isDaemonChild {
			sendDaemonMsg(readyPipe, daemonReadyMsg{
				Status: "error",
				Error:  errMsg,
			})
			os.Exit(1)
		}
		log.Fatalf("%s", errMsg)
	}
	defer ln.Close()

	if err := process.WritePIDFile(pidFile, os.Getpid()); err != nil {
		errMsg := fmt.Sprintf("failed to write PID file %s: %v", pidFile, err)
		if isDaemonChild {
			sendDaemonMsg(readyPipe, daemonReadyMsg{
				Status: "error",
				Error:  errMsg,
			})
			os.Exit(1)
		}
		log.Fatalf("%s", errMsg)
	}
	defer process.RemovePIDFile(pidFile)

	tokenURL := fmt.Sprintf("http://%s:%d/?token=%s", cfg.General.Host, cfg.General.Port, sec.Token)

	if isDaemonChild {
		sendDaemonMsg(readyPipe, daemonReadyMsg{
			Status:        "ok",
			PID:           os.Getpid(),
			PIDFile:       pidFile,
			WebURL:        tokenURL,
			AuthTokenFile: sec.TokenPath,
			AuthToken:     sec.Token,
			LogFile:       cfg.General.LogFile,
		})
	} else {
		fmt.Println()
		fmt.Println("=================================================================")
		fmt.Println(" Starting Project UndineD (Linux Kernel MUA Daemon & Web UI)")
		fmt.Println("=================================================================")
		fmt.Printf("🚀 Web UI running at:    %s (Token authentication required)\n", tokenURL)
		fmt.Printf("🤖 MCP Server tool:      undined --mcp-stdio\n")
		fmt.Printf("🔑 Auth Token file:      %s\n", sec.TokenPath)
		fmt.Printf("🔑 Authentication Token: %s\n", sec.Token)
		if cfg.General.LogFile != "" {
			fmt.Printf("📝 Log file:             %s\n", cfg.General.LogFile)
		}
		fmt.Printf("📌 PID file:             %s (PID: %d)\n", pidFile, os.Getpid())
		fmt.Println("=================================================================")
		fmt.Println()
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	if poller != nil {
		go poller.Start(ctx)
	}

	srv := &http.Server{
		Handler: apiServer.Router(),
	}

	go func() {
		if err := srv.Serve(ln); err != nil && err != http.ErrServerClosed {
			log.Fatalf("HTTP server failed: %v", err)
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

	_ = process.RemovePIDFile(pidFile)
	log.Println("UndineD daemon terminated.")
}
