# **Project UndineD (Go Implementation)**

**A Modern, Plaintext-Strict MUA for Linux Kernel Development with Selective Sync, Side-by-Side Diff, b4 Integration, Patchwork Management, and Model Context Protocol (MCP)**

---

## **1. Overview & Core Philosophy**

### **1.1 Heritage & Purpose**

Project **UndineD** is inspired by the classic Sylpheed email client. While Sylpheed provided decades of lightweight, keyboard-driven, and highly reliable email management based on the one-email-one-file principle, modern desktop environments demand a decoupled, headless architecture with native AI agent integration.

UndineD decouples the traditional MUA into:
1. **A Headless Daemon Engine (in Golang)**: Managing IMAP polling, Maildir storage, SQLite indexing with FTS5, strict plain-text SMTP transport, external tool orchestration (`b4`, `git`, `agy`, `$EDITOR`), Patchwork REST API integration, and an embedded Model Context Protocol (MCP) server.
2. **A Responsive Local Web Frontend**: Serving a classic Sylpheed-style 3-pane UI accessible via `http://localhost:<port>`, featuring a responsive Side-by-Side patch viewer, inline line-number annotation bubbles, built-in plaintext composer, and Patchwork metadata controls.

### **1.2 Kernel Development Principles**

* **Strict Plain-Text Fidelity**: Zero HTML generation. No multipart/alternative, no format=flowed, no line wrapping or whitespace mangling. Tab stops strictly respect 8-character alignments (`tab-size: 8`). Outgoing mail enforces 8-bit UTF-8 plaintext with `X-Mailer: UndineD 0.1`.
* **Patch Integrity & Non-ASCII Rejection**: Patches containing non-ASCII characters in diff lines are treated as corrupted. Rather than silently normalizing characters (which threatens kernel codebase hygiene and compiler safety), UndineD rejects SideDiff, alerts the reviewer, and displays raw text with non-ASCII characters visually escaped. See [POLICY_PATCH_INTEGRITY.md](POLICY_PATCH_INTEGRITY.md).
* **Selective IMAP Sync & Lightweight Header Fetching**: Never poll bloated LKML archival folders from IMAP. Synchronize only designated personal/direct labels. Fetch fast header summaries to render mail lists instantly, downloading full RFC822 bodies on-demand.
* **On-Demand Hydration via b4**: Dynamically fetch missing parent threads and patch series from `lore.kernel.org` using `b4 mbox` whenever a solitary reply or patch arrives.
* **Patchwork Management**: Full integration with `patchwork.kernel.org` (REST API v1.4) to view patch review status, assign delegates, check CI test results, and download delegated patches directly into the local `TODO` mailbox.
* **Dual Editor Architecture**: Inline review notes taken in the browser are automatically merged into clean, RFC-compliant plain-text quotes. Reviewers can compose replies directly in the built-in browser editor (preserving native browser undo/redo history) or hand off to an external desktop terminal editor (`foot`, `nvim`, `vim`).
* **AI-Agent Native (MCP)**: First-class MCP tool endpoints allowing Antigravity or other agents to search archives, retrieve threads, inspect Patchwork metadata, review patches, and post targeted line-specific review comments.

---

## **2. System Architecture**

```
                    +-------------------------------------------------------+
                    |                    Remote Services                    |
                    |  IMAP           | SMTP        | lore.kernel.org       | patchwork.kernel.org
                    |  (Selective)    | (Plaintext) | (b4 mbox / am)        | (REST API v1.4)
                    +--------+--------------+---------------+---------------+
                             |              ^               ^               ^
                             v              |               |               v
+-----------------------------------------------------------------------------------------------+
|                                 UndineD Go Backend Daemon                                     |
|                                                                                               |
|   +-------------------+    +--------------------+    +---------------+    +---------------+   |
|   | Selective IMAP    |    | Strict SMTP Engine |    | b4 Runner     |    | Patchwork     |   |
|   | Poller (goroutine)|    | (8-bit plain-text) |    | & Git am      |    | Client (v1.4) |   |
|   +---------+---------+    +---------+----------+    +-------+-------+    +-------+-------+   |
|             |                        |                       |                    |           |
|             v                        |                       v                    v           |
|      +---------------+               |             +-----------------+    +---------------+   |
|      | Maildir Store | <-------------+-------------+ lore.kernel.org |    | TODO Mailbox  |   |
|      | (1-mail-1-file)               |             +-----------------+    | Sync Engine   |   |
|      +-------+-------+               |                                    +-------+-------+   |
|              |                       |                                            |           |
|              v                       |                                            v           |
|      +---------------+               |                                    +---------------+   |
|      | SQLite FTS5   | <-------------+------------------------------------+ Review Runner |   |
|      | (Meta/Threads)|                                                    | (agy --print) |   |
|      +-------+-------+                                                    +-------+-------+   |
|              |                                                                    |           |
|       +------+-----------------------+--------------------------------------------+           |
|       |                                        |                                              |
|       v                                        v                                              |
|  +------------------------+             +---------------------------+                         |
|  | REST API Router        |             | MCP Server Engine         |                         |
|  | (go-chi/chi/v5)        |             | (STDIO & SSE transports)  |                         |
|  +-----------+------------+             +-------------+-------------+                         |
+--------------|----------------------------------------|---------------------------------------+
               |                                        |
               v                                        v
     +-------------------+                    +-------------------+
     | Local Web UI      |                    | Antigravity / AI  |
     | (Browser Client)  |                    | (Context & Review)|
     +-------------------+                    +-------------------+
```

