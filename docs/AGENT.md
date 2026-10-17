# UndineD AI Agent & Review Skills Specification

This document defines the agent architecture, review skills, Model Context Protocol (MCP) tool contracts, and logging standards for **Project UndineD**.

---

## 1. Overview & Architecture

UndineD is designed from the ground up to empower AI coding agents (such as Google Antigravity, Gemini CLI, Claude, and custom LLM review agents) to interact directly with the Linux kernel email archive, patch database, and review workflow engine.

### Integration Channels

1. **Embedded Model Context Protocol (MCP) Server**:
   Exposes high-level tools over STDIO or Server-Sent Events (SSE) to search mail, inspect diffs, post inline review comments, and generate reply drafts.
2. **Asynchronous Review Runner (`agy --print`)**:
   Invoked by the backend when the user clicks `[ AI review ]` in the Web UI. Executes prompt-only reviews in the background and saves reports to the `AI-reviews` mailbox.

---

## 2. Agent Review Skills

UndineD provides two specialized agent skills located in `.agents/skills/`:

### A. `undine-mcp-review` (Protocol & Tool Contracts)
The technical protocol specification, parameter schemas, return definitions, transport configuration, and diagnostic access logging instructions.
* **Skill File**: `~/.gemini/skills/undine-mcp-review/SKILL.md` (or `.agents/skills/undine-mcp-review/SKILL.md`)

### B. `undine-review` (Review Specialist Heuristics)
Guides AI agents in evaluating Linux kernel patch submissions, verifying coding style, concurrency/locking invariants, error handling, memory safety, and executing a 4-step review lifecycle:

```
[1. Discover] ---> [2. Inspect] ---> [3. Critique & Post] ---> [4. Draft Response]
list_recent_emails  get_thread_context  post_review_comment       create_review_draft
```

---

## 3. MCP Server Transports

UndineD's Go daemon supports two MCP transports:

### 3.1 STDIO Transport (Default)
The client spawns the compiled binary as a subprocess communicating via JSON-RPC 2.0 over standard input and standard output.

* **Command**:
  ```bash
  /path/to/undined-go/bin/undined --mcp-stdio
  ```
* **Standard I/O Invariant**:
  * `stdout` is strictly reserved for newline-delimited JSON-RPC 2.0 messages.
  * Diagnostics and access logs are written to `~/.cache/undined/undined.log`, never to `stdout`.

### 3.2 SSE Transport (HTTP Server-Sent Events)
Runs an HTTP server on port 8081 for networked or containerized clients.

* **Endpoints**:
  * Connection stream: `GET http://<host>:8081/sse`
  * Client messages: `POST http://<host>:8081/messages`
* **Authentication & Host Guard**:
  * Requires `Authorization: Bearer <AUTH_TOKEN>` read from `~/.local/share/undined/auth_token`.
  * Enforces DNS rebinding protection on `Host` header.

---

## 4. MCP Tool Reference

### 4.1 `post_review_comment`
**Aliases**: `post_diff_comment`, `add_diff_comment`

Posts an inline review comment onto a patch email diff at a specified `FILE:LINE` target. Comments are persisted in the SQLite `patch_comments` table and mark the message with `has_notes = 1`.

#### Parameters:
| Parameter | Type | Required | Default | Description |
|---|---|---|---|---|
| `message_id` | `string` | Yes | — | Target patch email Message-ID or numeric DB ID. |
| `target` | `string` | Yes | — | Target coordinate in `FILE:LINE` format (e.g. `kernel/trace/ftrace.c:51`). Paths prefixed with `a/` or `b/` are automatically normalized. |
| `comment` | `string` | Yes | — | The review feedback, critique, or question. |
| `side` | `string` | No | `"new"` | Diff side: `"new"` for additions and context lines; `"old"` for deletions. |
| `state` | `string` | No | `"draft"` | Comment status: `"draft"` or `"submitted"`. Automatically set to `"submitted"` if comment text contains `[ai review]` or `[review]`. |
| `target_message_id` | `string` | No | `""` | Backward-compatible alias for `message_id`. |

#### Returns:
```json
{
  "success": true,
  "comment_id": 42,
  "message_id": "ftrace-patch-1@kernel.org",
  "file": "kernel/trace/ftrace.c",
  "line_no": 51,
  "side": "new",
  "comment": "Check new_code() error return handling.",
  "mapped": true
}
```

*Note*: If `target` points to an existing file in the diff but outside the hunk context window, `mapped` is `false`, but the comment is still persisted as a draft so review feedback is never lost.

---

### 4.2 `list_recent_emails`
Lists recent email summaries, optionally filtered by folder name.

#### Parameters:
| Parameter | Type | Required | Default | Description |
|---|---|---|---|---|
| `folder` | `string` | No | `null` | Folder name filter (e.g. `"b4-imports"`, `"Drafts"`, `"TODO"`). Omit to query all folders. |
| `limit` | `int` | No | `20` | Maximum number of message summaries to return. |

