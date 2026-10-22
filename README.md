# UndineD (Golang Edition)

**A Modern, Plaintext-Strict MUA Daemon & Review Workstation for Linux Kernel Development**  
*Single Static Binary · Selective IMAP Sync · Side-by-Side Diff · b4 Integration · Model Context Protocol (MCP)*

---

## 1. Overview & Architecture

**UndineD** is a headless email daemon and web workstation tailored specifically for Linux kernel maintainers and contributors. Inspired by the classic Sylpheed email client, it adheres strictly to Linux kernel mailing list (LKML) conventions: **strict plain-text UTF-8**, real tab retention (`tab-size: 8`), zero HTML, and automated inline review quote generation.

```
                    +------------------------------------+
                    |  Web Browser Interface (:8080)    |
                    |  (Sylpheed 3-Pane + Side-by-Side)  |
                    +-----------------+------------------+
                                      | HTTP REST / SSE
                                      v
+------------------+       +---------------------+       +-------------------+
| AI Review Agents | <---> |   UndineD Daemon    | <---> | lore.kernel.org   |
| (via MCP STDIO)  |       |   (bin/undined)     |       | (b4 mbox / git am)|
+------------------+       +----------+----------+       +-------------------+
                                      |
         +----------------------------+----------------------------+
         |                            |                            |
         v                            v                            v
+-----------------+          +-----------------+          +-----------------+
|   IMAP Server   |          |   SMTP Server   |          | patchwork.kernel|
| (Selective Sync)|          | (Strict Plain)  |          | (REST API v1.4) |
+-----------------+          +-----------------+          +-----------------+
```

