# DESIGN-VSCODE.md: Undine Review Workstation for VS Code

## 1. Overview & Objectives

**Undine VS Code Extension (`undine-vscode`)** provides an integrated development and review frontend for **UndineD**, a headless MUA daemon tailored for Linux kernel developers. While UndineD operates as a single static binary handling IMAP/SMTP synchronization, SQLite indexing, and local Maildir storage, this extension brings the review workflow directly into Visual Studio Code.

### Key Objectives

* **Unified Review Workflow:** Eliminate context switching between separate email clients (such as Sylpheed or Web mailers) and code editors.


* **Strict LKML Compliance:** Preserve strict plain-text UTF-8 formatting, enforce 8-character tab stops (`tab-size: 8`), prohibit line-wrapping and HTML, and inject required email headers.


* **Native Code Inspection:** Render incoming kernel patches using VS Code's native diff engine and Language Server Protocol (LSP) for definition lookups and reference checking.
* **Seamless b4 & Patchwork Integration:** Trigger on-demand thread hydration via `b4`, apply patches directly into local working trees, and synchronize status with Patchwork.


* **Context-Aware AI Review:** Combine UndineD's Model Context Protocol (MCP) capabilities with VS Code Language Model APIs and local repository context.



---

## 2. System Architecture

```
+---------------------------------------------------------------------------------------+
| Visual Studio Code (Extension Host / TypeScript)                                      |
|                                                                                       |
|  [ Activity Bar / Sidebar Views ] [ Native Diff & Comments ]    [ @undine Copilot ]   |
|   - Mailbox Folders (INBOX, etc.)  - tab-size: 8 Diff Rendering  - LKML Style Check   |
|   - Thread Hierarchy (j/k)         - Inline Review Notes         - Patch Summary      |
|   - Message Header (Lore, CI)      - Quote Merger Draft Engine   - Tree-Aware Review  |
|                  |                             |                          |           |
|                  +-----------------------------+--------------------------+           |
|                                                |                                      |
|                                [ Undine REST API Client ]                             |
|                                (Bearer Token from POSIX 0600)                         |
+------------------------------------------------+--------------------------------------+
                                                 | HTTP REST / Server-Sent Events (SSE)
                                                 v
+---------------------------------------------------------------------------------------+
| UndineD Daemon (:8080)                                                                |
|  - Auth Token File: ~/.local/share/undined/auth_token (0600 permissions)              |
|  - Storage: Maildir Spool & SQLite FTS5 (undine.db)                                   |
|  - External Subprocesses: b4 (lore.kernel.org), git am, Patchwork API v1.4, SMTP      |
+---------------------------------------------------------------------------------------+

```

(UndineD architecture and external integrations:)

---

## 3. Authentication & Communication Lifecycle

UndineD implements a token-based authentication mechanism where a cryptographically secure 256-bit token is saved to the local file system on startup.

### 3.1 Token Auto-Discovery

1. **Target File Path:**
* By default, the extension resolves `~/.local/share/undined/auth_token` following the XDG Base Directory Specification.


* The path can be overridden via `undine.authTokenPath` in VS Code configuration settings.


2. **File Ingestion:**
* The Extension Host reads the token directly using Node.js `fs/promises` or `vscode.workspace.fs`.
* File access permissions (`0600`) are verified to ensure secure token handling.




3. **Secure Storage:**
* Once loaded, the token is held in process memory and optionally registered into `context.secrets` (`vscode.SecretStorage`).


4. **API Requests & Rotation:**
* All outgoing HTTP requests attach the header `Authorization: Bearer <token>`.
* If the daemon responds with `401 Unauthorized` (e.g., after `undined --renew-token`), the extension prompts the user and reloads the token from the file system.





---

## 4. UI/UX Specifications

The user interface adapts the traditional Sylpheed 3-pane paradigm (Folders, Thread Summary, Message Viewer) into native VS Code UI components with a redesigned stacked vertical sidebar:

```
+------------------+------------------------------------+---------------------------------------+
| Activity Bar     | Primary Sidebar (Custom Views)     | Editor / Tab Area                     |
| [Undine Icon]    | ---------------------------------- | ------------------------------------- |
|                  | ▼ Folders                          | Patch Diff Viewer                     |
|                  |   📥 INBOX (12)                    | (font: monospace, tab-size: 8)        |
|                  |   🐧 LKML (145)                    |                                       |
|                  |   📦 b4-imports                    | @@ -230,6 +230,7 @@                   |
|                  |   📝 Drafts                        | + return VM_FAULT_SIGBUS;             |
|                  | ---------------------------------- |                                       |
|                  | ▼ Thread Hierarchy                 | +-- (Inline Review Note) -------------+
|                  |   ├─ [PATCH v2] mm: fix null...    | | "Does this condition ever trigger   |
|                  |   │  └─ Re: [PATCH v2] mm...       | |  for pure anonymous mappings?"      |
|                  |   └─ [PATCH 0/3] trace: new...     | +-------------------------------------+
|                  | ---------------------------------- |                                       |
|                  | ▼ Message Header                   |                                       |
|                  |   [PATCH] [UNREAD]                 |                                       |
|                  |   Subject: [PATCH v2] mm: fix...   |                                       |
|                  |   From: dev@example.com            |                                       |
|                  |   Msg-ID: <...> [lore ↗]           |                                       |
|                  |   Patchwork: #12431 · [PASS]       |                                       |
|                  |   [Description] [Apply] [Reply]    |                                       |
+------------------+------------------------------------+---------------------------------------+
```

