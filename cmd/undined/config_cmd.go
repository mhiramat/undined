package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/pelletier/go-toml/v2"

	"undined-go/internal/config"
)

func printConfigHelp() {
	fmt.Println(`Usage: undined config <subcommand> [options]

Subcommands:
  show, list                          Display full configuration file
  path                                Display path of active config.toml
  get <section.key>                   Display value of a specific setting
  set <section.key> <value>           Update a setting in config.toml
  edit                                Open config.toml in $EDITOR

Options:
  -C, --config <path>                 Specify custom path to config.toml

Examples:
  undined config path
  undined config show
  undined config get patchwork.delegate-users
  undined config set patchwork.delegate-users '["dev", "reviewer"]'
  undined config set general.editor_cmd 'nvim +{line} {file}'
  undined config edit`)
}

func runConfigCommand(args []string) int {
	var explicitConfig string

	// Extract optional -C or --config flags from args
	var filteredArgs []string
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if arg == "-C" || arg == "--config" {
			if i+1 < len(args) {
				explicitConfig = args[i+1]
				i++
			}
		} else if strings.HasPrefix(arg, "--config=") {
			explicitConfig = strings.TrimPrefix(arg, "--config=")
		} else if strings.HasPrefix(arg, "-C=") {
			explicitConfig = strings.TrimPrefix(arg, "-C=")
		} else {
			filteredArgs = append(filteredArgs, arg)
		}
	}

	if len(filteredArgs) == 0 {
		// Default to show config
		if err := doShowConfig(explicitConfig); err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			return 1
		}
		return 0
	}

	subcmd := strings.ToLower(filteredArgs[0])
	switch subcmd {
	case "show", "list":
		if err := doShowConfig(explicitConfig); err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			return 1
		}
		return 0

	case "path":
		if err := doShowConfigPath(explicitConfig); err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			return 1
		}
		return 0

	case "get":
		if len(filteredArgs) < 2 {
			fmt.Fprintln(os.Stderr, "Error: missing setting key. Usage: undined config get <section.key>")
			return 1
		}
		if err := doConfigGet(explicitConfig, filteredArgs[1]); err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			return 1
		}
		return 0

	case "set":
		if len(filteredArgs) < 2 {
			fmt.Fprintln(os.Stderr, "Error: missing arguments. Usage: undined config set <section.key> <value>")
			return 1
		}
		var valArgs []string
		if len(filteredArgs) > 2 {
			valArgs = filteredArgs[2:]
		}
		if err := doConfigSet(explicitConfig, filteredArgs[1], valArgs...); err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			return 1
		}
		return 0

	case "edit":
		if err := doConfigEdit(explicitConfig); err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			return 1
		}
		return 0

	case "help", "-h", "--help":
		printConfigHelp()
		return 0

	default:
		// If user typed: undined config <section.key> (shorthand for get)
		if strings.Contains(subcmd, ".") {
			if err := doConfigGet(explicitConfig, subcmd); err == nil {
				return 0
			}
		}
		fmt.Fprintf(os.Stderr, "Error: unknown config subcommand %q\n\n", subcmd)
		printConfigHelp()
		return 1
	}
}

func doShowConfigPath(configPath string) error {
	p := config.FindConfigFile(configPath)
	fmt.Println(config.DisplayPath(p))
	return nil
}

func doShowConfig(configPath string) error {
	p := config.FindConfigFile(configPath)
	data, err := os.ReadFile(p)
	if err != nil {
		if os.IsNotExist(err) {
			fmt.Printf("# Configuration file %s does not exist yet.\n", config.DisplayPath(p))
			fmt.Println("# Run 'undined config edit' or 'undined config set' to create it.")
			return nil
		}
		return fmt.Errorf("failed to read config file %s: %w", config.DisplayPath(p), err)
	}

	fmt.Printf("# Configuration: %s\n\n", config.DisplayPath(p))
	fmt.Print(string(data))
	if !strings.HasSuffix(string(data), "\n") {
		fmt.Println()
	}
	return nil
}

