# UndineD Backend REST Interface Specification

This document provides a comprehensive technical specification of the REST API exposed by the **UndineD** daemon (`backend/app.py`).

Interactive Swagger UI documentation is available at `/docs` (e.g., `http://127.0.0.1:8080/docs`) and raw OpenAPI v3 schemas are served at `/openapi.json`.

---

## 1. Architectural Overview & Security Model

The UndineD backend is implemented with **FastAPI** / **Uvicorn** and strictly enforces local-first multi-layered security (Section 8 of `DESIGN.md`):

1. **Layer 1: Secret Filesystem Token**
   - On daemon startup, a 256-bit entropy token is generated and written to `~/.local/share/undined/auth_token` with strict POSIX `0600` permissions.
   - Persistent application logs strictly record only the SHA-256 fingerprint of the token.
2. **Layer 2: Authentication Handshake**
   - Every request to `/api/*` requires a valid authentication token provided via one of:
     - `Authorization: Bearer <token>` header
     - Session cookie: `undine_session=<token>` (HTTPOnly, SameSite=Strict)
     - Query parameter: `?token=<token>` (used for initial browser bootstrap)
3. **Layer 3: Host Header & DNS Rebinding Defense**
   - Incoming `Host` headers must strictly match allowed local interfaces (`127.0.0.1:<port>`, `localhost:<port>`, `127.0.0.1`, `localhost`).
4. **Layer 4: CSRF Origin Protection**
   - Mutation requests (`POST`, `PUT`, `DELETE`, `PATCH`) must originate from trusted local origins (`http://127.0.0.1:<port>`, `http://localhost:<port>`).

---

## 2. Authentication & Web UI Endpoints

### `GET /`
Serves the single-page application frontend (`index.html`) or redirects to bootstrap session cookies.

- **Query Parameters**:
  - `token` (optional, string): Authentication token for one-click browser launch.
- **Behavior**:
  - If a valid `token` is supplied in the query string, responds with HTTP 302 redirecting to `/` and sets the `undine_session` cookie.
  - If an authenticated session cookie or dependency override is present, serves `frontend/index.html`.
  - Otherwise, responds with HTTP 401 rendering the terminal-themed login HTML page.

### `GET /login`
Renders the terminal-themed authentication HTML login page.

### `POST /login`
Authenticates a user via web form or JSON payload.

- **Content-Type**: `application/x-www-form-urlencoded` or `application/json`
- **Request Body**:
  ```json
  {
    "token": "3e3d242c3ab1870f5c68dcdbf6d138a1b09ac39f6a60fa19e49796dd999ecf5e"
  }
  ```
- **Responses**:
  - `302 Found`: Valid token; redirects to `/` with `Set-Cookie: undine_session=<token>; HttpOnly; SameSite=Strict; Path=/`.
  - `401 Unauthorized`: Invalid token; re-renders login form with error notification.

---

## 3. Mailbox / Folder Endpoints

### `GET /api/folders`
Lists all mailboxes indexed in SQLite and Maildir storage.

- **Response `200 OK`**:
  ```json
  [
    {
      "id": 1,
      "name": "INBOX",
      "sync_enabled": 1,
      "is_imap": true,
      "unread_count": 5,
      "total_count": 120
    },
    {
      "id": 2,
      "name": "TODO",
      "sync_enabled": 0,
      "is_imap": false,
      "unread_count": 2,
      "total_count": 15
    },
    {
      "id": 3,
      "name": "b4-imports",
      "sync_enabled": 0,
      "is_imap": false,
      "unread_count": 0,
      "total_count": 42
    },
    {
      "id": 4,
      "name": "AI-reviews",
      "sync_enabled": 0,
      "is_imap": false,
      "unread_count": 1,
      "total_count": 8
    }
  ]
  ```

### `POST /api/folders/{folder_id}/sync-full`
Triggers an incremental/full summary download from IMAP for the designated mailbox without downloading full email bodies.

- **Path Parameters**:
  - `folder_id` (integer): Database ID of the folder.
- **Responses**:
  - `200 OK`:
    ```json
    {
      "status": "ok",
      "folder_id": 1,
      "folder_name": "kernel.org/dev",
      "synced_count": 34,
      "removed_count": 2
    }
    ```
  - `400 Bad Request`: Folder is not an IMAP account folder or IMAP is not configured.
  - `404 Not Found`: Folder does not exist.
  - `409 Conflict`: Sync is already in progress for this folder.

