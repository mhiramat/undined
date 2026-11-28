---
name: undine-mcp-review
description: Comprehensive API reference, schemas, transport configuration, and access logging for Undine's Model Context Protocol (MCP) review tools.
---

# UndineD MCP Review API Specification

This skill serves as the independent technical reference manual for UndineD's embedded Model Context Protocol (MCP) review APIs. It provides detailed tool signatures, parameter definitions, return schemas, transport configuration, and diagnostic access logging instructions.

---

## 1. Overview & Architecture

UndineD exposes an MCP server named `undine` that allows AI agents (such as Google Antigravity, Gemini CLI, Claude, and custom LLM review agents) to interact programmatically with the local Linux kernel email archive, patch database, and review workflow engine.

### Core Safety Invariants: Zero SMTP Transmission, Zero Deletion/Move & Local-Cache-Only
- **MCP tools MUST NEVER send email over SMTP.**
- **MCP tools MUST NEVER delete/expunge emails or move them between folders.** Processed/superseded `TODO` items are marked via `$cl_5` tags rather than moved.
- **Local-Cache-Only reads**: MCP read tools (`get_email`, `get_thread_context`, `list_threads`) MUST NEVER trigger on-demand IMAP `FETCH`. Uncached IMAP messages return `body_hydrated: false`.
- Agents may only read local archives, export `git am`-ready mbox files, post inline review comments, manage keyword tags (`update_email_tags`), update Patchwork metadata, and stage strict plain-text drafts into Maildir `Drafts` (and SQLite `patch_series_drafts` / `patch_series_items`).
- Final human review, editing, and SMTP transmission happen exclusively through the UndineD Web UI.

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
| `state` | `str` | No | `"draft"` | Comment status: `"draft"` or `"submitted"`. |
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

### 3.2 `get_email`
**Aliases**: `get_message`, `fetch_email`, `read_email`

Finds a specific email by Message-ID (or database ID) and retrieves its metadata, tags, trailers, diffstat, and locally cached plaintext body. Strictly local-cache-only: reads local Maildir file if present, never issues on-demand IMAP `FETCH`.

#### Parameters:
| Parameter | Type | Required | Default | Description |
|---|---|---|---|---|
| `message_id` | `str` | Yes | — | Canonical Message-ID (with or without angle brackets `<...>`), or numeric database ID. |
| `id` | `str` | No | `None` | Alias for `message_id`. |
| `include_diff` | `bool` | No | `true` | If `false`, strips unified diff hunks (`diff --git ...`) from `body_plain`, returning commit message, trailers, and diffstat only. |

#### Returns:
```json
{
  "found": true,
  "id": 102,
  "message_id": "patch.1791745088.git.author@kernel.org",
  "folder_name": "b4-imports",
  "subject": "[PATCH v7 1/2] selftests/ftrace: fix path resolution",
  "from_name": "Kernel Developer",
  "from_addr": "dev@kernel.org",
  "to_addr": "linux-kernel@vger.kernel.org",
  "cc_addr": "maintainer@kernel.org",
  "date": "2026-10-12 03:35:00",
  "in_reply_to": "cover.1791745088.git.author@kernel.org",
  "references": "cover.1791745088.git.author@kernel.org",
  "body_plain": "diff --git a/tools/... b/tools/...\n...",
  "body_hydrated": true,
  "is_patch": true,
  "is_read": true,
  "is_replied": false,
  "has_notes": false,
  "file_path": ".../maildir/b4-imports/cur/...",
  "tags": ["$cl_1"],
  "trailers": { "Fixes": ["123456789abc (\"prev commit\")"] },
  "diffstat": " tools/testing/selftests/ftrace/test | 2 +-\n 1 file changed, 1 insertion(+), 1 deletion(-)"
}
```

---

### 3.3 `get_thread_context`
Retrieves the thread conversation tree for a given message ID using locally cached bodies (no on-demand IMAP `FETCH`).