func doConfigGet(configPath, key string) error {
	cfg, err := config.Load(configPath)
	if err != nil {
		return fmt.Errorf("failed to load config %s: %w", config.DisplayPath(config.FindConfigFile(configPath)), err)
	}

	val, err := config.GetConfigValue(cfg, key)
	if err != nil {
		return err
	}
	fmt.Println(val)
	return nil
}

func doConfigSet(configPath, arg string, extraArgs ...string) error {
	var key, val string
	if len(extraArgs) > 0 {
		key = arg
		val = strings.Join(extraArgs, " ")
	} else if idx := strings.Index(arg, "="); idx > 0 {
		key = arg[:idx]
		val = arg[idx+1:]
	} else {
		return fmt.Errorf("missing value for setting %q. Usage: undined config set <section.key> <value>", arg)
	}

	targetPath := config.FindConfigFile(configPath)
	if err := config.SetTOMLValue(targetPath, key, val); err != nil {
		return err
	}

	fmt.Printf("Updated %s in %s\n", key, config.DisplayPath(targetPath))
	return nil
}

func doConfigEdit(configPath string) error {
	targetPath := config.FindConfigFile(configPath)
	if _, err := os.Stat(targetPath); os.IsNotExist(err) {
		dir := filepath.Dir(targetPath)
		_ = os.MkdirAll(dir, 0700)
		initialContent := `# Project UndineD Configuration File
[general]
host = "127.0.0.1"
port = 8080

[patchwork]
enabled = true
url = "https://patchwork.kernel.org"
api_version = "1.4"
# delegate-users = ["dev", "reviewer"]
`
		if err := os.WriteFile(targetPath, []byte(initialContent), 0600); err != nil {
			return fmt.Errorf("failed to create config file %s: %w", config.DisplayPath(targetPath), err)
		}
	}

	editor := os.Getenv("VISUAL")
	if editor == "" {
		editor = os.Getenv("EDITOR")
	}
	if editor == "" {
		cfg, _ := config.Load(configPath)
		if cfg != nil && cfg.General.EditorCmd != "" {
			editor = cfg.General.EditorCmd
		}
	}
	if editor == "" {
		if _, err := exec.LookPath("nano"); err == nil {
			editor = "nano"
		} else if _, err := exec.LookPath("vim"); err == nil {
			editor = "vim"
		} else {
			editor = "vi"
		}
	}

	quotedPath := "'" + strings.ReplaceAll(targetPath, "'", `'\''`) + "'"
	var cmd *exec.Cmd
	if strings.Contains(editor, "{file}") {
		cmdStr := strings.ReplaceAll(editor, "{file}", quotedPath)
		cmdStr = strings.ReplaceAll(cmdStr, "{line}", "1")
		cmd = exec.Command("sh", "-c", cmdStr)
	} else if strings.Contains(editor, " ") {
		cmd = exec.Command("sh", "-c", fmt.Sprintf("%s %s", editor, quotedPath))
	} else {
		cmd = exec.Command(editor, targetPath)
	}

	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	if err := cmd.Run(); err != nil {
		return fmt.Errorf("editor %q failed: %w", editor, err)
	}

	// Validate edited config file
	data, err := os.ReadFile(targetPath)
	if err != nil {
		return fmt.Errorf("failed to read %s after editing: %w", config.DisplayPath(targetPath), err)
	}

	var testCfg config.Config
	if err := toml.Unmarshal(data, &testCfg); err != nil {
		fmt.Fprintf(os.Stderr, "Warning: Configuration syntax error in %s:\n  %v\n", config.DisplayPath(targetPath), err)
		return fmt.Errorf("syntax error in configuration")
	}

	fmt.Printf("Configuration in %s is valid.\n", config.DisplayPath(targetPath))
	return nil
}