### `GET /api/folders/{folder_id}/sync-status`
Retrieves background sync progress for a mailbox.

- **Path Parameters**:
  - `folder_id` (integer): Database ID of the folder.
- **Response `200 OK`**:
  ```json
  {
    "is_syncing": true,
    "synced": 150,
    "total": 300,
    "status": "syncing"
  }
  ```

### `POST /api/folders/{folder_id}/sync-cancel`
Cancels an ongoing IMAP summary sync.

- **Response `200 OK`**:
  ```json
  {
    "status": "ok",
    "folder_id": 1,
    "cancelled": true
  }
  ```

---

## 4. Message Management Endpoints

### `GET /api/messages`
Lists message summaries with support for sorting, folder filtering, and pagination.

- **Query Parameters**:
  - `folder_id` (optional, integer): Filter by folder ID.
  - `limit` (optional, integer, $\ge 1$): Maximum messages to return.
  - `offset` (optional, integer, $\ge 0$, default `0`): Pagination offset.
  - `threaded` (optional, boolean, default `true`): Whether messages should be ordered for thread hierarchy.
  - `sort_by` (optional, string, default `"date"`): Sort key (`"date"`, `"subject"`, `"from"`).
  - `sort_order` (optional, string, default `"desc"`): Sort direction (`"asc"`, `"desc"`).
- **Response `200 OK`**:
  ```json
  [
    {
      "id": 101,
      "folder_id": 1,
      "folder_name": "INBOX",
      "message_id": "<20261015010203.12345-1-dev@kernel.org>",
      "subject": "[PATCH v2 1/2] tracing/probes: Fix symbol parsing",
      "from_name": "Kernel Developer",
      "from_addr": "dev@kernel.org",
      "to_addr": "linux-trace-kernel@vger.kernel.org",
      "date": "Wed, 15 Oct 2026 10:02:03 +0000",
      "epoch_timestamp": 1792058523,
      "in_reply_to": null,
      "is_read": 0,
      "is_replied": 0,
      "is_patch": 1,
      "review_commit_id": "4ac73dd24e3123cdcdb3e938dc7bd74be66a3f77",
      "tag_key": "$cl_1",
      "tag_name": "Review",
      "tag_color": "#f38ba8"
    }
  ]
  ```

### `GET /api/messages/{message_id_or_db_id}`
Retrieves full message details, including parsed RFC822 headers, unquoted plain-text body, parsed patch hunks, inline review comments, parent thread linking, and Patchwork / AI review associations.

- **Path Parameters**:
  - `message_id_or_db_id` (string or integer): Message database ID or RFC822 `Message-ID`.
- **Response `200 OK`**:
  ```json
  {
    "message": {
      "id": 101,
      "folder_id": 1,
      "message_id": "<20261015010203.12345-1-dev@kernel.org>",
      "subject": "[PATCH v2 1/2] tracing/probes: Fix symbol parsing",
      "from_name": "Kernel Developer",
      "from_addr": "dev@kernel.org",
      "to_addr": "linux-trace-kernel@vger.kernel.org",
      "cc_addr": "dev@kernel.org",
      "body_plain": "Commit message details...\n---\n diff --git a/kernel/trace/trace_probe.c...",
      "is_patch": 1,
      "is_read": 1,
      "is_replied": 0,
      "file_path": "/home/dev/.local/share/undined/maildir/INBOX/cur/1792058523.M123P456:2,S",
      "review_commit_id": "4ac73dd24e3123cdcdb3e938dc7bd74be66a3f77"
    },
    "comments": [
      {
        "id": 1,
        "message_id": "<20261015010203.12345-1-dev@kernel.org>",
        "file_path": "kernel/trace/trace_probe.c",
        "line_no": 45,
        "side": "new",
        "state": "draft",
        "author": "user",
        "text": "Check return value of kstrdup here."
      }
    ],
    "missing_parent": false,
    "parent_msgid": "<20261015010000.12300-1-dev@kernel.org>",
    "parent_info": {
      "id": 99,
      "subject": "[PATCH v2 0/2] tracing/probes: Cleanups",
      "folder_id": 1,
      "folder_name": "INBOX",
      "is_same_folder": true
    },
    "parsed_diff": {
      "is_patch": true,
      "files": [
        {
          "old_path": "kernel/trace/trace_probe.c",
          "new_path": "kernel/trace/trace_probe.c",
          "hunks": [
            {
              "old_start": 40,
              "old_lines": 10,
              "new_start": 40,
              "new_lines": 12,
              "header": "@@ -40,10 +40,12 @@ static int parse_probe_vars...",
              "lines": [
                {"type": "context", "old_no": 40, "new_no": 40, "text": " int ret;"},
                {"type": "add", "old_no": null, "new_no": 41, "text": "+    if (!var) return -EINVAL;"}
              ]
            }
          ]
        }
      ]
    },
    "raw_headers": "From: dev@kernel.org\nTo: linux-trace-kernel@vger.kernel.org\n...",
    "attachments": [
      {
        "index": 0,
        "filename": "0001-fix.patch",
        "content_type": "text/x-diff",
        "size": 3330
      }
    ],
    "lore_url": "https://lore.kernel.org/r/20261015010203.12345-1-dev@kernel.org",
    "reviewed_message": null
  }
  ```