#### Parameters:
| Parameter | Type | Required | Default | Description |
|---|---|---|---|---|
| `message_id` | `str` | Yes | — | Canonical Message-ID (with or without `<...>`) or numeric DB ID of any message in the target thread. |
| `include_diff` | `bool` | No | `true` | If `false`, strips unified diff hunks from `body_plain` while keeping commit logs, trailers, and diffstat. |
| `deduplicate` | `bool` | No | `true` | If `true`, merges duplicate copies of the same `Message-ID` across folders, preferring hydrated copies and returning all folder names/IDs in `folders` and `folder_ids`. |

#### Returns:
Array of messages ordered by hierarchy:
```json
[
  {
    "id": 101,
    "message_id": "cover.1791745088.git.author@kernel.org",
    "folder_name": "TODO",
    "folders": ["TODO", "kernel.org/maintainer"],
    "folder_ids": [3, 7],
    "subject": "[PATCH v7 0/2] selftests/ftrace improvements",
    "from_name": "Kernel Developer",
    "from_addr": "dev@kernel.org",
    "date": "2026-10-12 03:30:00",
    "in_reply_to": null,
    "references": null,
    "body_plain": "This series improves test coverage...",
    "body_hydrated": true,
    "is_patch": false,
    "tags": ["$cl_1"]
  }
]
```

---

### 3.4 `list_recent_emails`
Lists recent email summaries with optional folder, pagination, sender exclusion, tag, and patch filters.

#### Parameters:
| Parameter | Type | Required | Default | Description |
|---|---|---|---|---|
| `folder` | `str` | No | `null` | Folder name to filter by (e.g. `"b4-imports"`, `"Drafts"`, `"TODO"`, `"Sent"`). Omit to query across all folders. |
| `limit` | `int` | No | `20` | Maximum number of message summaries to return. |
| `offset` | `int` | No | `0` | Pagination offset. |
| `exclude_from` | `list[str]` | No | `[]` | Email addresses to exclude (`"self"` expands to configured user emails). |
| `has_tags` | `list[str]` | No | `[]` | Return only messages carrying at least one of these tags. |
| `without_tags` | `list[str]` | No | `[]` | Exclude messages carrying any of these tags. |
| `is_patch` | `bool` | No | `null` | Optional filter for patch messages (`true` / `false`). |