---

## **3. Technology Stack & Directory Structure**

* **Language**: Go 1.22+
* **HTTP Routing**: `github.com/go-chi/chi/v5`
* **Database**: SQLite 3 with FTS5 virtual tables and WAL mode (`github.com/mattn/go-sqlite3`)
* **Configuration**: TOML v1.0 (`github.com/pelletier/go-toml/v2`)
* **Email & MIME Parsing**: `github.com/jhillyerd/enmime` & `net/mail`
* **Protocols**:
  * Selective IMAP: `github.com/emersion/go-imap/v2`
  * Strict Plaintext SMTP: Standard `net/smtp` + `crypto/tls`
  * Model Context Protocol: `github.com/mark3labs/mcp-go`
* **Subprocess Orchestration**: `os/exec` for `b4`, `git`, `agy`, and `$EDITOR`

### **Directory Layout**

```
undined/
├── cmd/
│   └── undined/
│       └── main.go                  # CLI entrypoint, flag parsing, daemon bootstrap
├── internal/
│   ├── config/                      # TOML configuration loader and validator
│   ├── constants/                   # Shared mailbox names, flags, headers, default ports
│   ├── security/                    # POSIX 0600 token, DNS rebinding & CSRF protection
│   ├── storage/
│   │   ├── maildir/                 # Maildir read/write/flags/delete
│   │   ├── database/                # SQLite connection, DDL, safe migrations, threading
│   │   └── parser/                  # MIME plain-text extraction and decoding
│   ├── lkml/
│   │   ├── patch/                   # Unified diff parser and paired rows for SideDiff
│   │   ├── quote/                   # LKML quote merger and inline comment insertion
│   │   ├── b4/                      # b4 mbox and git am subprocess wrapper
│   │   └── review/                  # Background AI review runner (agy --print)
│   ├── imap/                        # Selective IMAP polling and header summary fetching
│   ├── smtp/                        # Strict plain-text SMTP transport with X-Mailer
│   ├── patchwork/                   # Patchwork REST API v1.4 client
│   ├── bridge/                      # External terminal editor bridge ($EDITOR)
│   ├── api/                         # REST API handlers and static file serving
│   └── mcp/                         # Model Context Protocol server (STDIO & SSE)
├── frontend/                        # Vanilla JavaScript 3-pane client
├── docs/
│   ├── DESIGN.md                    # System architecture and design documentation
│   ├── REST_API.md                  # REST API interface specification
│   └── AGENT.md                     # AI Agent skills & MCP protocol specification
├── go.mod
├── go.sum
└── Makefile
```

---

## **4. Backend Subsystems & Specifications**

### **4.1 Storage & SQLite Schema (`internal/storage/database`)**

Messages are stored as individual immutable files within Maildir directories (`cur/`, `new/`, `tmp/`). SQLite serves as the high-speed query and thread-resolution index.

