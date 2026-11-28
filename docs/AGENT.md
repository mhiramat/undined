# UndineD AI Agent & Review Skills Specification

This document defines the agent architecture, review skills, Model Context Protocol (MCP) tool contracts, and logging standards for **Project UndineD**.

---

## 1. Overview & Architecture

UndineD is designed from the ground up to empower AI coding agents (such as Google Antigravity, Gemini CLI, Claude, and custom LLM review agents) to interact directly with the Linux kernel email archive, patch database, and review workflow engine.

### Integration Channels

1. **Embedded Model Context Protocol (MCP) Server**:
   Exposes high-level tools over STDIO or Server-Sent Events (SSE) to search mail, aggregate threads/series, manage color/custom tags, inspect locally cached diffs, post inline review comments, export `git am`-ready mbox files, and stage plain-text drafts (standalone or threaded patch series). **MCP tools never transmit email over SMTP, never delete/expunge or move mail between folders, and never issue on-demand IMAP `FETCH` calls**; human review and transmission happen exclusively in the Web UI.
2. **Asynchronous Review Runner (`agy --print`)**:
   Invoked by the backend when the user clicks `[ AI review ]` in the Web UI. Executes prompt-only reviews in the background and saves reports to the `AI-reviews` mailbox.

---

## 2. Agent Review Skills

UndineD provides two specialized agent skills located in `.agents/skills/`:

### A. `undine-mcp-review` (Protocol & Tool Contracts)
The technical protocol specification, parameter schemas, return definitions, transport configuration, and diagnostic access logging instructions.
* **Skill File**: `~/.gemini/config/skills/undine-mcp-review/SKILL.md` (or `.agents/skills/undine-mcp-review/SKILL.md`)

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
  /path/to/undined/bin/undined --mcp-stdio
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
| `state` | `string` | No | `"draft"` | Comment status: `"draft"` or `"submitted"`. |
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
Lists recent email summaries with optional folder, pagination, sender exclusion, tag, and patch filters.

#### Parameters:
| Parameter | Type | Required | Default | Description |
|---|---|---|---|---|
| `folder` | `string` | No | `null` | Folder name filter (e.g. `"b4-imports"`, `"Drafts"`, `"TODO"`). Omit to query all folders. |
| `limit` | `int` | No | `20` | Maximum number of message summaries to return. |
| `offset` | `int` | No | `0` | Pagination offset. |
| `exclude_from` | `string[]` | No | `[]` | Sender addresses to exclude. Special value `"self"` expands to configured user identity emails. |
| `has_tags` | `string[]` | No | `[]` | Return only messages carrying at least one of these tags. |
| `without_tags` | `string[]` | No | `[]` | Exclude messages carrying any of these tags. |
| `is_patch` | `bool` | No | `null` | Optional filter by patch status (`true` / `false`). |

#### Returns:
```json
[
  {
    "id": 102,
    "message_id": "patch.1791745088.git.author@kernel.org",
    "subject": "[PATCH v7 1/2] selftests/ftrace: fix path resolution",
    "from_addr": "developer@kernel.org",
    "from_name": "Kernel Developer",
    "date": "2026-10-12 03:35:00",
    "is_patch": true,
    "is_read": false,
    "tags": ["$cl_1"],
    "thread_id": "thr_9f2a1c4e"
  }
]
```

---

### 4.3 `search_emails`
Performs full-text search across indexed kernel emails using SQLite FTS5 with optional folder and tag filtering.

#### Parameters:
| Parameter | Type | Required | Default | Description |
|---|---|---|---|---|
| `query` | `string` | Yes | — | Search query terms or FTS5 syntax (e.g. `"ftrace AND kprobe"`). |
| `limit` | `int` | No | `20` | Maximum search results to return. |
| `folder` | `string` | No | `null` | Optional folder name filter (e.g. `"TODO"`). |
| `has_tags` | `string[]` | No | `[]` | Return only matching messages with at least one of these tags. |
| `without_tags` | `string[]` | No | `[]` | Exclude matching messages with any of these tags. |

#### Returns:
Array of matching messages with `folder_name`, `tags`, snippet highlights, subject, sender, and date.

---

### 4.4 `get_email` (alias: `get_message`)
Retrieves metadata, tags, trailers, diffstat, attachments, and locally cached plaintext body for a message by Message-ID or numeric DB ID. Strictly local-cache-only (never triggers IMAP `FETCH`).