#### Returns:
```json
[
  {
    "id": 102,
    "message_id": "patch.1791745088.git.author@kernel.org",
    "subject": "[PATCH v7 1/2] selftests/ftrace: fix path resolution",
    "from_addr": "dev@kernel.org",
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

### 3.5 `search_emails`
Performs full-text search across indexed kernel emails using SQLite FTS5, with optional folder and tag filtering.

#### Parameters:
| Parameter | Type | Required | Default | Description |
|---|---|---|---|---|
| `query` | `str` | Yes | — | Search query terms or FTS5 syntax (e.g. `"ftrace AND kprobe"`). |
| `limit` | `int` | No | `20` | Maximum search results to return. |
| `folder` | `str` | No | `null` | Optional folder name filter. |
| `has_tags` | `list[str]` | No | `[]` | Return only matches with at least one of these tags. |
| `without_tags` | `list[str]` | No | `[]` | Exclude matches carrying any of these tags. |

#### Returns:
Array of matching messages containing `folder_name`, `tags`, matched headline snippets, subject, and sender metadata.

---

### 3.5a `list_threads`
Aggregates messages in a folder into threads / patch series with series-level counts, tag filtering, and cursor pagination.

#### Parameters:
| Parameter | Type | Required | Default | Description |
|---|---|---|---|---|
| `folder` | `str` | No | `null` | Folder name to scope threads (e.g. `"TODO"`). |
| `limit` | `int` | No | `30` | Threads per page (`1`–`200`). |
| `cursor` | `str` | No | `null` | Opaque pagination cursor returned in `next_cursor`. |
| `exclude_from` | `list[str]` | No | `[]` | Root author email addresses to exclude (`"self"` expands to user identity emails). |
| `patches_only` | `bool` | No | `false` | Return only threads containing at least one patch in the target folder. |
| `has_tags` | `list[str]` | No | `[]` | Return only threads carrying at least one of these tags. |
| `without_tags` | `list[str]` | No | `[]` | Exclude threads carrying any of these tags (e.g. `["$cl_1","$cl_2","$cl_3","$cl_5"]`). |
| `sort` | `str` | No | `"latest_date_desc"` | `"latest_date_desc"` or `"latest_date_asc"`. |

#### Returns:
`{"total_threads": int, "returned": int, "next_cursor": str|null, "threads": [{"thread_id", "root_message_id", "subject", "author_name", "author_addr", "latest_date", "patch_count_in_folder", "total_messages_in_thread", "reply_count", "folder_message_ids", "tags", "tags_consistent", "has_my_reply", "has_unhydrated_replies"}]}`

---

### 3.5b `update_email_tags`
Adds, removes, or replaces UndineD / IMAP keyword tags (e.g. `$cl_1`–`$cl_3`, `$cl_5`) on existing emails or threads without sending, moving, or deleting mail.

#### Parameters:
| Parameter | Type | Required | Default | Description |
|---|---|---|---|---|
| `message_ids` | `list[str]` | Yes | — | `1`–`100` target Message-IDs (with or without `<...>`) or numeric DB IDs. |
| `add_tags` | `list[str]` | No | `[]` | RFC 3501 `flag-keyword` tags to add (`[A-Za-z0-9$_\-.]`, 1–64 chars). |
| `remove_tags` | `list[str]` | No | `[]` | Tags to remove (cannot overlap `add_tags` or combine with `replace_tags`). |
| `replace_tags` | `list[str]` | No | `null` | If non-null, replaces the entire user-tag set (mutually exclusive with `add_tags`/`remove_tags`). |
| `scope` | `str` | No | `"message"` | `"message"` or `"thread"` (expands to all messages in the thread). |
| `folder` | `str` | No | `null` | Optional folder guard (e.g. `"TODO"`). Default `null` updates all folder copies sharing the target `Message-ID`(s). |
| `dry_run` | `bool` | No | `false` | Validates and returns planned tag diffs without mutating DB, `imap_sync_queue`, or SSE. |

#### Returns:
`{"success": true, "dry_run": bool, "scope": str, "folder_filter": str|null, "updated_count": int, "unchanged_count": int, "imap_sync_queued_count": int, "results": [{"id", "message_id", "folder_name", "before_tags", "after_tags", "changed", "imap_sync_queued"}], "not_found": list[str]}`

---

### 3.5c `list_tags`
Lists configured UndineD color labels (ordered by priority rank) and known custom tags with optional per-folder message counts.
- **Parameters**: `folder: Optional[str] = None`
- **Returns**: `{"color_labels": [{"tag", "display_name", "color", "count"}], "custom_tags": [{"tag", "count"}]}`

---

### 3.5d `get_user_identity`
Returns the configured user's display name and deduplicated email addresses/aliases for self-authorship filtering (never exposes passwords or tokens).
- **Parameters**: *(none)*
- **Returns**: `{"display_name": str, "primary_email": str, "aliases": list[str]}`

---

### 3.6 `write_draft_message`
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
| `attachments` | `list[str\|dict]` | No | `null` | Optional array of local file paths or attachment objects (`path`/`file_path`, `content`, or `content_base64`; total <= 25 MiB). |
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

### 3.7 `create_review_draft`
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

### 3.8 `create_mail_draft`
Stages a standalone strict 8-bit UTF-8 plain-text email draft into Maildir `Drafts` without requiring a `target_message_id` reply parent (ideal for Pull Requests and new threads). Preserves raw `\t` tabs and newlines verbatim and never invokes SMTP.

#### Parameters:
| Parameter | Type | Required | Default | Description |
|---|---|---|---|---|
| `to` | `list[str]` | Yes | — | Recipient email addresses (`Name <addr>` or `addr`). |
| `cc` | `list[str]` | No | `[]` | Optional carbon-copy recipients. |
| `subject` | `str` | Yes | — | Email subject line (e.g. `[GIT PULL] probes: Fixes for v6.14-rc3`). |
| `body` | `str` | Yes | — | Strict plain-text UTF-8 body (tabs and newlines preserved verbatim). |
| `in_reply_to` | `str` | No | `""` | Optional parent `Message-ID`. |
| `references` | `list[str]` | No | `[]` | Optional `References` Message-ID chain. |
| `extra_headers` | `dict[str, str]` | No | `{}` | Optional allowlisted `X-*` custom headers (rejects non-`X-*`, `X-Mailer`, `X-Undine-Series-*`, and CR/LF). |
| `tags` | `list[str]` | No | `[]` | Optional UndineD color/custom tags to attach (e.g. `["PR-Draft"]`). |

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

### 3.9 `stage_patch_series`
Stages a complete threaded patch series (optional `0/N` cover letter + `1/N..N/N` patches) into Maildir `Drafts` and registers it in SQLite (`patch_series_drafts` / `patch_series_items`) as a unified draft queue. Emits a `series_draft_updated` SSE event when connected to an Event Hub. Never invokes SMTP.

#### Parameters:
| Parameter | Type | Required | Default | Description |
|---|---|---|---|---|
| `series_key` | `str` | Yes | — | Stable logical key (e.g. `topic/fprobe-cleanup:v2`) for idempotency. |
| `branch` | `str` | No | `""` | Optional source Git branch name. |
| `version` | `int` | No | `1` | Series version number. |
| `to` | `list[str]` | Yes | — | Shared `To` recipients for the series. |
| `cc` | `list[str]` | No | `[]` | Optional shared `Cc` recipients for the series. |
| `in_reply_to` | `str` | No | `""` | Optional external thread `Message-ID`. |
| `base_commit` | `str` | No | `""` | Optional upstream base commit SHA. |
| `replace_existing` | `bool` | No | `false` | If `true` and an unsent draft queue with `series_key` exists, replaces it cleanly. Refused if any item is already `sent` or `sending`. |
| `messages` | `list[dict]` | Yes | — | Ordered array of `SeriesMessage` objects (`index`, `is_cover_letter`, `subject`, `body`, optional `commit_sha`, `extra_to`, `extra_cc`). |

#### Threading & Headers:
- Generates deterministic `git send-email`-style Message-IDs (`<YYYYMMDDHHMMSS.<token>-<seq>-<from_email>>`).
- Links `1..N` via `In-Reply-To: <root_message_id>` and `References: [<optional_parent>, <root_message_id>]`.
- Injects internal tracking headers (`X-Undine-Series-ID`, `X-Undine-Series-Index`, `X-Undine-Series-Total`) to be stripped prior to human-initiated SMTP send.

#### Returns:
```json
{
  "series_id": "ser_...",
  "series_key": "topic/fprobe-cleanup:v2",
  "message_count": 3,
  "root_message_id": "<20261123112500.41022-1-dev@kernel.org>",
  "items": [
    { "index": 0, "draft_id": "57", "message_id": "<...-1-...>", "status": "draft" },
    { "index": 1, "draft_id": "58", "message_id": "<...-2-...>", "status": "draft" }
  ],
  "web_url": "http://127.0.0.1:8080/#series/ser_..."
}
```

---

### 3.10 `export_thread_mbox`
Exports a local email thread or patch series from Maildir/SQLite as a `git am`-ready mbox file without duplicate network fetches.

#### Parameters:
| Parameter | Type | Required | Default | Description |
|---|---|---|---|---|
| `message_id` | `str` | Yes | — | Any Message-ID (or numeric DB ID) in the target patch or series thread. |
| `output_path` | `str` | No | Temp file | Optional destination path restricted to allowed temp/cache/exports directories (rejects symlinks and path escapes). |
| `patches_only` | `bool` | No | `true` | Filters to patch messages ordered by `[PATCH x/N]` and injects deduplicated review trailers (`Reviewed-by:`, `Acked-by:`, etc.) from thread replies before `---`. |

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

### 3.11 `import_patch_series_b4`
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

### 3.12 Patchwork Integration Tools

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