### 4.1 Sidebar Views (Stacked Vertical Panes)

* **Folders View (`vscode.TreeDataProvider`):**
  * Displays synchronized mail folders (`INBOX`, `LKML`, `Drafts`, `b4-imports`) along with unread counter badges.

* **Thread Summary View (`vscode.TreeDataProvider`):**
  * Displays email threads in a hierarchical tree view based on `In-Reply-To` and `References` headers.
  * Provides keyboard shortcuts: `j` for next email, `k` for previous email, and `/` to open search.

* **Message Header View (`vscode.WebviewViewProvider`):**
  * Replaces the standalone Patchwork pane in the stacked vertical sidebar with a rich, responsive email/patch header inspector.
  * Displays `From`, `To`, `Cc`, `Subject`, `Date`, and canonical `Message-ID` with direct hyperlinks to `https://lore.kernel.org/r/<msgid>`.
  * Integrates Patchwork status, CI checks badge (`PASS`/`FAIL`), and self-delegation (`Delegate to Me`) directly into the header for incoming patches.
  * Displays thread hydration warnings with one-click `[Hydrate Thread via b4]` if parent emails are missing from the local store.
  * Provides changed file shortcuts to jump directly to specific patch diffs, plus quick maintainer actions: `[Description]`, `[Apply (b4 am)]`, and `[Compose LKML Reply]`.

### 4.2 Patch Diff & Code Viewer
* Displays code patches with strict preservation of tab characters (`tab-size: 8`) and monospace font styling.
* Opens dual-column Side-by-Side or Unified Diff via custom virtual document providers (`undine-patch://<msgid>/<filepath>`).
* **On-Demand Inspection**: By default, selecting an email or patch updates the Message Header pane in the sidebar without immediately opening diff tabs. The diff or commit description is opened on-demand when the developer clicks `[🔍 View Diff]`, `[📝 Description]`, or any file shortcut in the header (or automatically if `undine.autoOpenDiff` is configured).



### 4.3 Inline Review Notes (`vscode.comments` API)

* **Gutter Comment Bubbles:**
* Clicking on line number gutters in the patch diff or commit description mounts an inline comment thread.
* Notes are persisted to UndineD via `POST /api/messages/{id}/comments` (compatible with the `post_review_comment` MCP tool, supporting `DESCRIPTION:LINE`).




* **Quote Merger & Draft Generation:**
* Triggering **"Generate LKML Reply"** aggregates all saved inline notes.


* Original diff hunks without notes are stripped out.
* Hunks containing notes retain 2–3 lines of context, prefixing original lines with `> `.


* The resulting draft is opened directly in a VS Code text editor for final adjustments before dispatch.



---

## 5. REST API Integration Mapping

The extension's TypeScript client interacts with UndineD's HTTP endpoints:

| Endpoint | Method | VS Code Extension Feature |
| --- | --- | --- |
| `/api/folders` | `GET` | Populates the Folders sidebar tree and unread counters. |
| `/api/folders/{folderID}/sync-full` | `POST` | Triggers full sync of folder envelope summaries from IMAP. |
| `/api/folders/{folderID}/sync-status` | `GET` | Queries background folder summary sync progress. |
| `/api/messages?folder_id={id}` | `GET` | Fetches envelope summaries and builds the message thread list. |
| `/api/messages/{id}` | `GET` | Loads email body, patch diff structure, comments, and missing parent status. |
| `/api/search?q={q}` | `GET` | Executes full-text search against the SQLite FTS5 database. |
| `/api/messages/{id}/read` | `POST` | Updates message read/unread status on database and IMAP server. |
| `/api/messages/{id}/replied` | `POST` | Updates message replied status on database and IMAP server. |
| `/api/messages/{id}/tag` | `POST` | Updates custom color tag on message. |
| `/api/messages/{id}/comments` | `GET` / `POST` | Stores and retrieves line-anchored review comments on diffs and commit descriptions (`DESCRIPTION:LINE`). |
| `/api/messages/{id}/comments/{commentID}` | `DELETE` | Deletes a review comment. |
| `/api/composer/preview-quote` | `POST` | Generates LKML-compliant in-reply-to quoted reply text from inline notes. |
| `/api/composer/save-draft` | `POST` | Saves draft reply into local Maildir Drafts folder. |
| `/api/smtp/send` | `POST` | Submits draft for strict plain-text delivery with sender signature. |
| `/api/threads/import-b4` | `POST` | Triggers `b4 mbox` to import missing threads from lore.kernel.org. |
| `/api/patch/apply` | `POST` | Applies patch to local workspace git repo (`git am` / `b4 am`). |
| `/api/patch/apply/abort` | `POST` | Aborts in-progress patch application. |
| `/api/patchwork/info?msgid={msgid}` | `GET` | Queries Patchwork checks and maintainer info. |
| `/api/patchwork/my-patches` | `GET` | Queries Patchwork patches delegated to the maintainer (`new`, `under-review`). |
| `/api/patchwork/patches/{patchID}` | `PATCH` | Updates Patchwork patch state or delegation. |
| `/api/patchwork/patches/{patchID}/delegate-to-me` | `POST` | Self-delegates patch on Patchwork. |
| `/api/review/trigger` | `POST` | Triggers background AI patch review runner. |

---

## 6. AI & Copilot Integration

By bridging UndineD's Model Context Protocol (MCP) server with VS Code's `vscode.lm` (Language Model API) and `vscode.chat` (Chat Participant API), the extension delivers intelligent kernel code reviews.

### 6.1 `@undine` Chat Participant

Developers can invoke `@undine` in the GitHub Copilot Chat view:

* `@undine /review`: Analyzes the active patch against Linux kernel coding conventions, checking for common bugs (e.g., NULL pointer dereferences, locking imbalances, memory leaks).
* `@undine /summarize`: Produces a concise summary of long, multi-party email discussion threads.
* `@undine /draft-reply`: Generates a professional reply draft reflecting inline notes, including standard maintainer review trailers (`Reviewed-by:`, `Acked-by:`).



### 6.2 Workspace Context Augmentation

Unlike standalone email clients that only inspect isolated patch text, the VS Code extension queries the developer's open Linux kernel repository:

* Inspects surrounding function declarations and macros across the local codebase.
* Verifies compatibility with architecture-specific code paths.
* Validates whether modified structures affect external callers in the workspace.

---

## 7. Configuration Settings (`package.json`)

```json
{
  "configuration": {
    "title": "Undine Review Workstation",
    "properties": {
      "undine.serverUrl": {
        "type": "string",
        "default": "http://127.0.0.1:8080",
        "description": "Base URL of the running UndineD daemon."
      },
      "undine.authTokenPath": {
        "type": "string",
        "default": "~/.local/share/undined/auth_token",
        "description": "Path to the authentication token file generated by UndineD."
      },
      "undine.targetGitRepo": {
        "type": "string",
        "default": "${workspaceFolder}",
        "description": "Target Git repository path for b4/git patch application."
      },
      "undine.tabSize": {
        "type": "number",
        "default": 8,
        "description": "Strict tab size for LKML patch and code display."
      },
      "undine.autoOpenDiff": {
        "type": "boolean",
        "default": false,
        "description": "Automatically open the patch diff editor when selecting a message (default: false)."
      }
    }
  }
}

```

(Token path and strict tab size requirements:)

---

## 8. Implementation Roadmap

### Phase 1: Client Foundation & Authentication

* Implement `TokenManager` to resolve and parse `auth_token` with POSIX `0600` verification.


* Implement `UndineRestClient` with automatic `Authorization: Bearer` header injection and health checks.

### Phase 2: Mailbox, Thread, & Header Navigation

* Register the `Undine` icon in the VS Code Activity Bar.
* Build `FoldersTreeProvider`, `ThreadsTreeProvider`, and `MessageHeaderViewProvider` using the REST API.
* Register keyboard navigation commands (`j`, `k`, `/`).



### Phase 3: Patch Viewer & Comments Engine

* Build virtual document provider (`undine-patch://`) enforcing 8-space tab stops and monospace styling.


* Integrate `vscode.comments` controller to capture file and line-specific review notes.


* Connect inline notes to UndineD review comment endpoints.



### Phase 4: Patchwork, b4, & SMTP Dispatch

* Implement one-click thread hydration (`POST /api/threads/import-b4`).


* Add patch application command (`POST /api/patch/apply`) targeting the active workspace repository.


* Integrate LKML quote merger draft generation and strict plain-text sending.



### Phase 5: Language Model API & Copilot Agent

* Implement the `@undine` Chat Participant utilizing `vscode.lm`.
* Expose workspace symbol search and diff context to AI prompts for automated kernel patch reviews.