#### Parameters:
| Parameter | Type | Required | Default | Description |
|---|---|---|---|---|
| `message_id` | `string` | Yes | — | Canonical Message-ID (with or without `<...>`) or numeric DB ID (alias: `id`). |
| `include_diff` | `bool` | No | `true` | If `false`, strips unified diff hunks (`diff --git ...`) from `body_plain`, preserving commit log, trailers, and diffstat. |

#### Returns:
Includes all message metadata plus `tags: string[]`, `body_hydrated: bool`, `trailers: map[string][]string` (`Fixes`, `Reviewed-by`, `Acked-by`, `Reported-by`, `Cc-stable`, etc.), `diffstat: string`, and `attachments`.

---

### 4.5 `get_thread_context`
Retrieves the thread conversation tree for a given message ID using locally cached bodies (no on-demand IMAP `FETCH`).

#### Parameters:
| Parameter | Type | Required | Default | Description |
|---|---|---|---|---|
| `message_id` | `string` | Yes | — | Canonical Message-ID or numeric DB ID of any message in the target thread. |
| `include_diff` | `bool` | No | `true` | If `false`, strips unified diff hunks to cut triage token usage by ~80–90%. |
| `deduplicate` | `bool` | No | `true` | If `true`, merges duplicate copies of the same `Message-ID` across folders, preferring locally hydrated copies and returning all folders in `folders` and `folder_ids`. |

#### Returns:
Array of messages ordered by hierarchy, including `body_plain`, `body_hydrated`, `tags`, `folders`, `folder_ids`, `in_reply_to`, `references`, and `is_patch`.

---

### 4.6 `list_threads`
Aggregates messages in a folder into threads / patch series with cursor pagination and triage metadata.

#### Parameters:
| Parameter | Type | Required | Default | Description |
|---|---|---|---|---|
| `folder` | `string` | No | `null` | Folder name to scope threads (e.g. `"TODO"`). |
| `limit` | `int` | No | `30` | Maximum threads per page (`1`–`200`). |
| `cursor` | `string` | No | `null` | Opaque pagination cursor from the previous response's `next_cursor`. |
| `exclude_from` | `string[]` | No | `[]` | Thread root author addresses to exclude (`"self"` expands to configured user identity emails). |
| `patches_only` | `bool` | No | `false` | If `true`, include only threads with at least one patch in the target folder. |
| `has_tags` | `string[]` | No | `[]` | Return only threads carrying at least one of these tags. |
| `without_tags` | `string[]` | No | `[]` | Exclude threads carrying any of these tags (e.g. `["$cl_1","$cl_2","$cl_3","$cl_5"]`). |
| `sort` | `string` | No | `"latest_date_desc"` | `"latest_date_desc"` or `"latest_date_asc"`. |

#### Returns:
```json
{
  "total_threads": 22,
  "returned": 22,
  "next_cursor": null,
  "threads": [
    {
      "thread_id": "thr_9f2a1c4e",
      "root_message_id": "cover.1794746252.git.author@example.com",
      "subject": "[PATCH v4 0/3] riscv: probes: reject probes inside LR/SC",
      "author_name": "Kernel Developer",
      "author_addr": "dev@example.com",
      "latest_date": "2026-11-18 17:43:06 +0200",
      "patch_count_in_folder": 3,
      "total_messages_in_thread": 7,
      "reply_count": 3,
      "folder_message_ids": [13094, 13095, 13093],
      "tags": [],
      "tags_consistent": true,
      "has_my_reply": false,
      "has_unhydrated_replies": true
    }
  ]
}
```

---

### 4.7 `update_email_tags`
Adds, removes, or replaces UndineD / IMAP keyword tags (e.g. `$cl_1`–`$cl_3`, `$cl_5`) on existing emails or threads without sending, moving, or deleting mail. Uses generic client-driven tag transitions (mutual exclusion between priority/done tags is specified explicitly via `add_tags` + `remove_tags`).