### `GET /api/messages/{message_id_or_db_id}/attachments/{index}`
Downloads the specified MIME attachment decoded from its transfer encoding (Base64, Quoted-Printable, 7bit/8bit). Also accessible via `GET /api/messages/{message_id_or_db_id}/attachments/{index}/{filename}` for friendly browser saving and tools like `curl -OJ`.

- **Path Parameters**:
  - `message_id_or_db_id` (string or integer): Message database ID or RFC822 `Message-ID`.
  - `index` (integer, $\ge 0$): 0-based attachment index.
  - `filename` (optional, string): Friendly filename in path.
- **Headers Returned**:
  - `Content-Type`: MIME content type (e.g., `text/x-diff`, `application/pdf`, `image/png`).
  - `Content-Disposition`: `attachment; filename="<filename>"; filename*=UTF-8''<encoded>`
  - `Content-Length`: Size of the decoded attachment in bytes.
  - `X-Content-Type-Options`: `nosniff`
  - `Content-Security-Policy`: `default-src 'none'`
- **Responses**:
  - `200 OK`: Binary/text payload of the decoded attachment.
  - `400 Bad Request`: Invalid attachment index.
  - `404 Not Found`: Message or attachment index not found.

### `GET /api/messages/{message_id_or_db_id}/raw-header`
Retrieves the unparsed RFC822 email headers.

- **Response `200 OK`**:
  ```json
  {
    "message_id": "<20261015010203.12345-1-dev@kernel.org>",
    "raw_headers": "From: dev@kernel.org\nTo: linux-trace-kernel@vger.kernel.org\nSubject: ...\nDate: ..."
  }
  ```

### `GET /api/messages/{message_id_or_db_id}/mbox-path`
Resolves and verifies the current on-disk message/maildir file path. Hydrates missing files if necessary.

- **Response `200 OK`**:
  ```json
  {
    "success": true,
    "file_path": "/home/dev/.local/share/undined/maildir/INBOX/cur/1792058523.M123P456:2,S"
  }
  ```

### `DELETE /api/messages/{message_id_or_db_id}`
Deletes a message from database indexing and Maildir storage.

- **Response `200 OK`**:
  ```json
  {
    "success": true,
    "id": 101,
    "message": "Message deleted successfully"
  }
  ```

### `POST /api/messages/{message_db_id}/read`
Updates the read/seen status (`\Seen` Maildir flag `S`).

- **Request Body**:
  ```json
  {
    "is_read": true
  }
  ```
- **Response `200 OK`**:
  ```json
  {
    "success": true,
    "is_read": true,
    "file_path": "/home/dev/.local/share/undined/maildir/INBOX/cur/1792058523.M123P456:2,S"
  }
  ```

### `POST /api/messages/{message_db_id}/replied`
Updates the replied/answered status (`\Answered` Maildir flag `R`).

- **Request Body**:
  ```json
  {
    "is_replied": true
  }
  ```
- **Response `200 OK`**:
  ```json
  {
    "success": true,
    "is_replied": true,
    "file_path": "/home/dev/.local/share/undined/maildir/INBOX/cur/1792058523.M123P456:2,RS"
  }
  ```

### `POST /api/messages/{message_db_id}/tag`
Applies or removes user-defined color/label tags (`$cl_1` – `$cl_9` or custom keyword flags), synchronizing with IMAP flags if applicable.

