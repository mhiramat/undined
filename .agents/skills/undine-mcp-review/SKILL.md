---
name: undine-mcp-review
description: Comprehensive API reference, schemas, transport configuration, and access logging for Undine's Model Context Protocol (MCP) review tools.
---

# UndineD MCP Review API Specification

This skill serves as the independent technical reference manual for UndineD's embedded Model Context Protocol (MCP) review APIs. It provides detailed tool signatures, parameter definitions, return schemas, transport configuration, and diagnostic access logging instructions.

---

## 1. Overview & Architecture

UndineD exposes an MCP server named `undine` that allows AI agents (such as Google Antigravity, Gemini CLI, Claude, and custom LLM review agents) to interact programmatically with the local Linux kernel email archive, patch database, and review workflow engine.

### Relationship to `undine-review`
- **`undine-mcp-review` (This Skill)**: The technical protocol specification, tool API contracts, schema definitions, transport setup, and access logging documentation.
- **`undine-review`**: The domain specialist skill focusing on Linux kernel patch review heuristics, coding style, locking/concurrency checks, memory safety rules, and oneshot review execution.

---

## 2. Server Transports & Connections

UndineD supports two transport modes for MCP clients:

### A. STDIO Transport (Default)
In STDIO mode, the client spawns Undine's MCP server as a subprocess and communicates via JSON-RPC 2.0 over standard input and standard output.

- **Command**:
  ```bash
  /path/to/undined/bin/undined --mcp-stdio
  ```
  *(or `undined --mcp-stdio` if installed on `PATH`)*
- **Standard I/O Invariant**:
  - `stdout` is strictly reserved for newline-delimited JSON-RPC messages.
  - Diagnostics, status traces, and access logs are written to the rotating log file (`~/.cache/undined/undined.log`) and stderr, never to `stdout`.

### B. SSE (Server-Sent Events / HTTP) Transport
In SSE mode, UndineD serves an embedded HTTP endpoint over its standard API port or configured SSE port.

- **Configuration** (`~/.config/undined/config.toml`):
  ```toml
  [mcp]
  transport = "stdio"
  sse_port = 8081
  ```
- **Security & Authentication**:
  - Requires a valid Bearer token header:
    ```http
    Authorization: Bearer <AUTH_TOKEN>
    ```
    The token is read from `~/.local/share/undined/auth_token` (POSIX `0600`).
  - Includes DNS rebinding and CSRF Origin protection: Host and Origin headers are strictly validated against configured host/port combinations.

---

## 3. Tool API Reference

### 3.1 `post_review_comment`
**Aliases**: `post_diff_comment`, `add_diff_comment`

Posts an inline review comment onto a patch email diff at a specified `FILE:LINE` target. Comments are persisted in the SQLite `patch_comments` table and mark the email with `has_notes = 1`.

#### Parameters:
| Parameter | Type | Required | Default | Description |
|---|---|---|---|---|
| `message_id` | `str` | Yes | — | Target patch email Message-ID (e.g. `<20261003...-1-author@kernel.org>`) or numeric DB ID. |
| `target` | `str` | Yes | — | Target coordinate in `FILE:LINE` format (e.g. `kernel/trace/ftrace.c:51`). To comment on lines in the message body / commit description, specify `DESCRIPTION:LINE` (e.g. `DESCRIPTION:12`). Paths prefixed with `a/` or `b/` are automatically normalized. |
| `comment` | `str` | Yes | — | The review feedback, critique, or question text. |
| `side` | `str` | No | `"new"` | Diff side: `"new"` (or `"right"`) for additions/post-image and context lines; `"old"` (or `"left"`) for deletions/pre-image. |
| `state` | `str` | No | `"draft"` | Comment status: `"draft"` or `"submitted"`. Automatically set to `"submitted"` if comment text contains `[ai review]` or `[review]`. |
| `target_message_id` | `str` | No | `None` | Backward-compatible alias for `message_id`. |

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

#### Mapping Behavior (`mapped` field):
- `mapped: true`: When targeting a diff file, the specified `line_no` successfully matched an active row within the patch diff's unified hunks. When targeting `DESCRIPTION:LINE`, `mapped` is `true` if `line_no` is within the line count of the commit description / message body preamble. The comment appears directly attached to that line in Undine's Side-by-Side viewer.
- `mapped: false`: The file exists in the patch (or the line is beyond the description), but `line_no` falls outside the active context window. Undine still persists the note as a draft comment so reviewer feedback is never discarded.