#### Parameters:
| Parameter | Type | Required | Default | Description |
|---|---|---|---|---|
| `message_ids` | `string[]` | Yes | — | `1`–`100` target Message-IDs (with or without `<...>`) or numeric DB IDs. |
| `add_tags` | `string[]` | No | `[]` | RFC 3501 keyword tags to add (`[A-Za-z0-9$_\-.]`, 1–64 chars). Mutually exclusive with `replace_tags`. |
| `remove_tags` | `string[]` | No | `[]` | Tags to remove. Cannot overlap `add_tags` or combine with `replace_tags`. |
| `replace_tags` | `string[]` | No | `null` | If non-null, replaces the entire user-tag set (use `[]` to clear all user tags). |
| `scope` | `string` | No | `"message"` | `"message"` or `"thread"` (expands across all messages in the same thread). |
| `folder` | `string` | No | `null` | Optional folder guard (e.g. `"TODO"`). Default `null` updates **all** folder copies sharing the target `Message-ID`(s). |
| `dry_run` | `bool` | No | `false` | Validates targets and returns planned before/after tag diffs without mutating SQLite, `imap_sync_queue`, or SSE. |

#### Validation & Sync Behavior:
- Rejects IMAP system flags (`\Deleted`, `\Seen`, etc.), whitespace, control characters, wildcards (`*`, `%`), >64-char tags, >32 tags/email, `replace_tags` combined with `add_tags`/`remove_tags`, and add/remove overlap with JSON-RPC `-32602`.
- Executes all DB updates (`messages.flags` + `email_tags`) in a single SQLite transaction, enqueues remote IMAP folder copies (those with valid `imap_uid`) into `imap_sync_queue` for background `UID STORE +/-FLAGS.SILENT`, and publishes `email_tags_updated` over SSE.

---

### 4.8 `list_tags`
Lists configured UndineD color labels (ordered by priority rank) and known custom tags with optional folder-scoped usage counts.
* **Parameters**: `folder: string (optional, default null)`
* **Returns**: `{"color_labels": [{"tag", "display_name", "color", "count"}], "custom_tags": [{"tag", "count"}]}`

---

### 4.9 `get_user_identity`
Returns the configured user's display name, primary email, and deduplicated email aliases for self-authorship filtering (never returns tokens or passwords).
* **Parameters**: *(none)*
* **Returns**: `{"display_name": string, "primary_email": string, "aliases": string[]}`

---