#### Returns:
```json
[
  {
    "id": 102,
    "message_id": "patch.1791745088.git.author@kernel.org",
    "subject": "[PATCH v7 1/2] selftests/ftrace: fix path resolution",
    "from_addr": "developer@kernel.org",
    "from_name": "Developer Name",
    "date": "2026-10-12 03:35:00",
    "is_patch": true,
    "is_read": false
  }
]
```

---

### 4.3 `search_emails`
Performs full-text search across indexed kernel emails using SQLite FTS5.

#### Parameters:
| Parameter | Type | Required | Default | Description |
|---|---|---|---|---|
| `query` | `string` | Yes | — | Search query terms or FTS5 syntax (e.g. `"ftrace AND kprobe"`). |
| `limit` | `int` | No | `20` | Maximum search results to return. |

#### Returns:
Array of matching messages with snippet highlights, subject, sender, and date.

---

### 4.4 `get_thread_context`
Retrieves the complete thread conversation tree and diff content for a given message ID.

#### Parameters:
| Parameter | Type | Required | Default | Description |
|---|---|---|---|---|
| `message_id` | `string` | Yes | — | Canonical Message-ID or numeric DB ID of any message in the target thread. |

#### Returns:
Array of messages ordered by hierarchy, including `body_plain`, `in_reply_to`, `references_header`, and `is_patch`.

---

### 4.5 `create_review_draft`
Aggregates inline review comments and general feedback into a canonical LKML quoted reply email, saving it into the local Maildir `Drafts` folder.

#### Parameters:
| Parameter | Type | Required | Default | Description |
|---|---|---|---|---|
| `target_message_id` | `string` | Yes | — | Message-ID being replied to. |
| `general_feedback` | `string` | Yes | — | High-level review summary or greeting text. |
| `inline_notes` | `array` | Yes | — | Array of note objects: `[{"file": "...", "line_no": 123, "comment": "..."}]`. |

#### Returns:
```json
{
  "success": true,
  "draft_id": 55,
  "file_path": "/home/user/.local/share/undined/maildir/Drafts/cur/1791745088.M12345P6789.host,S=1024:2,D",
  "subject": "Re: [PATCH] ftrace: improve kprobe handling"
}
```

---

### 4.6 `import_patch_series_b4`
Downloads an entire patch series or discussion thread from `lore.kernel.org` via `b4` and imports it into the local Maildir store and SQLite index.

#### Parameters:
| Parameter | Type | Required | Default | Description |
|---|---|---|---|---|
| `message_id_or_url` | `string` | Yes | — | Lore Message-ID or public inbox URL. |

#### Returns:
```json
{
  "success": true,
  "message_id": "20261003140000.12345-1-author@kernel.org",
  "imported_count": 4,
  "indexed_ids": [101, 102, 103, 104]
}
```

---

### 4.7 `get_patchwork_patch_info`
Queries `patchwork.kernel.org` for metadata associated with an email:
* **Parameters**: `message_id: string`
* **Returns**: `{"id": int, "state": string, "is_delegated_to_me": bool, "checks": array, "series": object}`

---

### 4.8 `list_my_delegated_patches`
Lists patches assigned to the current maintainer on Patchwork:
* **Parameters**: `state: string (optional)`, `project: string (optional)`, `limit: int = 20`
* **Returns**: Array of delegated patch records.

---

### 4.9 `update_patchwork_patch`
Updates review state or self-delegates a patch:
* **Parameters**: `patch_id: int`, `state: string (optional)`, `delegate_to_me: bool = false`
* **Returns**: Patchwork API response object.

---

## 5. MCP Access Logging & Audit Trail

UndineD records structured access logs for every MCP tool invocation:
* **File**: `~/.cache/undined/undined.log` (or `log_file` specified in `config.toml`)
* **Logger Prefix**: `[undine.mcp]`
* **Format**:
  ```
  YYYY-MM-DD HH:MM:SS [LEVEL] [undine.mcp] MCP access: <TOOL_NAME>(<ARGS>) -> <OUTCOME> (<METRICS>) [<DURATION>ms]
  ```

### Example Log Entries:
```log
2026-10-17 18:00:00 [INFO] [undine.mcp] MCP access: list_recent_emails(folder="b4-imports", limit=20) -> 12 messages (2.4ms)
2026-10-17 18:00:02 [INFO] [undine.mcp] MCP access: get_thread_context(message_id="patch-1@kernel.org") -> 3 messages in thread (4.1ms)
2026-10-17 18:00:05 [INFO] [undine.mcp] MCP access: post_review_comment("patch-1@kernel.org", target="kernel/trace/ftrace.c:51", side="new") -> SUCCESS (comment_id=42, mapped=true, state="draft") (3.8ms)
```

---

## 6. Registering UndineD Go with agy CLI

Register the compiled Go binary into `agy`:

```bash
agy mcp add undine /path/to/undined-go/bin/undined --mcp-stdio
```