---

### 3.2 `get_thread_context`
Retrieves the complete thread conversation tree and diff content for a given message ID.

#### Parameters:
| Parameter | Type | Required | Default | Description |
|---|---|---|---|---|
| `message_id` | `str` | Yes | — | Canonical Message-ID or numeric DB ID of any message in the target thread. |

#### Returns:
Array of messages ordered by hierarchy:
```json
[
  {
    "id": 101,
    "message_id": "cover.1791745088.git.author@kernel.org",
    "subject": "[PATCH v7 0/2] selftests/ftrace improvements",
    "from_name": "Developer Name",
    "from_addr": "developer@kernel.org",
    "date": "2026-10-12 03:30:00",
    "in_reply_to": null,
    "references": null,
    "body_plain": "This series improves test coverage...",
    "is_patch": false
  },
  {
    "id": 102,
    "message_id": "patch.1791745088.git.author@kernel.org",
    "subject": "[PATCH v7 1/2] selftests/ftrace: fix path resolution",
    "from_name": "Developer Name",
    "from_addr": "developer@kernel.org",
    "date": "2026-10-12 03:35:00",
    "in_reply_to": "cover.1791745088.git.author@kernel.org",
    "references": "cover.1791745088.git.author@kernel.org",
    "body_plain": "diff --git a/tools/... b/tools/...",
    "is_patch": true
  }
]
```

---

### 3.3 `list_recent_emails`
Lists recent email summaries, optionally filtered by folder name.

#### Parameters:
| Parameter | Type | Required | Default | Description |
|---|---|---|---|---|
| `folder` | `str` | No | `null` | Folder name to filter by (e.g. `"b4-imports"`, `"Drafts"`, `"TODO"`, `"Sent"`). Omit to query across all folders. |
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

### 3.4 `search_emails`
Performs full-text search across indexed kernel emails using SQLite FTS5.

#### Parameters:
| Parameter | Type | Required | Default | Description |
|---|---|---|---|---|
| `query` | `str` | Yes | — | Search query terms or FTS5 syntax (e.g. `"ftrace AND kprobe"`). |
| `limit` | `int` | No | `20` | Maximum search results to return. |

#### Returns:
Array of matching messages containing matched headline snippets, subject, and sender metadata.

---

### 3.5 `write_draft_message`
**Aliases**: `write_draft`, `save_draft`

Writes a draft email message into the local Maildir `Drafts` folder and indexes it in SQLite so the user can review, edit, and send it from Undine's UI or external editor.

Supports drafting replies to existing messages (auto-deriving `to`, `cc`, `subject`, `in_reply_to`, and `references`) as well as standalone new messages. Optionally merges structured `inline_notes` into LKML quote format if supplied.

#### Parameters:
| Parameter | Type | Required | Default | Description |
|---|---|---|---|---|
| `body` | `str` | Yes | — | Plain-text draft email body. (Aliases: `body_text`, `general_feedback`). |
| `in_reply_to` | `str` | No | `null` | Message-ID (e.g. `<20261003...-1-author@kernel.org>`) or DB ID being replied to. (Aliases: `target_message_id`, `reply_to`). |
| `to` | `str` | No | Auto | Recipient email address(es). Automatically derived from parent email if `in_reply_to` is given. (Alias: `to_addr`). |
| `subject` | `str` | No | Auto | Subject line. Automatically defaults to `"Re: <parent subject>"` if `in_reply_to` is given. |
| `cc` | `str` | No | Auto | CC email address(es). Automatically derived if `in_reply_to` is given. (Alias: `cc_addr`). |
| `references` | `str` | No | Auto | References header string. Automatically derived if `in_reply_to` is given. |
| `inline_notes` | `list[dict]` | No | `null` | Optional array of structured review comments `[{"file": "...", "line_no": 123, "comment": "..."}]` to merge into LKML quote format. |
| `append_signature` | `bool` | No | `false` | Whether to append the user's configured signature from `~/.signature`. |

#### Returns:
```json
{
  "success": true,
  "draft_id": 55,
  "message_id": "<undine-1791745088-abc@undined.local>",
  "subject": "Re: [PATCH] ftrace: improve kprobe handling",
  "to": "author@kernel.org",
  "cc": "linux-trace-kernel@vger.kernel.org",
  "file_path": ".../maildir/Drafts/cur/1791745088.M12345P6789.host,S=1024:2,D"
}
```