- **Request Body**:
  ```json
  {
    "tag": "$cl_1"
  }
  ```
  *(Pass `null` or `""` to clear the tag)*
- **Response `200 OK`**:
  ```json
  {
    "success": true,
    "message_id": 101,
    "flags": "$cl_1",
    "tag_key": "$cl_1",
    "tag_name": "Review",
    "tag_color": "#f38ba8"
  }
  ```

---

## 5. Inline Review Comments & Notes

### `GET /api/messages/{message_id_or_db_id}/comments`
Lists inline patch review comments for a message.

- **Query Parameters**:
  - `state` (optional, string): Filter by `"draft"` or `"submitted"`.
- **Response `200 OK`**:
  ```json
  {
    "message_id": "<20261015010203.12345-1-dev@kernel.org>",
    "comments": [
      {
        "id": 1,
        "message_id": "<20261015010203.12345-1-dev@kernel.org>",
        "file_path": "kernel/trace/trace_probe.c",
        "line_no": 45,
        "side": "new",
        "state": "draft",
        "author": "user",
        "text": "Need NULL check here.",
        "created_at": 1792059000
      }
    ]
  }
  ```

### `POST /api/messages/{message_id_or_db_id}/comments`
Creates or updates an inline review comment.

- **Request Body**:
  ```json
  {
    "file": "kernel/trace/trace_probe.c",
    "line_no": 45,
    "side": "new",
    "state": "draft",
    "author": "user",
    "text": "Need NULL check here.",
    "comment_id": null
  }
  ```
- **Response `200 OK`**:
  ```json
  {
    "success": true,
    "comment": {
      "id": 1,
      "file_path": "kernel/trace/trace_probe.c",
      "line_no": 45,
      "side": "new",
      "state": "draft",
      "text": "Need NULL check here."
    }
  }
  ```

### `POST /api/messages/{message_id_or_db_id}/comments/discard`
Discards all draft comments for a message.

- **Response `200 OK`**:
  ```json
  {
    "success": true,
    "discarded_count": 3
  }
  ```

### `DELETE /api/messages/{message_id_or_db_id}/comments/{comment_id}`
Deletes a specific comment by ID.

- **Response `200 OK`**:
  ```json
  {
    "success": true
  }
  ```

---

## 6. AI Review Automation Endpoints

Automates background code reviews using Google Antigravity (`agy --print`) or custom command runners. Reviews are queued and status is recorded in the `AI-reviews` folder as messages with status `Queued` or `In-progress`.

### `POST /api/review/trigger`
Triggers or queues a background AI review for a patch.

- **Request Body** (or query parameter `message_id`):
  ```json
  {
    "message_id": "<20261015010203.12345-1-dev@kernel.org>",
    "commit_id": "4ac73dd24e3123cdcdb3e938dc7bd74be66a3f77"
  }
  ```
- **Response `200 OK`**:
  ```json
  {
    "status": "queued",
    "message_id": "<20261015010203.12345-1-dev@kernel.org>",
    "commit_id": "4ac73dd24e3123cdcdb3e938dc7bd74be66a3f77",
    "review_db_id": 205
  }
  ```
- **Errors**:
  - `400 Bad Request`: Missing message or commit reference.
  - `409 Conflict`: Review already in progress or queued for this patch.

### `GET /api/review/status`
Returns the status of the review worker and pending job queue.

- **Response `200 OK`**:
  ```json
  {
    "is_running": true,
    "active_message_id": "<20261015010203.12345-1-dev@kernel.org>",
    "active_commit_id": "4ac73dd24e3123cdcdb3e938dc7bd74be66a3f77",
    "active_review_db_id": 205,
    "status": "in-progress",
    "error": null,
    "output": "Analyzing patch hunks...",
    "queue": [
      {
        "message_id": "<20261015020000.67890-1-dev@kernel.org>",
        "commit_id": "89b4fa2c3ab1",
        "status": "queued",
        "review_db_id": 206
      }
    ],
    "queue_length": 1
  }
  ```

### `POST /api/review/cancel`
Cancels a running or queued review job.

- **Request Body** (or query parameter `message_id`):
  ```json
  {
    "message_id": "<20261015010203.12345-1-dev@kernel.org>"
  }
  ```