### 4.10 `create_review_draft`
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
  "file_path": ".../maildir/Drafts/cur/1791745088.M12345P6789.host,S=1024:2,D",
  "subject": "Re: [PATCH] ftrace: improve kprobe handling"
}
```

---

### 4.11 `create_mail_draft`
Stages a standalone strict 8-bit UTF-8 plain-text email draft into Maildir `Drafts` without requiring a `target_message_id` reply parent (suitable for Pull Requests and new threads). Preserves raw `\t` tabs verbatim and never invokes SMTP.

#### Parameters:
| Parameter | Type | Required | Default | Description |
|---|---|---|---|---|
| `to` | `string[]` | Yes | — | Recipient email addresses (`Name <addr>` or `addr`). |
| `cc` | `string[]` | No | `[]` | Optional carbon-copy recipients. |
| `subject` | `string` | Yes | — | Email subject line. |
| `body` | `string` | Yes | — | Strict plain-text UTF-8 body (tabs and newlines preserved verbatim). |
| `in_reply_to` | `string` | No | `""` | Optional parent `Message-ID`. |
| `references` | `string[]` | No | `[]` | Optional `References` chain. |
| `extra_headers` | `object` | No | `{}` | Optional allowlisted `X-*` custom headers (excluding `X-Mailer` and `X-Undine-Series-*`). |
| `tags` | `string[]` | No | `[]` | Optional UndineD color/custom tags to attach. |

#### Returns:
```json
{
  "draft_id": "56",
  "message_id": "<20261123112000.98412-1-dev@kernel.org>",
  "maildir_path": ".../maildir/Drafts/cur/...",
  "web_url": "http://127.0.0.1:8080/#drafts/56"
}
```

---

### 4.12 `stage_patch_series`
Stages a threaded multi-email patch series (optional `0/N` cover letter + `1/N..N/N` patches) into Maildir `Drafts` and SQLite (`patch_series_drafts` / `patch_series_items`), emitting a `series_draft_updated` SSE event when connected to an Event Hub. Never invokes SMTP.

#### Parameters:
| Parameter | Type | Required | Default | Description |
|---|---|---|---|---|
| `series_key` | `string` | Yes | — | Stable logical key (e.g. `topic/fprobe-cleanup:v2`) for idempotency. |
| `branch` | `string` | No | `""` | Optional source Git branch name. |
| `version` | `int` | No | `1` | Series version number. |
| `to` | `string[]` | Yes | — | Shared `To` recipients for the series. |
| `cc` | `string[]` | No | `[]` | Optional shared `Cc` recipients. |
| `in_reply_to` | `string` | No | `""` | Optional external thread `Message-ID`. |
| `base_commit` | `string` | No | `""` | Optional upstream base commit SHA. |
| `replace_existing` | `bool` | No | `false` | Replace an existing unsent series with the same `series_key` (refused if any item was already sent). |
| `messages` | `array` | Yes | — | Ordered `SeriesMessage` objects (`index`, `is_cover_letter`, `subject`, `body`, optional `commit_sha`, `extra_to`, `extra_cc`). |

#### Returns:
```json
{
  "series_id": "ser_...",
  "series_key": "topic/fprobe-cleanup:v2",
  "message_count": 3,
  "root_message_id": "<20261123112500.41022-1-dev@kernel.org>",
  "items": [
    { "index": 0, "draft_id": "57", "message_id": "<...-1-...>", "status": "draft" }
  ],
  "web_url": "http://127.0.0.1:8080/#series/ser_..."
}
```

---

### 4.13 `export_thread_mbox`
Exports a local thread or patch series from Maildir/SQLite as a `git am`-ready mbox file.

#### Parameters:
| Parameter | Type | Required | Default | Description |
|---|---|---|---|---|
| `message_id` | `string` | Yes | — | Any Message-ID or DB ID in the target thread. |
| `output_path` | `string` | No | Temp file | Optional output path restricted to allowed temp/cache/exports directories (rejects symlinks and path escapes). |
| `patches_only` | `bool` | No | `true` | Filters to patch messages ordered by `[PATCH x/N]` and attaches deduplicated review trailers collected from thread replies. |

#### Returns:
```json
{
  "mbox_path": "/tmp/undined-export-xxxx.mbx",
  "patch_count": 3,
  "base_commit": "a1b2c3d4e5f6...",
  "root_message_id": "<...>"
}
```

---

### 4.14 `import_patch_series_b4`
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

### 4.15 `get_patchwork_patch_info`
Queries `patchwork.kernel.org` for metadata associated with an email:
* **Parameters**: `message_id: string`
* **Returns**: `{"id": int, "state": string, "is_delegated_to_me": bool, "checks": array, "series": object}`

---

### 4.16 `list_my_delegated_patches`
Lists patches assigned to the current maintainer on Patchwork:
* **Parameters**: `state: string (optional)`, `project: string (optional)`, `limit: int = 20`
* **Returns**: Array of delegated patch records.

---

### 4.17 `update_patchwork_patch`
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
2026-10-17 18:00:00 [INFO] [undine.mcp] MCP access: list_recent_emails(folder="b4-imports", limit=20, offset=0) -> 12 messages (2.4ms)
2026-10-17 18:00:01 [INFO] [undine.mcp] MCP access: list_threads(folder='TODO', exclude_from=['self'], without_tags=['$cl_1','$cl_2','$cl_3','$cl_5']) -> 22 threads (8.2ms)
2026-10-17 18:00:02 [INFO] [undine.mcp] MCP access: get_thread_context(message_id="patch-1@kernel.org", include_diff=false, deduplicate=true) -> 3 messages in thread (4.1ms)
2026-10-17 18:00:04 [INFO] [undine.mcp] MCP access: update_email_tags(ids=1, scope='thread', folder=None, add=['$cl_1'], remove=['$cl_2','$cl_3','$cl_5'], dry_run=false) -> SUCCESS (updated=6, imap_sync_queued=3) (4.5ms)
2026-10-17 18:00:05 [INFO] [undine.mcp] MCP access: post_review_comment("patch-1@kernel.org", target="kernel/trace/ftrace.c:51", side="new") -> SUCCESS (comment_id=42, mapped=true, state="draft") (3.8ms)
```

---

## 6. Registering UndineD Go with agy CLI

Register the compiled Go binary into `agy`:

```bash
agy mcp add undine /path/to/undined/bin/undined --mcp-stdio
```