---

### 3.6 `create_review_draft`
Aggregates inline review comments and general feedback into a canonical LKML quoted reply email, saving it into the local Maildir `Drafts` folder. (Convenience wrapper around `write_draft_message`).

#### Parameters:
| Parameter | Type | Required | Default | Description |
|---|---|---|---|---|
| `target_message_id` | `str` | Yes | — | Message-ID being replied to. |
| `general_feedback` | `str` | Yes | — | High-level review summary or greeting text. |
| `inline_notes` | `list[dict]` | No | `[]` | Array of note objects: `[{"file": "...", "line_no": 123, "comment": "..."}]`. Set `"file": "DESCRIPTION"` to attach comments to lines of the commit description / message body. |

#### Returns:
```json
{
  "success": true,
  "draft_id": 55,
  "file_path": ".../maildir/Drafts/cur/1791745088.M12345P6789.host,S=1024:2,D",
  "subject": "Re: [PATCH] ftrace: improve kprobe handling"
}
```

---

### 3.7 `import_patch_series_b4`
Downloads an entire patch series or discussion thread from `lore.kernel.org` via `b4` and imports it into the local Maildir store and SQLite index.

#### Parameters:
| Parameter | Type | Required | Default | Description |
|---|---|---|---|---|
| `message_id_or_url` | `str` | Yes | — | Lore Message-ID or public inbox URL. |

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

### 3.7 Patchwork Integration Tools

#### `get_patchwork_patch_info`
Queries `patchwork.kernel.org` for metadata associated with an email:
- **Parameters**: `message_id: str`
- **Returns**: `{"id": int, "state": str, "is_delegated_to_me": bool, "checks": list, "series": dict}`

#### `list_my_delegated_patches`
Lists patches assigned to the current maintainer on Patchwork:
- **Parameters**: `state: Optional[str]`, `project: Optional[str]`, `limit: int = 20`
- **Returns**: Array of delegated patch records.

#### `update_patchwork_patch`
Updates review state or self-delegates a patch:
- **Parameters**: `patch_id: int`, `state: Optional[str]`, `delegate_to_me: bool = False`
- **Returns**: Patchwork API response object.

---

## 4. MCP Access Logging & Audit Trail

UndineD records structured access logs for every MCP tool invocation to enable real-time debugging, performance profiling, and review activity audits.

### Log Destination & Logger Name
- **File**: `~/.cache/undined/undined.log` (or `log_file` specified in `config.toml`)
- **Logger Name**: `undine.mcp`
- **Format**:
  ```
  YYYY-MM-DD HH:MM:SS [LEVEL] [undine.mcp] MCP access: <TOOL_NAME>(<ARGS>) -> <OUTCOME> (<METRICS>) [<DURATION>ms]
  ```

### Example Log Entries:
```log
2026-10-12 21:10:00 [INFO] [undine.mcp] MCP access: list_recent_emails(folder='b4-imports', limit=20) -> 12 messages (3.4ms)
2026-10-12 21:10:02 [INFO] [undine.mcp] MCP access: get_thread_context(message_id='patch-1@kernel.org') -> 3 messages in thread (6.1ms)
2026-10-12 21:10:05 [INFO] [undine.mcp] MCP access: post_review_comment('patch-1@kernel.org', target='kernel/trace/ftrace.c:51', side='new') -> SUCCESS (comment_id=42, mapped=True, state='draft') (4.8ms)
2026-10-12 21:10:08 [WARNING] [undine.mcp] MCP access: post_review_comment('patch-1@kernel.org', target='bad_target') -> REJECTED (Invalid target format) (0.8ms)
```

### Monitoring MCP Access in Real Time:
```bash
tail -f ~/.cache/undined/undined.log | grep --line-buffered "\[undine.mcp\]"
```

---

## 5. Client Configuration Examples

### agy CLI Configuration
Register Undine's MCP server with `agy`:
```bash
agy mcp add undine -- /path/to/undined/bin/undined --mcp-stdio
```

### Antigravity / Claude Desktop Configuration (`mcp_config.json`)
```json
{
  "mcpServers": {
    "undine": {
      "command": "/path/to/undined/bin/undined",
      "args": ["--mcp-stdio"]
    }
  }
}
```