- **Response `200 OK`**:
  ```json
  {
    "success": true,
    "cancelled": true
  }
  ```

---

## 7. Search & b4 Integration Endpoints

### `GET /api/search`
Searches indexed emails using SQLite Full-Text Search. Supports keyword prefixes (`from:`, `to:`, `subject:`, `body:`, `has:patch`, `is:unread`, `tag:`).

- **Query Parameters**:
  - `q` (string, required): Query string.
  - `limit` (integer, default `20`): Result limit.
- **Response `200 OK`**:
  ```json
  [
    {
      "id": 101,
      "subject": "[PATCH v2 1/2] tracing/probes: Fix symbol parsing",
      "from_name": "Kernel Developer",
      "from_addr": "dev@kernel.org",
      "date": "Wed, 15 Oct 2026 10:02:03 +0000"
    }
  ]
  ```

### `POST /api/threads/import-b4`
Fetches a complete patch series or thread from `lore.kernel.org` using `b4 mbox` and indexes messages into the `b4-imports` mailbox.

- **Request Body**:
  ```json
  {
    "message_id": "20261015010203.12345-1-dev@kernel.org"
  }
  ```
- **Response `200 OK`**:
  ```json
  {
    "success": true,
    "message_id": "20261015010203.12345-1-dev@kernel.org",
    "imported_count": 2,
    "indexed_ids": [101, 102]
  }
  ```

---

## 8. Git Patch Application Endpoints

### `POST /api/patch/apply`
Applies a patch series to the local git working tree via `git am -3`.

- **Request Body**:
  ```json
  {
    "message_id": "20261015010203.12345-1-dev@kernel.org",
    "target_git_repo": "~/ksrc/review",
    "branch_name": "undine-review/20261015010203",
    "base_commit": "HEAD"
  }
  ```
- **Response `200 OK`**:
  ```json
  {
    "success": true,
    "applied_commits": [
      {
        "commit_id": "4ac73dd24e3123cdcdb3e938dc7bd74be66a3f77",
        "subject": "tracing/probes: Fix symbol parsing",
        "message_id": "20261015010203.12345-1-dev@kernel.org"
      }
    ],
    "branch": "undine-review/20261015010203",
    "original_branch": "master",
    "head_before": "e3c1f13f5e48"
  }
  ```

### `POST /api/patch/apply/abort`
Aborts an in-progress patch application (`git am --abort`) and returns the git working tree to its original branch.

- **Request Body**:
  ```json
  {
    "target_git_repo": "~/ksrc/review",
    "original_branch": "master",
    "head_before": "e3c1f13f5e48"
  }
  ```
- **Response `200 OK`**:
  ```json
  {
    "success": true,
    "message": "Git am aborted and restored to master"
  }
  ```

---

## 9. Patchwork Integration (`patchwork.kernel.org`)

### `GET /api/patchwork/info`
Looks up a patch on Patchwork by `Message-ID`.

- **Query Parameters**:
  - `msgid` (string, required): Message-ID.
  - `refresh` (boolean, default `false`): Bypass cache and query live API.
- **Response `200 OK`**:
  ```json
  {
    "enabled": true,
    "found": true,
    "id": 1234567,
    "state": "under-review",
    "delegate": "dev",
    "is_delegated_to_me": true,
    "checks": {"total": 4, "success": 4, "warning": 0, "failure": 0},
    "series_id": 98765
  }
  ```

### `GET /api/patchwork/my-patches`
Lists patches delegated to the configured maintainer account.

- **Query Parameters**:
  - `state` (optional, string): Filter by state (`"new"`, `"under-review"`, `"queued"`).
  - `project` (optional, string): Filter by project name.
  - `limit` (optional, integer, default `50`).
- **Response `200 OK`**:
  ```json
  {
    "enabled": true,
    "my_username": "dev",
    "count": 1,
    "patches": [
      {
        "id": 1234567,
        "name": "[v2,1/2] tracing/probes: Fix symbol parsing",
        "state": "under-review",
        "delegate": {"username": "dev"},
        "msgid": "20261015010203.12345-1-dev@kernel.org"
      }
    ]
  }
  ```

### `POST /api/patchwork/sync-todo`
Synchronizes delegated patches from Patchwork into the local `TODO` mailbox.

- **Query Parameters**:
  - `states` (optional, repeated string): Statuses to synchronize (defaults to `new`, `under-review`, `queued`).