```sql
CREATE TABLE IF NOT EXISTS folders (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    name TEXT UNIQUE NOT NULL,
    last_synced_uid INTEGER DEFAULT 0,
    sync_enabled BOOLEAN DEFAULT 1
);

CREATE TABLE IF NOT EXISTS messages (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    message_id TEXT NOT NULL,
    folder_id INTEGER NOT NULL REFERENCES folders(id),
    imap_uid INTEGER,
    file_path TEXT NOT NULL,
    subject TEXT,
    from_name TEXT,
    from_addr TEXT,
    to_addr TEXT,
    cc_addr TEXT,
    date TEXT,
    epoch_timestamp INTEGER,
    in_reply_to TEXT,
    references_header TEXT,
    body_plain TEXT,
    is_patch BOOLEAN DEFAULT 0,
    is_read BOOLEAN DEFAULT 0,
    is_replied BOOLEAN DEFAULT 0,
    has_notes BOOLEAN DEFAULT 0,
    flags TEXT DEFAULT '',
    review_commit_id TEXT,
    created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
    UNIQUE(folder_id, message_id)
);

CREATE TABLE IF NOT EXISTS review_commits (
    message_id TEXT PRIMARY KEY,
    commit_id TEXT NOT NULL,
    created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE IF NOT EXISTS patch_comments (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    message_id TEXT NOT NULL,
    message_db_id INTEGER REFERENCES messages(id) ON DELETE CASCADE,
    file_path TEXT NOT NULL,
    line_no INTEGER NOT NULL,
    side TEXT DEFAULT 'new',
    text TEXT NOT NULL,
    state TEXT DEFAULT 'draft',
    author TEXT DEFAULT 'user',
    created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX IF NOT EXISTS idx_messages_in_reply_to ON messages(in_reply_to);
CREATE INDEX IF NOT EXISTS idx_messages_folder ON messages(folder_id);
CREATE INDEX IF NOT EXISTS idx_messages_epoch ON messages(epoch_timestamp DESC);
CREATE INDEX IF NOT EXISTS idx_messages_msgid ON messages(message_id);
CREATE INDEX IF NOT EXISTS idx_messages_folder_msgid ON messages(folder_id, message_id);
CREATE INDEX IF NOT EXISTS idx_messages_imap_uid ON messages(folder_id, imap_uid);
CREATE INDEX IF NOT EXISTS idx_messages_flags ON messages(flags);
CREATE INDEX IF NOT EXISTS idx_messages_is_replied ON messages(is_replied);
CREATE INDEX IF NOT EXISTS idx_messages_review_commit_id ON messages(review_commit_id);
CREATE INDEX IF NOT EXISTS idx_messages_has_notes ON messages(has_notes);

CREATE INDEX IF NOT EXISTS idx_patch_comments_msgid ON patch_comments(message_id);
CREATE INDEX IF NOT EXISTS idx_patch_comments_msg_db_id ON patch_comments(message_db_id);
CREATE INDEX IF NOT EXISTS idx_patch_comments_state ON patch_comments(state);
CREATE INDEX IF NOT EXISTS idx_patch_comments_file_line ON patch_comments(message_id, file_path, line_no);

CREATE VIRTUAL TABLE IF NOT EXISTS messages_fts USING fts5(
    message_id UNINDEXED,
    subject,
    from_addr,
    body_plain,
    content='messages',
    content_rowid='id'
);

CREATE TRIGGER IF NOT EXISTS messages_ai AFTER INSERT ON messages BEGIN
  INSERT INTO messages_fts(rowid, message_id, subject, from_addr, body_plain)
  VALUES (new.id, new.message_id, new.subject, new.from_addr, new.body_plain);
END;

CREATE TRIGGER IF NOT EXISTS messages_ad AFTER DELETE ON messages BEGIN
  INSERT INTO messages_fts(messages_fts, rowid, message_id, subject, from_addr, body_plain)
  VALUES('delete', old.id, old.message_id, old.subject, old.from_addr, old.body_plain);
END;

CREATE TRIGGER IF NOT EXISTS messages_au AFTER UPDATE ON messages BEGIN
  INSERT INTO messages_fts(messages_fts, rowid, message_id, subject, from_addr, body_plain)
  VALUES('delete', old.id, old.message_id, old.subject, old.from_addr, old.body_plain);
  INSERT INTO messages_fts(rowid, message_id, subject, from_addr, body_plain)
  VALUES (new.id, new.message_id, new.subject, new.from_addr, new.body_plain);
END;
```

#### **Safe Migration Ordering (Rule 2)**
When initializing the database (`db.InitDB()`):
1. Check if table `messages` exists via `SELECT 1 FROM sqlite_master WHERE type='table' AND name='messages'`.
2. If it exists, execute `ALTER TABLE messages ADD COLUMN ...` for missing columns (`imap_uid`, `flags`, `review_commit_id`, `has_notes`, `is_replied`) **before** running index declarations.
3. Check for legacy single-column `UNIQUE(message_id)` constraint and migrate to composite `UNIQUE(folder_id, message_id)` via temporary table migration.
4. Execute `SCHEMA_SQL` to guarantee all indexes and triggers are in place.
5. Seed standard mailboxes: `b4-imports`, `Drafts`, `TODO`, `Sent`, `Trash`, `AI-reviews`, migrating deprecated `Outbox` to `TODO`.