### Core Design Principles
* **Strict Plain-Text LKML Delivery**: Guarantees pure 8-bit UTF-8 plain-text, exact tab character retention (`\t`), no line wrapping, no `multipart/alternative`, and injects the canonical `X-Mailer: UndineD 0.1` header.
* **Two-Stage On-Demand IMAP Sync**: Background polling fetches lightweight envelope summaries (`BODY.PEEK[HEADER.FIELDS]`) to populate folders and thread trees instantly. Full message bodies are hydrated on demand.
* **On-Demand Hydration via b4**: Solitary kernel replies or patch series are dynamically completed from [lore.kernel.org](https://lore.kernel.org) via `b4 mbox`.
* **Model Context Protocol (MCP) Native**: Embedded JSON-RPC 2.0 server allowing AI coding assistants (such as Antigravity and Gemini CLI) to search archives, retrieve thread trees, review patches, and post inline comments.
* **Drop-in Compatibility**: Reads and writes identical Maildir directories, SQLite schemas (`undine.db` with FTS5), and `config.toml` configuration files.

---

## 2. Prerequisites & System Requirements

* **Operating System**: Linux (x86_64 or aarch64; Debian, Ubuntu, Fedora, Arch, etc.)
* **Go Compiler**: Go 1.27 or newer (tested on Go 1.27.3)
* **C Toolchain**: `gcc` / `build-essential` (required for CGO SQLite FTS5 compilation)
* **Kernel & Git Utilities**:
  * `git` (2.25+)
  * `b4` (0.14+ recommended, for lore.kernel.org patch fetching)
* **Terminal Editor (Optional)**: `foot`, `alacritty`, `nvim`, `vim`, `emacsclient` (for external reply editing)
* **Password Helpers (Optional)**: `pass`, `secret-tool`, or `gpg`

### Installing Prerequisites

#### Debian / Ubuntu:
```bash
sudo apt update
sudo apt install -y build-essential git b4
# Ensure Go 1.27+ is installed. If apt provides an older version,
# install the official Go binary from https://go.dev/dl/ or via snap:
# sudo snap install go --classic
```

#### Fedora / RHEL:
```bash
sudo dnf install -y gcc git b4 golang
# Ensure 'go version' >= 1.27
```

#### Arch Linux:
```bash
sudo pacman -S go base-devel git b4
```

### Key Go Dependencies
Managed automatically via `go.mod`:
* `github.com/go-chi/chi/v5` (v5.5.0) — Lightweight HTTP REST router
* `github.com/mattn/go-sqlite3` (v1.14.52) — SQLite3 engine with FTS5 virtual tables
* `github.com/jhillyerd/enmime` (v1.3.0) — MIME email parsing and attachment decoding
* `github.com/emersion/go-imap` (v1.2.1) — IMAP4rev1 client for selective synchronization
* `github.com/pelletier/go-toml/v2` (v2.4.3) — Strict TOML v1.0 configuration loader

---

## 3. Installation & Building

### 3.1 Clone Repository
```bash
git clone https://github.com/mhiramat/undined.git
cd undined
```

### 3.2 Build Standalone Binary
Build using the included [`Makefile`](Makefile) (automatically compiles with the required SQLite FTS5 build tag):
```bash
make build
```
The compiled binary will be placed at `bin/undined`.

To verify the build:
```bash
./bin/undined --version
# Output: UndineD 0.1.0
```

### 3.3 Run Test Suite
Verify that all unit and integration tests pass:
```bash
make test
```

### 3.4 Optional: Install to System PATH
```bash
# Install to user local bin
mkdir -p ~/.local/bin
cp bin/undined ~/.local/bin/

# Or install system-wide
sudo install -m 755 bin/undined /usr/local/bin/
```

---

## 4. Configuration (`config.toml`)

UndineD follows the **XDG Base Directory Specification**:
* **Configuration File**: `~/.config/undined/config.toml` (or custom path via `--config <path>`)
* **Local Data & Maildir**: `~/.local/share/undined/`
* **Log File**: `~/.cache/undined/undined.log`
* **Auth Token**: `~/.local/share/undined/auth_token` (auto-generated POSIX `0600`)
* **PID File**: `~/.local/share/undined/undined.pid` (auto-managed for daemon control)

Create `~/.config/undined/config.toml` with your credentials:

```toml
[general]
host = "127.0.0.1"
port = 8080
maildir_path = "~/.local/share/undined/maildir"
db_path = "~/.local/share/undined/undine.db"
log_file = "~/.cache/undined/undined.log"
log_level = "info"

[imap]
host = "imap.example.com"
port = 993
username = "dev@example.com"
# Retrieve password dynamically via CLI command or raw password:
password_eval = "pass show mail/dev"
use_ssl = true
verify_cert = true
poll_interval_seconds = 60
sync_folders = ["INBOX", "LKML", "Sent"]

[smtp]
host = "smtp.example.com"
port = 587
username = "dev@example.com"
password_eval = "pass show mail/dev"
from_name = "Kernel Developer"
from_email = "dev@example.com"
use_starttls = true
signature = "-- \nKernel Developer"

[lkml]
b4_cmd = "b4"
git_cmd = "git"
target_git_repo = "~/linux"
# External terminal editor launcher command (optional):
editor_cmd = "foot -e nvim +{line} {file}"
reply_use_external_editor = false

[review]
command = "agy --print --model=flash"
auto_review = false

[patchwork]
url = "https://patchwork.kernel.org"
api_version = "1.4"
token_eval = "pass show api/patchwork.kernel.org"
todo_states = ["new", "under-review"]
```

---

## 5. Usage & Workflows

### 5.1 Launching the Daemon & Web Workstation

#### Background Daemon Mode (Recommended)
To run UndineD detached from your terminal:
```bash
./bin/undined --daemon
# or shorthand:
./bin/undined -d
```
On startup, UndineD displays the necessary connection parameters and PID details, writes the PID file (`~/.local/share/undined/undined.pid`), and detaches from the terminal:

```
=================================================================
 Starting Project UndineD (Linux Kernel MUA Daemon & Web UI)
=================================================================
🚀 Web UI running at:    http://127.0.0.1:8080/?token=404dfcf8f602... (Token authentication required)
🤖 MCP Server tool:      undined --mcp-stdio
🔑 Auth Token file:      ~/.local/share/undined/auth_token
🔑 Authentication Token: 404dfcf8f602b9e672f23fc8e64c20f1...
📝 Log file:             ~/.cache/undined/undined.log
📌 PID file:             ~/.local/share/undined/undined.pid (PID: 12345)
=================================================================
Daemon started in background. To terminate, run: undined --quit
```

#### Stopping the Daemon
To cleanly terminate a running UndineD instance:
```bash
./bin/undined --quit
# or shorthand:
./bin/undined -q
```
This reads the PID file, signals the process with `SIGTERM` for graceful shutdown, verifies process exit, and cleans up the PID file.

#### Foreground Mode
To run UndineD directly in the foreground attached to your current terminal session:
```bash
./bin/undined
```
Press `Ctrl+C` or send `SIGTERM` to stop gracefully.

Open the Web UI URL in your browser to access the Sylpheed-style 3-pane review interface.

#### Command-Line Flags
| Flag | Shorthand | Description | Default |
|---|---|---|---|
| `--daemon` | `-d` | Run UndineD as a background daemon detached from terminal | `false` |
| `--quit` | `-q` | Terminate running UndineD daemon instance | `false` |
| `--pid-file <path>` | | Path to PID file (overrides config) | `~/.local/share/undined/undined.pid` |
| `--config <path>` | `-C <path>` | Path to TOML configuration file | `~/.config/undined/config.toml` |
| `--host <host>` | | Host interface to bind Web UI | `127.0.0.1` (or from config) |
| `--port <port>` | `-p <port>` | Port number to bind Web UI | `8080` (or from config) |
| `--mcp-stdio` | | Run in MCP JSON-RPC 2.0 STDIO mode | `false` |
| `--log-level <lvl>` | | Log level (`debug`, `info`, `warning`, `error`) | `info` |
| `--renew-token` | | Renew authentication token (generates a new token and overwrites auth_token file) | `false` |
| `--no-server` | | Do not start the HTTP server (useful with `--renew-token` to rotate token and exit) | `false` |
| `--version` | `-v` | Output version and exit | |
| `--help` | `-h` | Display usage options | |

#### Renewing Authentication Token
To rotate your authentication token:
- **Renew and start daemon**:
  ```bash
  ./bin/undined --renew-token
  ```
  Generates a new cryptographically secure 256-bit token, updates `auth_token`, and starts the Web UI server with the new token.
- **Renew and exit immediately** (without starting the server or binding ports):
  ```bash
  ./bin/undined --renew-token --no-server
  ```

---

### 5.2 Model Context Protocol (MCP) Server for AI Review Agents

UndineD natively speaks the **Model Context Protocol (MCP)** over STDIO, allowing AI review agents (like Antigravity or Gemini CLI) to interact directly with your Linux kernel mail archive.

#### Registering with Antigravity (`agy`):
```bash
agy mcp add undine ./bin/undined --mcp-stdio
```

#### Available MCP Tools:
| Tool Name | Parameters | Description |
|---|---|---|
| `list_recent_emails` | `folder`, `limit` | List recent email summaries filtered by mailbox folder. |
| `search_emails` | `query`, `limit` | Full-text search emails using SQLite FTS5. |
| `get_thread_context` | `message_id` | Retrieve complete thread hierarchy and diff contents. |
| `import_patch_series_b4` | `message_id_or_url` | Download thread from lore.kernel.org via `b4` into Maildir. |
| `create_review_draft` | `target_message_id`, `general_feedback`, `inline_notes` | Build an LKML quoted review email in Maildir `Drafts`. |
| `get_patchwork_patch_info` | `message_id` | Query patchwork.kernel.org for CI checks and delegation. |
| `list_my_delegated_patches` | `state`, `project`, `limit` | List patches assigned to maintainer on Patchwork. |
| `update_patchwork_patch` | `patch_id`, `state`, `delegate_to_me` | Update patch review state or self-delegate. |
| `post_review_comment` | `message_id`, `target`, `comment`, `side` | Post inline review note at `FILE:LINE` onto patch diff. |
| `post_diff_comment` | `...` | Alias for `post_review_comment`. |
| `add_diff_comment` | `...` | Alias for `post_review_comment`. |

*All MCP tool calls write structured audit logs to `~/.cache/undined/undined.log`.*

---

### 5.3 Review Workflows

1. **Browsing & Thread Navigation**:
   * Navigate message summaries with keyboard shortcuts `j` (down) and `k` (up).
   * Search message bodies and subjects with SQLite FTS5 using `/`.
2. **Side-by-Side Patch Review**:
   * Patch emails automatically render in dual-column pre-image and post-image diff view.
   * Hover over any line number in the diff gutter and click `+` to open an inline review note.
3. **One-Click Draft Merging**:
   * Click **Reply** or press `r` to aggregate inline notes into standard LKML quoted plain text with 2–3 lines of context.
   * Edit using the built-in tab-preserving editor or launch your external `$EDITOR` via `Ctrl+E`.
4. **Patchwork Synchronization**:
   * View CI check results directly in the email header drawer.
   * Update patch state (e.g. *Under Review*, *Accepted*, *Changes Requested*) or click `👤 Delegate to Me`.

---

## 6. Development & Testing

```bash
# Clean build artifacts
make clean

# Compile binary
make build

# Run all test suites
make test

# Run daemon directly
make run
```

---

## 7. License

Project UndineD is licensed under the **Apache License 2.0**. See `LICENSE` for details.