- **Response `200 OK`**:
  ```json
  {
    "success": true,
    "total_delegated": 25,
    "synced_count": 3,
    "removed_count": 1,
    "patches": [
      {
        "id": 1234567,
        "name": "[v2,1/2] tracing/probes: Fix symbol parsing",
        "msgid": "20261015010203.12345-1-dev@kernel.org",
        "db_id": 105
      }
    ]
  }
  ```

### `GET /api/patchwork/patches/{patch_id}`
Returns raw patch details from the Patchwork REST API v1.4.

### `GET /api/patchwork/patches/{patch_id}/checks`
Returns CI test results and static analyzer checks for a patch.

### `GET /api/patchwork/series/{series_id}`
Returns details for a patch series.

### `PATCH /api/patchwork/patches/{patch_id}`
Updates patch review state, delegate assignment, or commit reference.

- **Request Body**:
  ```json
  {
    "state": "accepted",
    "delegate": "dev",
    "commit_ref": "4ac73dd24e31"
  }
  ```
  *(Pass `delegate: null` to unassign a delegate)*
- **Response `200 OK`**:
  ```json
  {
    "success": true,
    "patch": {"id": 1234567, "state": "accepted", "delegate": "dev"}
  }
  ```

### `POST /api/patchwork/patches/{patch_id}/delegate-to-me`
Convenience endpoint to assign patch delegate to the maintainer.

- **Response `200 OK`**:
  ```json
  {
    "success": true,
    "patch": {"id": 1234567, "delegate": "dev"},
    "message": "Patch #1234567 delegated to dev"
  }
  ```

### `GET /api/patchwork/projects`
Lists configured or available projects on the Patchwork instance.

---

## 10. Composer, Editor & SMTP Endpoints

### `POST /api/composer/preview-quote`
Merges inline diff review comments into the original email body using LKML quoting conventions (`>` quoting, comment indentation, signature).

- **Request Body**:
  ```json
  {
    "message_id": "<20261015010203.12345-1-dev@kernel.org>",
    "notes": [
      {
        "file": "kernel/trace/trace_probe.c",
        "line_no": 45,
        "side": "new",
        "text": "Check return value of kstrdup here."
      }
    ],
    "general_comment": "Thanks for the patch, looks mostly good with one note:"
  }
  ```
- **Response `200 OK`**:
  ```json
  {
    "quote_text": "Thanks for the patch, looks mostly good with one note:\n\nOn Wed, 15 Oct 2026, Kernel Developer wrote:\n> @@ -40,10 +40,12 @@ static int parse_probe_vars...\n> +    if (!var) return -EINVAL;\nCheck return value of kstrdup here.\n\n-- \nKernel Maintainer <dev@kernel.org>",
    "body": "..."
  }
  ```

### `POST /api/composer/open-editor`
Generates a complete RFC822 reply draft with inline comments merged, writes it to a temporary file (`/tmp/undine-reply-*.eml`), and spawns the configured external editor command (`editor_cmd` or `terminal_cmd`).

- **Request Body**:
  ```json
  {
    "message_id": "<20261015010203.12345-1-dev@kernel.org>",
    "notes": [],
    "general_comment": null,
    "custom_to": "dev@kernel.org",
    "custom_cc": "linux-trace-kernel@vger.kernel.org",
    "custom_subject": "Re: [PATCH v2 1/2] tracing/probes: Fix symbol parsing",
    "custom_body": null
  }
  ```
- **Response `200 OK`**:
  ```json
  {
    "success": true,
    "temp_file": "/tmp/undine-reply-a1b2c3d4e5f6.eml",
    "command": "foot -e nvim +10 /tmp/undine-reply-a1b2c3d4e5f6.eml",
    "message": "Editor launched"
  }
  ```

### `POST /api/composer/save-draft`
Saves an email draft to the local `Drafts` mailbox (`maildir/Drafts` and SQLite).

- **Request Body**:
  ```json
  {
    "to_addr": "dev@kernel.org",
    "cc_addr": "linux-trace-kernel@vger.kernel.org",
    "subject": "Re: [PATCH v2 1/2] tracing/probes: Fix symbol parsing",
    "body_text": "Looks good.\n\nAcked-by: Kernel Maintainer <dev@kernel.org>",
    "in_reply_to": "<20261015010203.12345-1-dev@kernel.org>",
    "references": "<20261015010203.12345-1-dev@kernel.org>"
  }
  ```