### **4.2 Selective IMAP Poller (`internal/imap`)**
* Connects via SSL/TLS (default port 993).
* Iterates only across mailboxes with `sync_enabled = 1`.
* Executes incremental fetch via `UID SEARCH UID {last_synced_uid + 1}:*`.
* Supports two-tier synchronization:
  1. Header Summary Mode: fetches `RFC822.HEADER` and flags (`\Seen`, `\Answered`, color flags).
  2. Full Sync Mode: fetches full RFC822 bodies on demand.

### **4.3 b4 & Git Integration (`internal/lkml/b4`)**
* Detects missing parent threads by inspecting `In-Reply-To` and `References`.
* Executes `b4 mbox -o - "<cleaned_msgid>"` to fetch entire series from lore.kernel.org.
* Parses mbox stream in-memory and indexes emails directly into `b4-imports`.
* Executes `b4 am` or `git am` to apply patches onto target repositories and records `review_commit_id`.

### **4.4 Diff Parser & Quote Merger (`internal/lkml/patch`, `internal/lkml/quote`)**
* **Diff Parser**: Parses unified diffs into files, hunks, and horizontally aligned paired rows (`left` pre-image, `right` post-image) for SideDiff rendering.
* **Quote Merger**: Follows LKML etiquette:
  1. Inserts `On {date}, {author} <{email}> wrote:`.
  2. Retains 2–3 lines of context around lines with review notes.
  3. Omits unannotated hunks and files entirely.
  4. Prefixes original lines with `> `.
  5. Inserts reviewer notes without indentation, flanked by blank lines.
  6. Appends `.signature` at the end.

### **4.5 Strict Plain-Text SMTP (`internal/smtp`)**
* Enforces strict plaintext delivery:
  * `Content-Type: text/plain; charset=utf-8`
  * `Content-Transfer-Encoding: 8bit`
  * `X-Mailer: UndineD 0.1`
* Preserves tabs and trailing spaces with strict LF line endings.
* Updates parent message in SQLite to `is_replied = 1` and sets `\Answered` flag upon dispatch.

### **4.6 Patchwork Client (`internal/patchwork`)**
* Integrates with `patchwork.kernel.org` via REST API v1.4.
* Syncs delegated patches into the local `TODO` mailbox.
* Supports partial updates (`PATCH /api/patchwork/patches/{id}`) preserving `null` values to unassign fields (Rule 4).

### **4.7 AI Review Runner (`internal/lkml/review`)**
* Spawns `agy --print` (or custom review command) in the background.
* Enforces single-review concurrency lock.
* Persists final report message into `AI-reviews` mailbox with links to original patch.

---

## **5. Multi-User Security & IPC Hardening (`internal/security`)**

1. **POSIX 0600 Secret Token**:
   * Generates a 256-bit cryptographically secure token (`crypto/rand`).
   * Writes to `$XDG_DATA_HOME/undined/auth_token` with mode `0600`.
2. **DNS Rebinding Defense**:
   * Validates `Host` header against `127.0.0.1:<port>` and `localhost:<port>`. Mismatches receive HTTP 403 Forbidden.
3. **CSRF Origin Defense**:
   * On mutating methods (`POST`, `PUT`, `DELETE`, `PATCH`), validates `Origin` or `Referer` against local origin.
4. **Session Cookie & Bearer Token**:
   * Browser bootstrap via `GET /?token=<secret>` sets `HttpOnly; SameSite=Strict` cookie `undine_session`.
   * CLI / MCP clients authenticate via `Authorization: Bearer <secret>`.

---

## **6. Model Context Protocol (MCP) Server (`internal/mcp`)**

Exposes 9 tools matching the `undine-mcp-review` skill:
1. `list_recent_emails`
2. `search_emails`
3. `get_thread_context`
4. `import_patch_series_b4`
5. `create_review_draft`
6. `get_patchwork_patch_info`
7. `list_my_delegated_patches`
8. `update_patchwork_patch`
9. `post_review_comment` (aliases: `post_diff_comment`, `add_diff_comment`)

* **Transports**:
  * **STDIO Mode**: `undined --mcp-stdio`. Standard input/output reserved strictly for JSON-RPC 2.0; logs written to file.
  * **SSE Mode**: HTTP server on port 8081 (`/sse` and `/messages`) protected by Bearer token and Host validation.
* **Access Logging**:
  * Formatted to `~/.cache/undined/undined.log`:
    `YYYY-MM-DD HH:MM:SS [INFO] [undine.mcp] MCP access: <TOOL>(<ARGS>) -> <OUTCOME> (<METRICS>) [<DURATION>ms]`