- **Response `200 OK`**:
  ```json
  {
    "success": true,
    "message_id": "<20261016010000.draft.12345@undined>",
    "db_id": 110,
    "file_path": "/home/dev/.local/share/undined/maildir/Drafts/cur/1792059500.M123P456:2,D"
  }
  ```

### `POST /api/smtp/send`
Transmits an email via the configured SMTP transport (supporting both STARTTLS and SMTP over SSL / SMTPS). Automatically stamps the `X-Mailer: UndineD <version>` header and marks replied parent messages with the `\Answered` (`R`) flag.

- **Request Body**:
  ```json
  {
    "to_addr": "dev@kernel.org",
    "cc_addr": "linux-trace-kernel@vger.kernel.org",
    "subject": "Re: [PATCH v2 1/2] tracing/probes: Fix symbol parsing",
    "body_text": "Reviewed-by: Kernel Maintainer <dev@kernel.org>",
    "in_reply_to": "<20261015010203.12345-1-dev@kernel.org>",
    "references": "<20261015010203.12345-1-dev@kernel.org>",
    "orig_msg_id": 101
  }
  ```
- **Response `200 OK`**:
  ```json
  {
    "success": true,
    "message": "Message sent successfully"
  }
  ```
- **Errors**:
  - `500 Internal Server Error`: SMTP transport failure or authentication error.

---

## 11. Configuration & Metadata Endpoints

### `GET /api/config`
Retrieves public, non-sensitive configuration parameters for the frontend client.

- **Response `200 OK`**:
  ```json
  {
    "editor_cmd": "foot -e nvim +{line} {file}",
    "has_external_editor": true,
    "reply_use_external_editor": false,
    "kernel_git_dir": "/home/dev/ksrc/review",
    "from_name": "Kernel Maintainer",
    "from_email": "dev@kernel.org",
    "patchwork_enabled": true,
    "patchwork_url": "https://patchwork.kernel.org/api/1.4",
    "patchwork_my_username": "dev",
    "patchwork_delegate_users": ["dev", "maintainer"],
    "patchwork_can_update": true,
    "review_cmd": "agy --print",
    "review_prompt_file": "/home/dev/.config/undined/review_prompt.txt",
    "tags": {
      "$cl_1": {"tag": "Review", "color": "#f38ba8", "shortcut": "1"},
      "$cl_2": {"tag": "Bug", "color": "#fab387", "shortcut": "2"},
      "$cl_3": {"tag": "Feature", "color": "#a6e3a1", "shortcut": "3"}
    }
  }
  ```

### `GET /api/tags`
Retrieves tag mappings and keyboard shortcuts.

- **Response `200 OK`**:
  ```json
  {
    "$cl_1": {"tag": "Review", "color": "#f38ba8", "shortcut": "1"},
    "$cl_2": {"tag": "Bug", "color": "#fab387", "shortcut": "2"}
  }
  ```

---

## 12. Known Addresses & Autocomplete Endpoints

### `GET /api/addresses`
Queries the Known Addresses database to retrieve email addresses and display names collected from past emails' `To` and `Cc` headers. Results are ordered with prefix matches first, followed by historical frequency and last-seen timestamps.

- **Query Parameters**:
  - `query` / `q` (string, optional): Search query to filter by email address or display name.
  - `limit` (integer, optional, default `20`, max `100`): Maximum number of matching records to return.
- **Response `200 OK`**:
  ```json
  [
    {
      "id": 1,
      "name": "Kernel Developer",
      "address": "dev@example.com",
      "frequency": 12,
      "last_seen": "2026-10-18 10:00:00",
      "created_at": "2026-10-01 08:00:00"
    },
    {
      "id": 2,
      "name": "Steven Rostedt",
      "address": "rostedt@goodmis.org",
      "frequency": 8,
      "last_seen": "2026-10-17 14:30:00",
      "created_at": "2026-10-02 09:15:00"
    }
  ]
  ```

### `POST /api/addresses/collect`
Triggers a re-scan of all indexed emails in the `messages` table, extracting and harvesting addresses from the `to_addr` and `cc_addr` fields into the `known_addresses` database.

- **Response `200 OK`**:
  ```json
  {
    "success": true,
    "processed": 142,
    "total": 58
  }
  ```

